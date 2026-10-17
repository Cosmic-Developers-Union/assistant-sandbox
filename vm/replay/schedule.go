package replay

import (
	"context"
	"encoding/binary"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type ScheduleKind uint8

const (
	ScheduleSignal ScheduleKind = iota + 1

	ScheduleReap

	ScheduleSnapshot

	ScheduleRestore
)

func (k ScheduleKind) String() string {
	switch k {
	case ScheduleSignal:
		return "signal"
	case ScheduleReap:
		return "reap"
	case ScheduleSnapshot:
		return "snapshot"
	case ScheduleRestore:
		return "restore"
	default:
		return "schedule(" + itoa(uint64(k)) + ")"
	}
}

type ScheduleFact struct {
	Kind ScheduleKind

	Detail uint64
}

const (
	scheduleApplied = 0
	scheduleRefused = 1
)

func RecordSchedule(ctx context.Context, journal Journal, pid uint64, fact ScheduleFact, apply func() (uint64, bool, error)) (uint64, error) {
	if _, err := errnoRequire(journal); err != nil {
		return 0, err
	}

	request := make([]byte, 9)
	request[0] = byte(fact.Kind)
	binary.LittleEndian.PutUint64(request[1:], fact.Detail)
	if recorder, ok := journal.(interface{ Recording() bool }); ok && recorder.Recording() {
		detail, refused, err := apply()
		output := make([]byte, 9)
		if refused {
			output[0] = scheduleRefused
		}
		binary.LittleEndian.PutUint64(output[1:], detail)
		recorded := Fact{PID: pid, Operation: OpSchedule, Input: request, Output: output}
		if recordErr := journal.Record(ctx, recorded); recordErr != nil {
			return 0, err
		}
		return detail, err
	}

	recorded, err := journal.Next(ctx, Fact{PID: pid, Operation: OpSchedule, Input: request})
	if err != nil {
		return 0, err
	}
	if len(recorded.Output) != 9 {
		return 0, errno.Wrap(errno.Invalid, "控制点事实长度不符")
	}
	if recorded.Output[0] == scheduleRefused {
		return binary.LittleEndian.Uint64(recorded.Output[1:]),
			errno.Wrap(errno.NoProcess, "回放的控制点当时被拒绝")
	}
	return binary.LittleEndian.Uint64(recorded.Output[1:]), nil
}

func ReplaySchedule(ctx context.Context, journal Journal, pid uint64, expect ScheduleFact) (uint64, error) {
	kind := expect.Kind
	if _, err := errnoRequire(journal); err != nil {
		return 0, err
	}
	request := make([]byte, 9)
	request[0] = byte(kind)
	binary.LittleEndian.PutUint64(request[1:], expect.Detail)
	recorded, err := journal.Next(ctx, Fact{PID: pid, Operation: OpSchedule, Input: request})
	if err != nil {
		return 0, err
	}
	if len(recorded.Output) != 9 {
		return 0, errno.Wrap(errno.Invalid, "控制点事实长度不符")
	}
	return binary.LittleEndian.Uint64(recorded.Output[1:]), nil
}

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
