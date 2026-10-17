package replay

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestRecordScheduleRoundTrip(t *testing.T) {
	ctx := context.Background()
	journal := Record()
	calls := 0
	apply := func() (uint64, bool, error) {
		calls++
		return 15, false, nil
	}
	detail, err := RecordSchedule(ctx, journal, 3, ScheduleFact{Kind: ScheduleSignal, Detail: 15}, apply)
	if err != nil || detail != 15 {
		t.Fatalf("detail=%d err=%v", detail, err)
	}
	if calls != 1 {
		t.Fatalf("记录模式应执行一次: %d", calls)
	}
	facts := journal.Facts()
	if len(facts) != 1 || facts[0].Operation != OpSchedule {
		t.Fatalf("应记录一条控制点事实: %+v", facts)
	}
	if len(facts[0].Input) != 9 || ScheduleKind(facts[0].Input[0]) != ScheduleSignal {
		t.Fatalf("输入应携带类别与 Detail: %v", facts[0].Input)
	}

	if binary.LittleEndian.Uint64(facts[0].Input[1:]) != 15 {
		t.Fatalf("输入应携带信号编号: %v", facts[0].Input)
	}

	calls = 0
	replayed, err := RecordSchedule(ctx, Replay(facts), 3, ScheduleFact{Kind: ScheduleSignal, Detail: 15}, apply)
	if err != nil || replayed != 15 {
		t.Fatalf("回放 detail=%d err=%v", replayed, err)
	}
	if calls != 0 {
		t.Fatalf("回放不得重新执行控制动作: %d", calls)
	}

	if effect, note := Classify(OpSchedule); effect != SideEffect || note == "" {
		t.Fatalf("控制点应登记为 SideEffect 且带理由: %v %q", effect, note)
	}
}

func TestRecordScheduleReplaysRefusal(t *testing.T) {
	ctx := context.Background()
	journal := Record()
	_, err := RecordSchedule(ctx, journal, 9, ScheduleFact{Kind: ScheduleSignal}, func() (uint64, bool, error) {
		return 0, true, errno.NoProcess
	})

	if !errors.Is(err, errno.NoProcess) {
		t.Fatalf("记录模式应上报拒绝原因: %v", err)
	}
	facts := journal.Facts()
	if len(facts) != 1 || len(facts[0].Output) != 9 || facts[0].Output[0] != scheduleRefused {
		t.Fatalf("拒绝应作为事实入账: %+v", facts)
	}

	_, err = RecordSchedule(ctx, Replay(facts), 9, ScheduleFact{Kind: ScheduleSignal}, func() (uint64, bool, error) {
		t.Fatal("回放不得执行控制动作")
		return 0, false, nil
	})
	if !errors.Is(err, errno.NoProcess) {
		t.Fatalf("回放应复现拒绝身份: %v", err)
	}
}

func TestRecordScheduleMismatches(t *testing.T) {
	ctx := context.Background()
	journal := Record()
	if _, err := RecordSchedule(ctx, journal, 1, ScheduleFact{Kind: ScheduleSnapshot}, func() (uint64, bool, error) {
		return 128, false, nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := RecordSchedule(ctx, Replay(journal.Facts()), 1, ScheduleFact{Kind: ScheduleRestore}, func() (uint64, bool, error) {
		return 0, false, nil
	}); err == nil {
		t.Fatal("类别不符必须失败")
	}

	brokenInput := make([]byte, 9)
	brokenInput[0] = byte(ScheduleSignal)
	binary.LittleEndian.PutUint64(brokenInput[1:], 15)
	broken := Replay([]Fact{{PID: 1, Operation: OpSchedule, Input: brokenInput, Output: []byte{1, 2}}})
	if _, err := RecordSchedule(ctx, broken, 1, ScheduleFact{Kind: ScheduleSignal, Detail: 15}, func() (uint64, bool, error) {
		return 0, false, nil
	}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("长度损坏应报 EINVAL: %v", err)
	}
}

func TestReplayScheduleReadsWithoutApplying(t *testing.T) {
	ctx := context.Background()
	journal := Record()

	if _, err := RecordSchedule(ctx, journal, 5, ScheduleFact{Kind: ScheduleReap, Detail: 42}, func() (uint64, bool, error) {
		return 42, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := ReplaySchedule(ctx, Replay(journal.Facts()), 5, ScheduleFact{Kind: ScheduleReap, Detail: 42})
	if err != nil || detail != 42 {
		t.Fatalf("detail=%d err=%v", detail, err)
	}

	if _, err := ReplaySchedule(ctx, Replay(journal.Facts()), 5, ScheduleFact{Kind: ScheduleSignal, Detail: 42}); err == nil {
		t.Fatal("类别不符必须失败")
	}

	if _, err := ReplaySchedule(ctx, nil, 5, ScheduleFact{Kind: ScheduleReap, Detail: 42}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil Journal 应报 EINVAL: %v", err)
	}
	if _, err := RecordSchedule(ctx, nil, 5, ScheduleFact{Kind: ScheduleReap}, func() (uint64, bool, error) {
		return 0, false, nil
	}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil Journal 应报 EINVAL: %v", err)
	}
}

func TestScheduleKindNames(t *testing.T) {
	for kind, want := range map[ScheduleKind]string{
		ScheduleSignal: "signal", ScheduleReap: "reap",
		ScheduleSnapshot: "snapshot", ScheduleRestore: "restore",
	} {
		if got := kind.String(); got != want {
			t.Errorf("ScheduleKind(%d).String()=%q want %q", kind, got, want)
		}
	}
	if got := ScheduleKind(42).String(); got != "schedule(42)" {
		t.Fatalf("未知类别=%q", got)
	}

	if got := ScheduleKind(0).String(); got != "schedule(0)" {
		t.Fatalf("零值类别=%q", got)
	}
}
