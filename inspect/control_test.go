package inspect

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
)

func controlKernel(t *testing.T) *vm.Kernel {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"one": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
		"two": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 3, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set:       capability.Set{FD: fd.New()},
		Programs:  registry,
		Sequencer: scheduler.New(256),
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return kernel
}

func runScenario(t *testing.T, kernel *vm.Kernel) {
	t.Helper()
	ctx := context.Background()
	for _, program := range []string{"one", "two"} {
		process, err := kernel.Begin(0, program, nil, "/", nil, fd.Streams{})
		if err != nil {
			t.Fatal(err)
		}

		if _, err := kernel.RunRegistered(ctx, process); err != nil {
			t.Fatal(err)
		}
	}
}

func TestControlSequenceIsObservableAndDeterministic(t *testing.T) {
	first := controlKernel(t)
	runScenario(t, first)
	firstEvents := Control(first)
	if len(firstEvents) == 0 {
		t.Fatal("应能观测到控制序列")
	}

	kinds := map[string]int{}
	for _, event := range firstEvents {
		kinds[event.Kind]++
	}
	if kinds[scheduler.KindSpawn] != 2 || kinds[scheduler.KindExit] != 2 {
		t.Fatalf("控制序列不符合预期: %+v", firstEvents)
	}

	if kinds[scheduler.KindClose] != 0 {
		t.Fatalf("未关闭内核不应记录 close: %+v", firstEvents)
	}

	second := controlKernel(t)
	runScenario(t, second)
	secondEvents := Control(second)
	if len(secondEvents) != len(firstEvents) {
		t.Fatalf("两次执行序列长度不同: %d vs %d", len(firstEvents), len(secondEvents))
	}
	for index := range firstEvents {
		if firstEvents[index] != secondEvents[index] {
			t.Fatalf("第 %d 条事件不同:\n%+v\n%+v", index, firstEvents[index], secondEvents[index])
		}
	}

	for index, event := range firstEvents {
		if event.Seq != uint64(index+1) {
			t.Fatalf("第 %d 条序号=%d want %d", index, event.Seq, index+1)
		}
	}
}

func TestControlObservationIsReadOnly(t *testing.T) {
	kernel := controlKernel(t)
	runScenario(t, kernel)
	first := Control(kernel)

	if len(first) > 0 {
		first[0].Detail = "被改写"
		first[0].PID = 9999
	}
	second := Control(kernel)
	if len(second) != len(first) {
		t.Fatalf("长度变了: %d vs %d", len(second), len(first))
	}
	if second[0].Detail == "被改写" || second[0].PID == 9999 {
		t.Fatal("观测返回的必须是副本，改写不得影响序列")
	}

	snapshot, err := Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Control) != len(second) {
		t.Fatalf("快照应包含控制序列: %d vs %d", len(snapshot.Control), len(second))
	}
	snapshot.Control[0].Kind = "被改写"
	third, err := Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if third.Control[0].Kind == "被改写" {
		t.Fatal("快照里的控制序列不得与内核共享底层数组")
	}
}

func TestControlWithoutSequencerIsEmpty(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{
		"one": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if events := Control(kernel); events != nil {
		t.Fatalf("未装配记录器时应为空: %+v", events)
	}
	snapshot, err := Kernel(kernel)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Control) != 0 {
		t.Fatalf("快照不应含控制序列: %+v", snapshot.Control)
	}
}
