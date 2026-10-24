package eventfd

import (
	"context"
	"encoding/binary"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// ValueUpperLimit 与 Linux 一致:计数达到 2^64-2 后写入阻塞,为 0 保留"空"语义。
const ValueUpperLimit = ^uint64(1)

type Options struct {
	Semaphore bool
	NonBlock  bool
}

// EventFD 是 eventfd(2) 的内核对象:8 字节计数器,读清零(或取 1),
// 计满后写入阻塞。Read/Write 阻塞路径经 Close 唤醒。
type EventFD struct {
	mu        sync.Mutex
	wake      wait.Queue
	counter   uint64
	semaphore bool
	nonblock  bool
	closed    bool
	watchers  []*watchFunc
}

type watchFunc struct{ fn func() }

func New(initialValue uint64, options Options) (*EventFD, error) {
	if initialValue > ValueUpperLimit {
		return nil, errno.Invalid
	}
	return &EventFD{
		counter:   initialValue,
		semaphore: options.Semaphore,
		nonblock:  options.NonBlock,
	}, nil
}

func (e *EventFD) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) < 8 {
		return 0, errno.Invalid
	}
	for {
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return 0, errno.Interrupted
		}
		if e.counter > 0 {
			var value uint64
			if e.semaphore {
				value = 1
				e.counter--
			} else {
				value = e.counter
				e.counter = 0
			}
			e.mu.Unlock()
			e.notify()
			binary.LittleEndian.PutUint64(p[:8], value)
			return 8, nil
		}
		if e.nonblock {
			e.mu.Unlock()
			return 0, errno.WouldBlock
		}
		e.mu.Unlock()
		if _, err := e.wake.Wait(context.Background(), func() (bool, wait.Reason, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.counter > 0 || e.closed, wait.Data, nil
		}); err != nil {
			return 0, errno.Interrupted
		}
	}
}

func (e *EventFD) Write(p []byte) (int, error) {
	if len(p) != 8 {
		return 0, errno.Invalid
	}
	value := binary.LittleEndian.Uint64(p)
	if value > ValueUpperLimit {
		return 0, errno.Invalid
	}
	for {
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return 0, errno.Interrupted
		}
		if e.counter > ValueUpperLimit-value {
			e.mu.Unlock()
			if e.nonblock {
				return 0, errno.WouldBlock
			}
			if _, err := e.wake.Wait(context.Background(), func() (bool, wait.Reason, error) {
				e.mu.Lock()
				defer e.mu.Unlock()
				return e.counter <= ValueUpperLimit-value || e.closed, wait.Data, nil
			}); err != nil {
				return 0, errno.Interrupted
			}
			continue
		}
		e.counter += value
		e.mu.Unlock()
		e.notify()
		return 8, nil
	}
}

func (e *EventFD) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errno.BadHandle
	}
	e.closed = true
	e.mu.Unlock()
	e.wake.Close()
	e.notify()
	return nil
}

func (e *EventFD) Ready() poll.Readiness {
	e.mu.Lock()
	defer e.mu.Unlock()
	var bits poll.Readiness
	if e.closed {
		return bits | poll.Hangup
	}
	if e.counter > 0 {
		bits |= poll.Readable
	}
	if e.counter < ValueUpperLimit {
		bits |= poll.Writable
	}
	return bits
}

func (e *EventFD) OnChange(fn func()) (cancel func()) {
	w := &watchFunc{fn: fn}
	e.mu.Lock()
	e.watchers = append(e.watchers, w)
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		for index, registered := range e.watchers {
			if registered == w {
				e.watchers = append(e.watchers[:index], e.watchers[index+1:]...)
				return
			}
		}
	}
}

func (e *EventFD) notify() {
	e.wake.WakeAll(wait.Data)
	e.mu.Lock()
	watchers := make([]*watchFunc, len(e.watchers))
	copy(watchers, e.watchers)
	e.mu.Unlock()
	for _, w := range watchers {
		w.fn()
	}
}
