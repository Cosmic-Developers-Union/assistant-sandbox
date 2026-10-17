package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func execKernel(t *testing.T) (*Kernel, *fd.Table) {
	t.Helper()
	descriptors := fd.New()
	registry, err := executable.New(map[string]executable.Program{
		"old": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 5, nil }),
		"new": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 9, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:       capability.Set{FD: descriptors},
		Programs:  registry,
		Sequencer: scheduler.New(64),
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, descriptors
}

func startGuest(t *testing.T, kernel *Kernel, descriptors *fd.Table) proc.PID {
	t.Helper()
	parent, err := kernel.Begin(0, "old", []string{"resident"}, "/", map[string]string{"K": "V"}, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.SetSID(parent.ID); err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(parent.ID, "old", []string{"arg"}, "/old", map[string]string{"OLD": "1"}, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	_ = descriptors
	return child.ID
}

func TestReplaceKeepsIdentityAndRunsNewProgram(t *testing.T) {
	kernel, _ := execKernel(t)
	pid := startGuest(t, kernel, nil)
	before := processView(t, kernel, pid)

	after, err := kernel.Replace(t.Context(), pid, "new", []string{"a", "b"}, "/new", map[string]string{"NEW": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.Parent != before.Parent {
		t.Fatalf("PID/Parent 必须保持: before=%+v after=%+v", before, after)
	}
	if after.PGID != before.PGID || after.SID != before.SID {
		t.Fatalf("组会话必须保持: before=%+v after=%+v", before, after)
	}
	if after.Program != "new" || after.Dir != "/new" || len(after.Args) != 2 || after.Env["NEW"] != "2" {
		t.Fatalf("程序身份未替换: %+v", after)
	}
	if after.Env["OLD"] != "" {
		t.Fatal("旧环境必须整体替换")
	}
	if after.Generation != before.Generation+1 {
		t.Fatalf("代数必须递增: %d -> %d", before.Generation, after.Generation)
	}

	if processView(t, kernel, pid).Generation != after.Generation {
		t.Fatal("内核视图必须报告新代数")
	}

	result, err := kernel.RunRegistered(t.Context(), after)
	if err != nil || result.Code != 9 {
		t.Fatalf("替换后应运行新程序: result=%+v err=%v", result, err)
	}

	sawExec := false
	for _, event := range kernel.sequencer.Log() {
		if event.Kind == scheduler.KindExec && event.PID == uint64(pid) {
			sawExec = true
		}
	}
	if !sawExec {
		t.Fatalf("控制序列缺少 exec 事件: %+v", kernel.sequencer.Log())
	}
}

func TestReplaceClosesOnlyCloexecDescriptors(t *testing.T) {
	kernel, descriptors := execKernel(t)
	pid := startGuest(t, kernel, descriptors)
	inherited, err := descriptors.InstallNext(uint64(pid), &countedStream{})
	if err != nil {
		t.Fatal(err)
	}
	cloexec, err := descriptors.InstallNext(uint64(pid), &countedStream{})
	if err != nil {
		t.Fatal(err)
	}
	if err := descriptors.SetCloexec(uint64(pid), cloexec, true); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Replace(t.Context(), pid, "new", nil, "/", nil); err != nil {
		t.Fatal(err)
	}
	opened := descriptors.Opened(uint64(pid))
	if !containsDescriptor(opened, inherited) {
		t.Fatalf("未标记描述符必须保留: %v", opened)
	}
	if containsDescriptor(opened, cloexec) {
		t.Fatalf("CLOEXEC 描述符必须关闭: %v", opened)
	}

	for _, stdio := range []uint32{0, 1, 2} {
		if !containsDescriptor(opened, stdio) {
			t.Fatalf("标准流 %d 必须保留: %v", stdio, opened)
		}
	}
}

func TestReplaceResetsExecutedProgramSignalFacts(t *testing.T) {
	kernel, _ := execKernel(t)
	pid := startGuest(t, kernel, nil)
	if err := kernel.Signal(pid, signal.USR1); err != nil {
		t.Fatal(err)
	}
	if err := kernel.signals.SetIgnored(uint64(pid), signal.HUP, true); err != nil {
		t.Fatal(err)
	}
	if err := kernel.signals.SetMasked(uint64(pid), signal.TERM, true); err != nil {
		t.Fatal(err)
	}
	bindInterrupt(kernel, pid)

	if _, err := kernel.Replace(t.Context(), pid, "new", nil, "/", nil); err != nil {
		t.Fatal(err)
	}
	if pending := kernel.Processes(); len(pending) == 0 {
		t.Fatal("进程表不应为空")
	}
	if pending := deliverableCount(kernel, pid); pending != 0 {
		t.Fatalf("待决信号必须清空: %d", pending)
	}
	if interruptFired(kernel, pid) {
		t.Fatal("中断钩子必须随旧程序失效")
	}

	if err := kernel.Signal(pid, signal.HUP); err != nil {
		t.Fatal(err)
	}
	if _, ok := kernel.signals.Deliverable(uint64(pid)); ok {
		t.Fatal("忽略处置必须跨 exec 保留")
	}

	if err := kernel.Signal(pid, signal.TERM); err != nil {
		t.Fatal(err)
	}
	if _, ok := kernel.signals.Deliverable(uint64(pid)); ok {
		t.Fatal("屏蔽必须跨 exec 保留")
	}
	if err := kernel.signals.SetMasked(uint64(pid), signal.TERM, false); err != nil {
		t.Fatal(err)
	}
	if n, ok := kernel.signals.Deliverable(uint64(pid)); !ok || n != signal.TERM {
		t.Fatalf("解除屏蔽后应可投递: n=%v ok=%v", n, ok)
	}
}

func TestReplaceRejectsInvalidTargets(t *testing.T) {
	kernel, _ := execKernel(t)
	pid := startGuest(t, kernel, nil)
	before := processView(t, kernel, pid)

	if _, err := kernel.Replace(t.Context(), pid, "missing", nil, "/", nil); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未知程序应报 ENOENT: %v", err)
	}
	unchanged := processView(t, kernel, pid)
	if unchanged.Program != before.Program || unchanged.Generation != before.Generation {
		t.Fatalf("拒绝后状态必须不变: %+v", unchanged)
	}

	if _, err := kernel.Replace(t.Context(), pid, "new", nil, "relative", nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("相对目录应报 EINVAL: %v", err)
	}
	if _, err := kernel.Replace(t.Context(), pid, "", nil, "/", nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空程序名应报 EINVAL: %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := kernel.Replace(cancelled, pid, "new", nil, "/", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}

	if _, err := kernel.Replace(t.Context(), proc.PID(999), "new", nil, "/", nil); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}

	ready, err := kernel.table.Create(kernel.Init(), "old", nil, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Replace(t.Context(), ready.ID, "new", nil, "/", nil); !errors.Is(err, errno.Busy) {
		t.Fatalf("Ready 进程应报 EBUSY: %v", err)
	}

	if err := kernel.table.Start(ready.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(t.Context(), ready); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Replace(t.Context(), ready.ID, "new", nil, "/", nil); !errors.Is(err, errno.Busy) {
		t.Fatalf("Zombie 进程应报 EBUSY: %v", err)
	}

	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Replace(t.Context(), pid, "new", nil, "/", nil); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后应报 ECANCELED: %v", err)
	}
}

func TestReplaceRefusesWhileExecutorActive(t *testing.T) {
	var kernel *Kernel
	registry, err := executable.New(map[string]executable.Program{
		"blocking": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			if _, err := kernel.Replace(ctx, p.ID, "new", nil, "/", nil); !errors.Is(err, errno.Busy) {
				t.Errorf("运行中应拒绝替换: %v", err)
			}
			return 0, nil
		}),
		"new": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err = New(Services{Programs: registry}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Exec(t.Context(), "blocking", nil); err != nil {
		t.Fatal(err)
	}
}

type countedStream struct{ closed bool }

func (s *countedStream) Read([]byte) (int, error)    { return 0, nil }
func (s *countedStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *countedStream) Close() error                { s.closed = true; return nil }

func containsDescriptor(opened []uint32, want uint32) bool {
	for _, id := range opened {
		if id == want {
			return true
		}
	}
	return false
}

func processView(t *testing.T, kernel *Kernel, id proc.PID) proc.Proc {
	t.Helper()
	for _, p := range kernel.Processes() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("进程 %d 不在表内", id)
	return proc.Proc{}
}

func bindInterrupt(kernel *Kernel, id proc.PID) {
	kernel.signals.BindInterrupt(uint64(id), func(signal.Number) {})
}

func interruptFired(kernel *Kernel, id proc.PID) bool {
	return kernel.signals.Interrupt(uint64(id), signal.USR1)
}

func deliverableCount(kernel *Kernel, id proc.PID) int {
	if _, ok := kernel.signals.Deliverable(uint64(id)); ok {
		return 1
	}
	return 0
}
