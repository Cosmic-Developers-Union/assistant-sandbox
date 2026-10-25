package clock

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

type timer struct {
	deadline uint64
	seq      uint64
}

const maxNanoseconds = uint64(1)<<63 - 1

type Virtual struct {
	mu      sync.Mutex
	elapsed uint64
	// wallOffset 是实时时钟相对单调时钟的偏移:系统时间调整只改
	// 偏移,单调时钟与相对等待期限不受影响(L01)。
	wallOffset int64
	seq        uint64
	closed     bool
	timers     []timer
	wake       wait.Queue
}

// Wall 返回实时时钟读数:等于单调时钟加系统时间偏移。系统时间被
// 调整时只有偏移变化,单调时钟不受影响。
func (v *Virtual) Wall(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return int64(v.elapsed) + v.wallOffset, nil
}

// SetWallTo 调整系统时间(实时时钟)到指定纳秒读数:只改实时钟偏移,
// 单调时钟与相对等待期限保持不变(L01)。
func (v *Virtual) SetWallTo(nanoseconds int64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return errno.Cancelled
	}
	v.wallOffset = nanoseconds - int64(v.elapsed)
	return nil
}

func (v *Virtual) Monotonic(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.elapsed, nil
}

func (v *Virtual) Close() {
	v.mu.Lock()
	v.closed = true
	v.mu.Unlock()
	v.wake.Close()
}

func (v *Virtual) Sleep(ctx context.Context, nanoseconds uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return errno.Cancelled
	}
	deadline := v.elapsed + nanoseconds
	if deadline < v.elapsed || deadline > maxNanoseconds {
		v.mu.Unlock()
		return errno.Overflow
	}
	if nanoseconds == 0 {
		v.mu.Unlock()
		return nil
	}
	v.seq++
	self := timer{deadline: deadline, seq: v.seq}
	v.timers = append(v.timers, self)
	v.mu.Unlock()
	defer v.remove(self)
	for {
		advanced := false
		v.mu.Lock()
		if v.closed {
			v.mu.Unlock()
			return errno.Cancelled
		}
		switch {
		case v.elapsed >= deadline && !v.pendingEarlierSatisfied(deadline, self.seq):
			v.mu.Unlock()
			return nil
		case v.elapsed < deadline && v.earliest() == self.seq:

			v.elapsed = deadline
			advanced = true
		}
		v.mu.Unlock()
		if advanced {

			v.wake.WakeAll(wait.Timer)
			continue
		}
		if _, err := v.wake.Wait(ctx, func() (bool, wait.Reason, error) {
			v.mu.Lock()
			defer v.mu.Unlock()
			satisfied := v.elapsed >= deadline && !v.pendingEarlierSatisfied(deadline, self.seq)
			return satisfied || (v.elapsed < deadline && v.earliest() == self.seq), wait.Timer, nil
		}); err != nil {
			return err
		}
	}
}

func (v *Virtual) remove(t timer) {
	removed := false
	v.mu.Lock()
	for i, registered := range v.timers {
		if registered.seq == t.seq {
			v.timers = append(v.timers[:i], v.timers[i+1:]...)
			removed = true
			break
		}
	}
	v.mu.Unlock()
	if removed {
		v.wake.WakeAll(wait.Timer)
	}
}

func (v *Virtual) pendingEarlierSatisfied(deadline, seq uint64) bool {
	for _, t := range v.timers {
		if t.seq < seq && t.deadline <= deadline {
			return true
		}
	}
	return false
}

func (v *Virtual) earliest() uint64 {
	var best timer
	for i, t := range v.timers {
		if i == 0 || t.deadline < best.deadline || (t.deadline == best.deadline && t.seq < best.seq) {
			best = t
		}
	}
	return best.seq
}

func (v *Virtual) SetElapsedTo(nanoseconds uint64) error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return errno.Cancelled
	}
	if nanoseconds > maxNanoseconds {
		v.mu.Unlock()
		return errno.Overflow
	}
	v.elapsed = nanoseconds
	v.mu.Unlock()
	v.wake.WakeAll(wait.Timer)
	return nil
}

func (v *Virtual) Advance(nanoseconds uint64) error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return errno.Cancelled
	}
	target := v.elapsed + nanoseconds
	if target < v.elapsed || target > maxNanoseconds {
		v.mu.Unlock()
		return errno.Overflow
	}
	v.elapsed = target
	v.mu.Unlock()
	v.wake.WakeAll(wait.Timer)
	return nil
}
