package scheduler

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func TestDetailConstructorsAreStable(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{SpawnDetail("sh"), "sh"},

		{ExitDetail(0, 0), "code=0 signal=signal(0)"},
		{ExitDetail(42, 0), "code=42 signal=signal(0)"},
		{ExitDetail(143, signal.TERM), "code=143 signal=SIGTERM"},
		{SignalDetail(signal.INT), "SIGINT"},
		{ReapDetail("sh"), "sh"},
		{ExecDetail("bash", 3), "program=bash generation=3"},
		{SnapshotDetail(1024), "bytes=1024"},
		{RestoreDetail(0), "bytes=0"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q want %q", tc.got, tc.want)
		}
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	policy := Policy{}
	tasks := []Task{
		{PID: 5, SpawnTick: 2, Program: "c", Cost: 1},
		{PID: 3, SpawnTick: 1, Program: "a", Cost: 1},
		{PID: 4, SpawnTick: 1, Program: "b", Cost: 1},
	}
	first, err := policy.Plan(tasks)
	if err != nil {
		t.Fatal(err)
	}

	shuffled := []Task{tasks[2], tasks[0], tasks[1]}
	second, err := policy.Plan(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("长度不同: %d vs %d", len(first), len(second))
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("第 %d 步不同: %+v vs %+v", index, first[index], second[index])
		}
	}

	wantOrder := []uint64{3, 4, 5}
	for index, pid := range wantOrder {
		if first[index].PID != pid {
			t.Fatalf("第 %d 步 PID=%d want %d", index, first[index].PID, pid)
		}
	}
}

func TestPlanHonoursSpawnOrderNotInputOrder(t *testing.T) {
	steps, err := Policy{}.Plan([]Task{
		{PID: 9, SpawnTick: 30, Program: "late", Cost: 1},
		{PID: 2, SpawnTick: 10, Program: "early", Cost: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if steps[0].PID != 2 || steps[1].PID != 9 {
		t.Fatalf("steps=%+v", steps)
	}
}

func TestPlanRejectsInvalidInput(t *testing.T) {
	if _, err := (Policy{}).Plan(nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空任务集应报 EINVAL: %v", err)
	}
	if _, err := (Policy{}).Plan([]Task{{PID: 0, Cost: 1}}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("PID 0 应报 EINVAL: %v", err)
	}
}

func TestPlanWithoutPreemptionRunsToCompletion(t *testing.T) {
	steps, err := Policy{Quantum: 1}.Plan([]Task{
		{PID: 1, Program: "a", Cost: 5},
		{PID: 2, Program: "b", Cost: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("不可抢占应两次跑完: %+v", steps)
	}
	for index, step := range steps {
		if step.Preempted || step.Remaining != 0 {
			t.Fatalf("第 %d 步不应被中断: %+v", index, step)
		}
	}
}

func TestPlanQuantumPreemptsWhenPreemptible(t *testing.T) {
	steps, err := Policy{Quantum: 2, Preemptible: true}.Plan([]Task{
		{PID: 1, Program: "a", Cost: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("5 成本 / 2 量子应三步: %+v", steps)
	}
	if !steps[0].Preempted || steps[0].Remaining != 3 {
		t.Fatalf("第一步应剩余 3: %+v", steps[0])
	}
	if !steps[1].Preempted || steps[1].Remaining != 1 {
		t.Fatalf("第二步应剩余 1: %+v", steps[1])
	}
	if steps[2].Preempted || steps[2].Remaining != 0 {
		t.Fatalf("第三步应跑完: %+v", steps[2])
	}
}

func TestExclusiveTaskIgnoresQuantum(t *testing.T) {
	steps, err := Policy{Quantum: 1, Preemptible: true}.Plan([]Task{
		{PID: 1, Program: "pipeline", Cost: 7, Exclusive: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Preempted || steps[0].Remaining != 0 {
		t.Fatalf("Exclusive 任务应一次跑完: %+v", steps)
	}
}

func TestPlanZeroCostAndZeroQuantum(t *testing.T) {
	steps, err := Policy{Quantum: 0, Preemptible: true}.Plan([]Task{
		{PID: 1, Program: "instant", Cost: 0},
		{PID: 2, Program: "work", Cost: 4},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(steps) != 1 {
		t.Fatalf("只有非零成本任务产生步骤: %+v", steps)
	}
	if steps[0].PID != 2 || steps[0].Remaining != 0 {
		t.Fatalf("量子 0 应一次跑完: %+v", steps[0])
	}
}

func TestAppendPlanIsReproducible(t *testing.T) {
	tasks := []Task{
		{PID: 7, SpawnTick: 1, Program: "a", Cost: 1},
		{PID: 8, SpawnTick: 2, Program: "b", Cost: 1},
	}
	steps, err := Policy{}.Plan(tasks)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[uint64]Outcome{8: {Code: 3}}
	first, second := New(64), New(64)
	first.AppendPlan(tasks, steps, outcomes)
	second.AppendPlan(tasks, steps, outcomes)
	if !Identical(first.Log(), second.Log()) {
		t.Fatalf("计划写出不可复现:\n%+v\n%+v", first.Log(), second.Log())
	}
	want := []Event{
		{Seq: 1, PID: 7, Kind: KindSpawn, Detail: "a"},
		{Seq: 2, PID: 7, Kind: KindExit, Detail: "code=0 signal=signal(0)"},
		{Seq: 3, PID: 8, Kind: KindSpawn, Detail: "b"},
		{Seq: 4, PID: 8, Kind: KindExit, Detail: "code=3 signal=signal(0)"},
	}
	if !Identical(first.Log(), want) {
		t.Fatalf("序列不符:\n%+v", first.Log())
	}

	fallback := New(64)
	fallback.AppendPlan(tasks, steps, nil)
	zero := New(64)
	zero.AppendPlan(tasks, steps, map[uint64]Outcome{7: {Code: 0}, 8: {Code: 0}})
	if !Identical(fallback.Log(), zero.Log()) {
		t.Fatalf("nil outcomes 应等价于退出码 0: %+v", fallback.Log())
	}
}

func TestAppendPlanStopsAtCapacity(t *testing.T) {
	tasks := []Task{
		{PID: 1, Program: "a", Cost: 1},
		{PID: 2, Program: "b", Cost: 1},
		{PID: 3, Program: "c", Cost: 1},
	}
	steps, err := Policy{}.Plan(tasks)
	if err != nil {
		t.Fatal(err)
	}

	sequencer := New(3)
	sequencer.AppendPlan(tasks, steps, nil)
	if !sequencer.Full() {
		t.Fatal("应已达容量上限")
	}
	if len(sequencer.Log()) != 3 {
		t.Fatalf("应只写出 3 条: %+v", sequencer.Log())
	}
}
