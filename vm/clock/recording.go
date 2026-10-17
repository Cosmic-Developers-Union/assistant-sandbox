package clock

import (
	"context"
	"encoding/binary"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

type Recording struct {
	source  Source
	journal *replay.Memory
	pid     uint64
}

type Advancer interface {
	SetElapsedTo(nanoseconds uint64) error
}

func NewRecording(source Source, journal *replay.Memory, pid uint64) *Recording {
	return &Recording{source: source, journal: journal, pid: pid}
}

func (r *Recording) Monotonic(ctx context.Context) (uint64, error) {
	output, err := replay.Run(ctx, r.journal, r.pid, replay.OpClockRead, []byte{'m'}, func(ctx context.Context) ([]byte, string, error) {
		value, readErr := r.source.Monotonic(ctx)
		if readErr != nil {
			return nil, "monotonic", readErr
		}
		return encodeTime(value), "", nil
	})
	if err != nil {
		return 0, err
	}
	return decodeTime(output)
}

func (r *Recording) Wall(ctx context.Context) (int64, error) {
	output, err := replay.Run(ctx, r.journal, r.pid, replay.OpClockRead, []byte{'w'}, func(ctx context.Context) ([]byte, string, error) {
		value, readErr := r.source.Wall(ctx)
		if readErr != nil {
			return nil, "wall", readErr
		}
		return encodeTime(uint64(value)), "", nil
	})
	if err != nil {
		return 0, err
	}
	value, err := decodeTime(output)
	return int64(value), err
}

func (r *Recording) Sleep(ctx context.Context, nanoseconds uint64) error {
	request := encodeTime(nanoseconds)
	output, err := replay.Run(ctx, r.journal, r.pid, replay.OpClockSleep, request, func(ctx context.Context) ([]byte, string, error) {
		before, readErr := r.source.Monotonic(ctx)
		if readErr != nil {
			return nil, "before", readErr
		}
		if sleepErr := r.source.Sleep(ctx, nanoseconds); sleepErr != nil {
			return nil, "sleep", sleepErr
		}
		after, readErr := r.source.Monotonic(ctx)
		if readErr != nil {
			return nil, "after", readErr
		}
		return append(encodeTime(before), encodeTime(after)...), "", nil
	})
	if err != nil {
		return err
	}

	if r.journal.Recording() {
		return nil
	}
	return r.advanceToRecorded(ctx, output, request)
}

func (r *Recording) advanceToRecorded(ctx context.Context, output, request []byte) error {
	if len(output) != 16 {
		return errno.Wrap(errno.Invalid, "等待事实长度不符")
	}
	before, err := decodeTime(output[:8])
	if err != nil {
		return err
	}
	after, err := decodeTime(output[8:])
	if err != nil {
		return err
	}

	if after < before {
		return errno.Wrap(errno.Invalid, "等待事实的终点早于起点")
	}

	if requested, decodeErr := decodeTime(request); decodeErr != nil {
		return decodeErr
	} else if after-before != requested {
		return errno.Wrap(errno.Invalid, "等待事实与请求时长不符")
	}

	advancer, ok := r.source.(Advancer)
	if !ok {
		if after == before {
			return nil
		}
		return errno.Wrap(errno.NotSupported, "回放时钟不支持显式对齐")
	}
	return advancer.SetElapsedTo(after)
}

func encodeTime(value uint64) []byte {
	buffer := make([]byte, 8)
	binary.LittleEndian.PutUint64(buffer, value)
	return buffer
}

func decodeTime(data []byte) (uint64, error) {
	if len(data) != 8 {
		return 0, errno.Wrap(errno.Invalid, "时钟事实长度不符")
	}
	return binary.LittleEndian.Uint64(data), nil
}
