package vm

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func signalJournalKernel(t *testing.T, journal *replay.Memory) (*Kernel, proc.Proc) {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:       capability.Set{FD: fd.New()},
		Programs:  registry,
		Journal:   journal,
		Sequencer: nil,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process
}

func TestSignalDeliveryIsRecordedAndNotReplayed(t *testing.T) {
	journal := replay.Record()
	kernel, process := signalJournalKernel(t, journal)
	if err := kernel.Signal(process.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	facts := journal.Facts()
	if len(facts) != 1 || facts[0].Operation != replay.OpSchedule {
		t.Fatalf("应记录一条控制点事实: %+v", facts)
	}
	if len(facts[0].Input) != 9 || replay.ScheduleKind(facts[0].Input[0]) != replay.ScheduleSignal {
		t.Fatalf("控制点类别应为信号: %v", facts[0].Input)
	}

	if got := binary.LittleEndian.Uint64(facts[0].Input[1:]); got != uint64(signal.TERM) {
		t.Fatalf("事实应携带信号编号 %d，实际 %d", signal.TERM, got)
	}

	replayJournal := replay.Replay(facts)
	replayKernel, replayProcess := signalJournalKernel(t, replayJournal)
	if err := replayKernel.Signal(replayProcess.ID, signal.TERM); err != nil {
		t.Fatalf("回放投递应复用记录结果: %v", err)
	}

	pending, err := replayKernel.Pending(replayProcess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("回放不得重新投递信号: %v", pending)
	}

	recordedPending, err := kernel.Pending(process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recordedPending) == 0 {
		t.Fatal("记录模式下信号应已投递")
	}
}

func TestSignalReplayMismatchIsDetected(t *testing.T) {
	journal := replay.Record()
	kernel, process := signalJournalKernel(t, journal)
	if err := kernel.Signal(process.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}

	replayKernel, replayProcess := signalJournalKernel(t, replay.Replay(journal.Facts()))
	if err := replayKernel.Signal(replayProcess.ID, signal.INT); err == nil {
		t.Fatal("投递不同信号必须报分歧")
	}
}

func TestSignalWithoutJournalUnchanged(t *testing.T) {
	kernel, process := signalJournalKernel(t, nil)
	if err := kernel.Signal(process.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	pending, err := kernel.Pending(process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 {
		t.Fatal("未装配事实流时信号应正常投递")
	}
}

func TestKernelControlExposesReadOnlySequence(t *testing.T) {
	ctx := context.Background()
	sequencer := scheduler.New(64)
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
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
	if _, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{}); err != nil {
		t.Fatal(err)
	}
	events := kernel.Control()
	if len(events) == 0 {
		t.Fatal("应能读到控制序列")
	}
	if events[0].Kind != scheduler.KindSpawn {
		t.Fatalf("首条应是 spawn: %+v", events[0])
	}

	events[0].Detail = "被改写"
	again := kernel.Control()
	if again[0].Detail == "被改写" {
		t.Fatal("Control 必须返回副本")
	}

	if err := kernel.Close(ctx); err != nil {
		t.Fatal(err)
	}
	final := kernel.Control()
	if len(final) <= len(again) {
		t.Fatalf("关闭后应追加事件: %d -> %d", len(again), len(final))
	}
	last := final[len(final)-1]
	if last.Kind != scheduler.KindClose {
		t.Fatalf("末条应是 close: %+v", last)
	}
}

func TestKernelControlWithoutSequencerIsNil(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if events := kernel.Control(); events != nil {
		t.Fatalf("未装配记录器应为 nil: %+v", events)
	}
}
