package shell

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

func meterView(ctx context.Context, t *testing.T, kernel *vm.Kernel, parent proc.Proc) resources.View {
	t.Helper()
	meter, err := parent.Resources.Meter()
	if err != nil {
		t.Fatal(err)
	}
	view, err := meter.View(ctx, uint64(parent.ID))
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func budgetedSlot(t *testing.T, work uint64) (*Slot, *vm.Kernel, vmfs.System, proc.Proc) {
	t.Helper()
	dir := t.TempDir()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	meters, err := resources.NewSession(
		resources.Limits{Memory: 1 << 20, Work: work},
		resources.Limits{},
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{
		"bash": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set:      capability.Set{FS: system, FD: fd.New(), Identity: identity.SingleUser{}, Clock: &clock.Virtual{}},
		Programs: registry,
		Meters:   meters,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := kernel.Begin(0, "bash", nil, dir, nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := NewSlot(kernel, parent, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slot.Close(context.Background()) })
	return slot, kernel, system, parent
}

func TestSlotStepChargesWorkBudget(t *testing.T) {
	ctx := context.Background()
	slot, kernel, _, parent := budgetedSlot(t, 1<<20)
	before := meterView(ctx, t, kernel, parent)
	if _, err := slot.Step(ctx, []byte("echo hi")); err != nil {
		t.Fatal(err)
	}
	after := meterView(ctx, t, kernel, parent)
	if after.WorkUsed <= before.WorkUsed {
		t.Fatalf("Step 应扣减工作量: before=%d after=%d", before.WorkUsed, after.WorkUsed)
	}

	firstDelta := after.WorkUsed - before.WorkUsed
	mid := meterView(ctx, t, kernel, parent)
	if _, err := slot.Step(ctx, []byte("echo a-much-longer-command-line")); err != nil {
		t.Fatal(err)
	}
	final := meterView(ctx, t, kernel, parent)
	if final.WorkUsed-mid.WorkUsed <= firstDelta {
		t.Fatalf("更长的输入应消耗更多: %d vs %d", final.WorkUsed-mid.WorkUsed, firstDelta)
	}
}

func TestSlotStepRejectsBeforeSideEffect(t *testing.T) {
	ctx := context.Background()

	slot, _, system, _ := budgetedSlot(t, 1)
	output, err := slot.Step(ctx, []byte("touch /created"))
	if !errors.Is(err, errno.Limit) {
		t.Fatalf("预算不足应报 EAGAIN: %v", err)
	}
	if output.Status != 0 || output.Data != nil {
		t.Fatalf("拒绝时不应产出输出: %+v", output)
	}

	if _, err := system.Stat(ctx, "/created"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("被拒绝的 Step 不得产生副作用: %v", err)
	}

	if _, err := slot.Step(ctx, []byte("cd /")); !errors.Is(err, errno.Limit) {
		t.Fatalf("预算耗尽后仍应被拒: %v", err)
	}
}

func TestSlotStepWithoutMeterIsUnlimited(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{
		"bash": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}

	kernel, err := vm.New(vm.Services{
		Set:      capability.Set{FS: system, FD: fd.New(), Identity: identity.SingleUser{}, Clock: &clock.Virtual{}},
		Programs: registry,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := kernel.Begin(0, "bash", nil, dir, nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := NewSlot(kernel, parent, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := slot.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	output, err := slot.Step(ctx, []byte("echo ok"))
	if err != nil {
		t.Fatalf("未装配计量器不应限制: %v", err)
	}
	if string(output.Data) != "ok\n" {
		t.Fatalf("输出=%q", output.Data)
	}
}
