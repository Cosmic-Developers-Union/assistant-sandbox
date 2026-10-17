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
)

func snapshotKernel(t *testing.T) *Kernel {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"probe": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:       capability.Set{FD: fd.New()},
		Programs:  registry,
		Sequencer: scheduler.New(64),
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return kernel
}

func TestSnapshotTableRoundTripRestoresProcessFacts(t *testing.T) {
	ctx := context.Background()
	kernel := snapshotKernel(t)

	child, err := kernel.Begin(kernel.Init(), "probe", nil, "/work", map[string]string{"K": "V"}, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(ctx, child); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(child.ID); err != nil {
		t.Fatal(err)
	}
	data, err := kernel.SnapshotTable(ctx)
	if err != nil {
		t.Fatal(err)
	}

	extra, err := kernel.Begin(kernel.Init(), "probe", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.RestoreTable(ctx, data); err != nil {
		t.Fatal(err)
	}
	for _, p := range kernel.Processes() {
		if p.ID == extra.ID {
			t.Fatalf("恢复后不应保留快照之后的进程: %+v", p)
		}
	}
	if len(kernel.Processes()) != 1 || kernel.Processes()[0].ID != kernel.Init() {
		t.Fatalf("恢复后应只有 init: %+v", kernel.Processes())
	}

	var sawSnapshot, sawRestore bool
	for _, event := range kernel.sequencer.Log() {
		switch event.Kind {
		case scheduler.KindSnapshot:
			sawSnapshot = true
		case scheduler.KindRestore:
			sawRestore = true
		}
	}
	if !sawSnapshot || !sawRestore {
		t.Fatalf("缺少快照/恢复事件: %+v", kernel.sequencer.Log())
	}
}

func TestSnapshotTableCancelledContext(t *testing.T) {
	kernel := snapshotKernel(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := kernel.SnapshotTable(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestSnapshotTableRequiresQuietPoint(t *testing.T) {
	ctx := context.Background()
	kernel := snapshotKernel(t)
	if _, err := kernel.Begin(kernel.Init(), "probe", nil, "/", nil, fd.Streams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.SnapshotTable(ctx); !errors.Is(err, errno.Busy) {
		t.Fatalf("非安静状态应报 EBUSY: %v", err)
	}

	child, err := kernel.Begin(kernel.Init(), "probe", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(ctx, child); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.SnapshotTable(ctx); !errors.Is(err, errno.Busy) {
		t.Fatalf("僵尸存在时应报 EBUSY: %v", err)
	}
}

func TestRestoreTableRejectsInvalidDataAndClosedKernel(t *testing.T) {
	ctx := context.Background()
	kernel := snapshotKernel(t)
	before := len(kernel.Processes())
	for _, bad := range [][]byte{nil, []byte("{"), []byte(`{"format":"assistant-sandbox-proctable-0"}`)} {
		if err := kernel.RestoreTable(ctx, bad); err == nil {
			t.Fatalf("非法数据必须拒绝: %q", bad)
		}
	}
	if len(kernel.Processes()) != before {
		t.Fatalf("拒绝后表被改动: %+v", kernel.Processes())
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := kernel.RestoreTable(cancelled, []byte("{}")); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}

	if err := kernel.Close(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := snapshotKernel(t).SnapshotTable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.RestoreTable(ctx, data); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后应报 ECANCELED: %v", err)
	}
	if _, err := kernel.SnapshotTable(ctx); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后快照应报 ECANCELED: %v", err)
	}
}
