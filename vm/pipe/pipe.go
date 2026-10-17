package pipe

import (
	"context"
	"io"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

func Pair(capacity int) (*Endpoint, *Endpoint, error) {
	if capacity <= 0 {
		return nil, nil, errno.Invalid
	}
	c := &core{capacity: capacity, readers: 1, writers: 1}
	return &Endpoint{c: c, read: true, refs: 1}, &Endpoint{c: c, refs: 1}, nil
}

type core struct {
	mu       sync.Mutex
	wake     wait.Queue
	buf      []byte
	capacity int
	readers  int
	writers  int
	hooks    []func()
	watchers []*watchFunc
}

type watchFunc struct{ fn func() }

func (c *core) notify() {
	c.wake.WakeAll(wait.Data)
	c.mu.Lock()
	watchers := make([]*watchFunc, len(c.watchers))
	copy(watchers, c.watchers)
	c.mu.Unlock()
	for _, w := range watchers {
		w.fn()
	}
}

func (c *core) fireHooks() {
	c.mu.Lock()
	hooks := c.hooks
	c.hooks = nil
	c.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

type Endpoint struct {
	mu   sync.Mutex
	c    *core
	read bool
	refs int
}

func (e *Endpoint) acquire() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refs == 0 {
		return errno.BadHandle
	}
	return nil
}

func (e *Endpoint) Dup() (*Endpoint, error) {
	e.mu.Lock()
	if e.refs == 0 {
		e.mu.Unlock()
		return nil, errno.BadHandle
	}
	e.mu.Unlock()
	c := e.c
	c.mu.Lock()
	if e.read {
		c.readers++
	} else {
		c.writers++
	}
	c.mu.Unlock()
	return &Endpoint{c: c, read: e.read, refs: 1}, nil
}

func (e *Endpoint) Read(p []byte) (int, error) {
	return e.ReadContext(context.Background(), p)
}

func (e *Endpoint) ReadContext(ctx context.Context, p []byte) (int, error) {
	if err := e.acquire(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	for {
		e.c.mu.Lock()
		if len(e.c.buf) > 0 {
			n := copy(p, e.c.buf)
			e.c.buf = e.c.buf[n:]
			e.c.mu.Unlock()
			e.c.notify()
			return n, nil
		}
		if e.c.writers == 0 {
			e.c.mu.Unlock()
			return 0, io.EOF
		}
		e.c.mu.Unlock()
		if _, err := e.c.wake.Wait(ctx, func() (bool, wait.Reason, error) {
			e.c.mu.Lock()
			defer e.c.mu.Unlock()
			return len(e.c.buf) > 0 || e.c.writers == 0, wait.Data, nil
		}); err != nil {
			return 0, err
		}
	}
}

func (e *Endpoint) Write(p []byte) (int, error) {
	return e.WriteContext(context.Background(), p)
}

func (e *Endpoint) WriteContext(ctx context.Context, p []byte) (int, error) {
	if err := e.acquire(); err != nil {
		return 0, err
	}
	written := 0
	for written < len(p) {
		e.c.mu.Lock()
		if e.c.readers == 0 {
			e.c.mu.Unlock()
			return written, errno.BrokenPipe
		}
		if free := e.c.capacity - len(e.c.buf); free > 0 {
			n := min(free, len(p)-written)
			e.c.buf = append(e.c.buf, p[written:written+n]...)
			written += n
			e.c.mu.Unlock()
			e.c.notify()
			continue
		}
		e.c.mu.Unlock()
		if _, err := e.c.wake.Wait(ctx, func() (bool, wait.Reason, error) {
			e.c.mu.Lock()
			defer e.c.mu.Unlock()
			return len(e.c.buf) < e.c.capacity || e.c.readers == 0, wait.Data, nil
		}); err != nil {
			return written, err
		}
	}
	return written, nil
}

func (e *Endpoint) Close() error {
	e.mu.Lock()
	if e.refs == 0 {
		e.mu.Unlock()
		return errno.BadHandle
	}
	e.refs--
	last := e.refs == 0
	e.mu.Unlock()
	if !last {

		return nil
	}
	c := e.c
	c.mu.Lock()
	if e.read {
		c.readers--
	} else {
		c.writers--
	}
	broke := e.read && c.readers == 0
	c.mu.Unlock()
	c.notify()
	c.wake.WakeAll(wait.Hangup)
	if broke {
		c.fireHooks()
	}
	return nil
}

func (e *Endpoint) OnBroken(hook func()) {
	c := e.c
	c.mu.Lock()
	if hook == nil {
		c.hooks = nil
	} else {
		c.hooks = []func(){hook}
	}
	c.mu.Unlock()
}

func (e *Endpoint) Buffered() int {
	e.c.mu.Lock()
	defer e.c.mu.Unlock()
	return len(e.c.buf)
}

func (e *Endpoint) Ready() poll.Readiness {
	c := e.c
	c.mu.Lock()
	defer c.mu.Unlock()
	var bits poll.Readiness
	if e.read {
		if len(c.buf) > 0 {
			bits |= poll.Readable
		}
		if c.writers == 0 {
			bits |= poll.Readable | poll.Hangup
		}
	} else {
		if len(c.buf) < c.capacity {
			bits |= poll.Writable
		}
		if c.readers == 0 {
			bits |= poll.Writable | poll.Hangup
		}
	}
	return bits
}

func (e *Endpoint) OnChange(fn func()) (cancel func()) {
	c := e.c
	w := &watchFunc{fn: fn}
	c.mu.Lock()
	c.watchers = append(c.watchers, w)
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, registered := range c.watchers {
			if registered == w {
				c.watchers = append(c.watchers[:i], c.watchers[i+1:]...)
				return
			}
		}
	}
}
