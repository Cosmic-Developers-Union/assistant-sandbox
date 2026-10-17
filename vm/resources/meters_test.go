package resources

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestSessionRequiresPositiveBudget(t *testing.T) {
	for _, bad := range []Limits{{}, {Memory: 10}, {Work: 10}} {
		if _, err := NewSession(bad, Limits{}); !errors.Is(err, errno.Invalid) {
			t.Fatalf("limits=%+v err=%v", bad, err)
		}
	}
}

func TestSpawnChargeAndViews(t *testing.T) {
	m, err := NewSession(Limits{Memory: 1000, Work: 1000}, Limits{Memory: 200, Work: 300})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}

	view, err := m.View(t.Context(), 1)
	if err != nil || view.MemoryLimit != 200 || view.WorkLimit != 300 {
		t.Fatalf("view=%+v err=%v", view, err)
	}

	if err := m.Charge(t.Context(), 1, 301); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if err := m.Charge(t.Context(), 1, 100); err != nil {
		t.Fatal(err)
	}

	session, _ := m.SessionView(t.Context())
	if session.WorkUsed != 100 {
		t.Fatalf("session=%+v", session)
	}

	release, err := m.Reserve(t.Context(), 1, 150)
	if err != nil {
		t.Fatal(err)
	}
	view, _ = m.View(t.Context(), 1)
	if view.MemoryUsed != 150 {
		t.Fatalf("view=%+v", view)
	}
	release()
	view, _ = m.View(t.Context(), 1)
	if view.MemoryUsed != 0 {
		t.Fatalf("view=%+v", view)
	}

	if err := m.Charge(t.Context(), 99, 1); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if _, err := m.View(t.Context(), 99); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
}

func TestSessionAggregateBudget(t *testing.T) {
	m, _ := NewSession(Limits{Memory: 300, Work: 150}, Limits{Memory: 200, Work: 150})
	if err := m.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Spawn(2, Limits{}); err != nil {
		t.Fatal(err)
	}

	if err := m.Charge(t.Context(), 1, 100); err != nil {
		t.Fatal(err)
	}
	if err := m.Charge(t.Context(), 2, 60); !errors.Is(err, errno.Limit) {
		t.Fatalf("err=%v", err)
	}

	if err := m.Spawn(3, Limits{Memory: 500, Work: 10}); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}

	release, _ := m.Reserve(t.Context(), 1, 200)
	m.Discard(1)
	session, _ := m.SessionView(t.Context())
	if session.WorkUsed != 100 || session.MemoryUsed != 0 {
		t.Fatalf("session=%+v", session)
	}
	release()
	session, _ = m.SessionView(t.Context())
	if session.MemoryUsed != 0 {
		t.Fatalf("session=%+v", session)
	}
}

func TestChargeBeforeSideEffect(t *testing.T) {
	m, _ := NewSession(Limits{Memory: 100, Work: 10}, Limits{Memory: 50, Work: 10})
	if err := m.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}

	if err := m.Charge(t.Context(), 1, 11); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	view, _ := m.View(t.Context(), 1)
	if view.WorkUsed != 0 {
		t.Fatalf("拒绝后仍消耗: %+v", view)
	}
}

func TestCancelRefusesMetering(t *testing.T) {
	m, _ := NewSession(Limits{Memory: 1, Work: 1}, Limits{Memory: 1, Work: 1})
	if err := m.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Charge(ctx, 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func newBudgeted(t *testing.T, session, defaults Limits) *Meters {
	t.Helper()
	meters, err := NewSession(session, defaults)
	if err != nil {
		t.Fatal(err)
	}
	return meters
}

func TestKindBudgetsAreIndependent(t *testing.T) {
	ctx := context.Background()
	meters := newBudgeted(t,
		Limits{Memory: 1 << 20, Work: 1000, Descriptors: 2, Output: 8, Files: 1},
		Limits{})
	if err := meters.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}

	if err := meters.ChargeKind(ctx, 1, KindDescriptors, 2); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, KindDescriptors, 1); !errors.Is(err, errno.Limit) {
		t.Fatalf("描述符超限应报 EAGAIN: %v", err)
	}

	if err := meters.ChargeKind(ctx, 1, KindOutput, 8); err != nil {
		t.Fatalf("输出仍应在预算内: %v", err)
	}
	if err := meters.ChargeKind(ctx, 1, KindOutput, 1); !errors.Is(err, errno.Limit) {
		t.Fatalf("输出超限应报 EAGAIN: %v", err)
	}

	if err := meters.ChargeKind(ctx, 1, KindFiles, 1); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, KindFiles, 1); !errors.Is(err, errno.Limit) {
		t.Fatalf("文件超限应报 EAGAIN: %v", err)
	}

	if err := meters.Charge(ctx, 1, 1000); err != nil {
		t.Fatalf("工作量应独立: %v", err)
	}
	if _, err := meters.Reserve(ctx, 1, 1<<20); err != nil {
		t.Fatalf("内存应独立: %v", err)
	}
}

func TestZeroLimitMeansUnbounded(t *testing.T) {
	ctx := context.Background()
	meters := newBudgeted(t, Limits{Memory: 1 << 20, Work: 100}, Limits{})
	if err := meters.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []Kind{KindDescriptors, KindOutput, KindFiles} {
		if err := meters.ChargeKind(ctx, 1, kind, 1<<20); err != nil {
			t.Fatalf("%v 未设上限应不限制: %v", kind, err)
		}
	}
}

