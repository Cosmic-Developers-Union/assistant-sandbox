package vm

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func TestSignalTableResetClearsAllState(t *testing.T) {
	table := signal.New()
	table.Spawn(7)
	fired := false
	table.BindInterrupt(7, func(signal.Number) { fired = true })
	if err := table.SetIgnored(7, signal.TERM, true); err != nil {
		t.Fatal(err)
	}
	if err := table.SetMasked(7, signal.INT, true); err != nil {
		t.Fatal(err)
	}
	if err := table.Raise(7, signal.USR1); err != nil {
		t.Fatal(err)
	}
	table.Reset()

	if _, ok := table.Deliverable(7); ok {
		t.Fatal("Reset 后不应仍有可投递信号")
	}
	if _, err := table.Pending(7); err == nil {
		t.Fatal("Reset 后不应仍有待投递信号")
	}

	table.Spawn(7)
	table.Interrupt(7, signal.TERM)
	if fired {
		t.Fatal("Reset 后不应保留中断钩子")
	}
}

func TestRestoreTableRebuildsSignalState(t *testing.T) {
	ctx := context.Background()
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 8)
	if err != nil {
		t.Fatal(err)
	}
	data, err := kernel.SnapshotTable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.RestoreTable(ctx, data); err != nil {
		t.Fatal(err)
	}

	if err := kernel.Signal(kernel.Init(), signal.TERM); err != nil {
		t.Fatalf("恢复后投递应成功: %v", err)
	}
}
