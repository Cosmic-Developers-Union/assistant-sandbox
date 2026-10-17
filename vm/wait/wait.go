package wait

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Reason uint8

const (
	Explicit Reason = iota

	Data

	Hangup

	Fault

	Signal

	Timer

	Shutdown

	Cancel
)

func (r Reason) String() string {
	switch r {
	case Explicit:
		return "explicit"
	case Data:
		return "data"
	case Hangup:
		return "hangup"
	case Fault:
		return "fault"
	case Signal:
		return "signal"
	case Timer:
		return "timer"
	case Shutdown:
		return "shutdown"
	case Cancel:
		return "cancel"
	default:
		return "unknown"
	}
}

type ticket struct{ ch chan Reason }

type Queue struct {
	mu      sync.Mutex
	closed  bool
	scan    int
	waiters []*ticket
}

func New() *Queue { return &Queue{} }

func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	for _, t := range q.waiters {
		select {
		case <-t.ch:
		default:
		}
		t.ch <- Shutdown
	}
	q.waiters = nil
}

func (q *Queue) Closed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

func (q *Queue) Waiters() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.waiters)
}

func (q *Queue) Wake(reason Reason) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.waiters)
	for i := range n {
		index := (q.scan + i) % n
		t := q.waiters[index]
		select {
		case t.ch <- reason:
			q.scan = (index + 1) % n
			return 1
		default:

		}
	}
	return 0
}

func (q *Queue) WakeAll(reason Reason) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	woken := 0
	for _, t := range q.waiters {
		select {
		case t.ch <- reason:
			woken++
		default:
		}
	}
	return woken
}

func (q *Queue) register() *ticket {
	t := &ticket{ch: make(chan Reason, 1)}
	q.waiters = append(q.waiters, t)
	return t
}

func (q *Queue) unregister(t *ticket) {
	for i, w := range q.waiters {
		if w == t {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			return
		}
	}
}

func (q *Queue) Wait(ctx context.Context, check func() (bool, Reason, error)) (Reason, error) {
	if err := ctx.Err(); err != nil {
		return Cancel, err
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return Shutdown, errno.Cancelled
	}
	t := q.register()
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.unregister(t)
		q.mu.Unlock()
	}()
	for {
		q.mu.Lock()
		done, reason, err := check()
		q.mu.Unlock()

		if done || err != nil {
			return reason, err
		}
		select {
		case wake := <-t.ch:
			if wake == Shutdown {
				return Shutdown, errno.Cancelled
			}

		case <-ctx.Done():
			return Cancel, ctx.Err()
		}
	}
}
