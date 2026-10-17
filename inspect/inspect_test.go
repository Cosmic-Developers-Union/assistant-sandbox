package inspect

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestKernelSnapshotIsReadOnlyAndComplete(t *testing.T) {
	descriptors := fd.New()
	reg, err := executable.New(map[string]executable.Program{"worker": executable.Func(
		func(ctx context.Context, p proc.Proc) (uint8, error) { return 4, nil },
	)})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{Set: capability.Set{FD: descriptors}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Init != 1 || len(snapshot.Processes) != 1 || snapshot.Processes[0].State != "running" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if snapshot.Running() {
		t.Fatal("init 不计入业务进程")
	}

	child, err := kernel.Begin(0, "worker", []string{"a"}, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.SetPGID(0, child.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err = Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Processes) != 2 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	view := snapshot.Processes[1]
	if view.ID != uint64(child.ID) || view.State != "running" || view.PGID != uint64(child.ID) || view.SID != 1 {
		t.Fatalf("view=%+v", view)
	}
	if len(view.OpenFDs) != 3 {
		t.Fatalf("openFDs=%v", view.OpenFDs)
	}
	if !snapshot.Running() {
		t.Fatal("运行中的子进程应可见")
	}

	if _, err := kernel.RunRegistered(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	before, err := Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if before.Processes[1].State != "zombie" || after.Processes[1].ExitCode != 4 {
		t.Fatalf("before=%+v after=%+v", before.Processes[1], after.Processes[1])
	}
}

func TestClosedKernelReportsState(t *testing.T) {
	reg, _ := executable.New(map[string]executable.Program{"noop": executable.Func(
		func(context.Context, proc.Proc) (uint8, error) { return 0, nil },
	)})
	kernel, _ := vm.New(vm.Services{Programs: reg}, 2)
	child, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(child.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Closed {
		t.Fatal("未调用 Close 不应报告关闭")
	}
	if err := kernel.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Closed {
		t.Fatal("关闭后应可见")
	}

	if len(snapshot.Processes) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
