package vm

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
)

func TestSerialPlanMatchesKernelControlSequence(t *testing.T) {
	ctx := context.Background()
	sequencer := scheduler.New(256)
	registry, err := executable.New(map[string]executable.Program{
		"first":  executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
		"second": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:       capability.Set{FD: fd.New()},
		Programs:  registry,
		Sequencer: sequencer,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}

	names := []string{"first", "second"}
	processes := make([]proc.Proc, 0, len(names))
	for _, name := range names {
		process, err := kernel.Begin(0, name, nil, "/", nil, fd.Streams{})
		if err != nil {
			t.Fatal(err)
		}
		processes = append(processes, process)
		if _, err := kernel.RunRegistered(ctx, process); err != nil {
			t.Fatal(err)
		}
	}

	var actual []scheduler.Event
	for _, event := range sequencer.Log() {
		if event.Kind == scheduler.KindSpawn || event.Kind == scheduler.KindExit {
			actual = append(actual, event)
		}
	}
	tasks := make([]scheduler.Task, 0, len(processes))
	for index, process := range processes {
		tasks = append(tasks, scheduler.Task{
			PID:       uint64(process.ID),
			SpawnTick: uint64(index),
			Program:   names[index],
			Cost:      1,
		})
	}
	steps, err := scheduler.Policy{}.Plan(tasks)
	if err != nil {
		t.Fatal(err)
	}
	planned := scheduler.New(256)
	planned.AppendPlan(tasks, steps, nil)
	if !scheduler.Identical(planned.Log(), actual) {
		t.Fatalf("计划与内核真实序列不一致:\n计划=%+v\n实际=%+v", planned.Log(), actual)
	}
}

func TestKernelUsesSharedDetailConstructors(t *testing.T) {
	sequencer := scheduler.New(64)
	registry, err := executable.New(map[string]executable.Program{
		"probe": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 7, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:       capability.Set{FD: fd.New()},
		Programs:  registry,
		Sequencer: sequencer,
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "probe", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	wantSpawn := scheduler.SpawnDetail("probe")
	wantExit := scheduler.ExitDetail(7, 0)
	var spawnDetail, exitDetail string
	for _, event := range sequencer.Log() {
		switch event.Kind {
		case scheduler.KindSpawn:
			spawnDetail = event.Detail
		case scheduler.KindExit:
			exitDetail = event.Detail
		}
	}
	if spawnDetail != wantSpawn {
		t.Fatalf("spawn Detail=%q want %q", spawnDetail, wantSpawn)
	}
	if exitDetail != wantExit {
		t.Fatalf("exit Detail=%q want %q", exitDetail, wantExit)
	}
}