func TestReleaseKindReturnsBudget(t *testing.T) {
	ctx := context.Background()
	meters := newBudgeted(t, Limits{Memory: 1024, Work: 10, Descriptors: 1}, Limits{})
	if err := meters.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, KindDescriptors, 1); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, KindDescriptors, 1); !errors.Is(err, errno.Limit) {
		t.Fatalf("应先用尽: %v", err)
	}
	if err := meters.ReleaseKind(ctx, 1, KindDescriptors, 1); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, KindDescriptors, 1); err != nil {
		t.Fatalf("归还后应可再次占用: %v", err)
	}

	if err := meters.ReleaseKind(ctx, 1, KindDescriptors, 5); !errors.Is(err, errno.Invalid) {
		t.Fatalf("超额归还应报 EINVAL: %v", err)
	}
	view, err := meters.View(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if view.DescriptorUsed != 1 {
		t.Fatalf("超额归还不得改动账目: %+v", view)
	}
}

func TestChargeKindRejectsUnknownKindAndProcess(t *testing.T) {
	ctx := context.Background()
	meters := newBudgeted(t, Limits{Memory: 1024, Work: 10}, Limits{})
	if err := meters.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, Kind(99), 1); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知类别应报 EINVAL: %v", err)
	}
	if err := meters.ChargeKind(ctx, 42, KindOutput, 1); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未登记进程应报 ESRCH: %v", err)
	}
	if err := meters.ReleaseKind(ctx, 1, Kind(99), 1); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知类别归还应报 EINVAL: %v", err)
	}
	if err := meters.ReleaseKind(ctx, 42, KindOutput, 1); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未登记进程归还应报 ESRCH: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := meters.ChargeKind(cancelled, 1, KindOutput, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := meters.ReleaseKind(cancelled, 1, KindOutput, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestDiscardReclaimsReclaimableKinds(t *testing.T) {
	ctx := context.Background()
	meters := newBudgeted(t, Limits{
		Memory: 1 << 20, Work: 1 << 20,
		Descriptors: 10, Output: 1 << 20, Files: 10,
	}, Limits{})
	if err := meters.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, KindDescriptors, 4); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, KindFiles, 2); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 1, KindOutput, 100); err != nil {
		t.Fatal(err)
	}
	if err := meters.Charge(ctx, 1, 50); err != nil {
		t.Fatal(err)
	}
	if _, err := meters.Reserve(ctx, 1, 4096); err != nil {
		t.Fatal(err)
	}
	meters.Discard(1)

	session, err := meters.SessionView(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if session.DescriptorUsed != 0 || session.FileUsed != 0 || session.MemoryUsed != 0 {
		t.Fatalf("可回收类别应归还: %+v", session)
	}

	if session.OutputUsed != 100 || session.WorkUsed != 50 {
		t.Fatalf("累计型类别应保留: %+v", session)
	}

	if err := meters.Spawn(2, Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := meters.ChargeKind(ctx, 2, KindDescriptors, 10); err != nil {
		t.Fatalf("回收后应可用足额描述符: %v", err)
	}

	meters.Discard(999)
}

func TestGuestViewExcludesHostInformation(t *testing.T) {
	ctx := context.Background()
	meters := newBudgeted(t, Limits{Memory: 1024, Work: 10}, Limits{})
	if err := meters.Spawn(1, Limits{}); err != nil {
		t.Fatal(err)
	}
	view, err := meters.View(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if view.WorkUsed != 0 || view.MemoryUsed != 0 || view.OutputUsed != 0 {
		t.Fatalf("未消耗时客体视图应为零: %+v", view)
	}

	if err := meters.Charge(ctx, 1, 7); err != nil {
		t.Fatal(err)
	}
	view, err = meters.View(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}

	if view.WorkUsed != 7 || view.WorkLimit != 10 {
		t.Fatalf("视图应反映消耗与会话上限: %+v", view)
	}

	tight, err := NewSession(
		Limits{Memory: 1024, Work: 10},
		Limits{Memory: 512, Work: 10, Descriptors: 4},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := tight.Spawn(2, Limits{}); err != nil {
		t.Fatal(err)
	}
	inherited, err := tight.View(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if inherited.MemoryLimit != 512 || inherited.WorkLimit != 10 || inherited.DescriptorLimit != 4 {
		t.Fatalf("逐进程默认值应按类别继承: %+v", inherited)
	}
}

func TestKindNamesAreStable(t *testing.T) {
	for kind, want := range map[Kind]string{
		KindMemory: "memory", KindWork: "work", KindDescriptors: "descriptors",
		KindOutput: "output", KindFiles: "files",
	} {
		if got := kind.String(); got != want {
			t.Errorf("Kind(%d).String()=%q want %q", kind, got, want)
		}
	}
	if got := Kind(42).String(); got != "kind(42)" {
		t.Fatalf("未知类别=%q", got)
	}

	for kind := Kind(0); int(kind) < KindCount; kind++ {
		if _, ok := kind.index(); !ok {
			t.Fatalf("Kind(%d) 缺少下标映射", kind)
		}
	}
	if _, ok := Kind(KindCount).index(); ok {
		t.Fatal("越界 Kind 不应有下标")
	}
}
