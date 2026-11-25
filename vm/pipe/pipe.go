package pipe

import (
	"context"
	"io"
	stdfs "io/fs"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// PipeBuf 是 POSIX 保证原子写入的上限:单次不超过该长度的写入
// 要么完整出现,要么不出现;超过该长度的写入允许交错与部分完成。
const PipeBuf = 4096

// DefaultCapacity 是匿名管道的默认容量(与 Linux 默认 16 页一致)。
const DefaultCapacity = 1 << 16

// 打开状态标志(asm-generic):访问模式与 O_NONBLOCK。
const (
	flagWriteOnly = 0x1
	flagNonBlock  = 0o4000
)

// Options 是管道端点的初始打开状态。
type Options struct {
	// NonBlock 表示非阻塞模式:空读/满写返回 EWOULDBLOCK 而非等待。
	NonBlock bool
}

// Pair 以默认选项创建一对管道端点。
func Pair(capacity int) (*Endpoint, *Endpoint, error) {
	return New(capacity, Options{})
}

// New 创建一对匿名管道端点:读端与写端共享同一缓冲。
func New(capacity int, options Options) (*Endpoint, *Endpoint, error) {
	if capacity <= 0 {
		return nil, nil, errno.Invalid
	}
	c := &core{capacity: capacity, readers: 1, writers: 1}
	readEnd := &Endpoint{c: c, read: true, refs: 1}
	writeEnd := &Endpoint{c: c, refs: 1}
	readEnd.nonblock.Store(options.NonBlock)
	writeEnd.nonblock.Store(options.NonBlock)
	return readEnd, writeEnd, nil
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
	// nonblock 是每次打开描述的状态:同一端点(经 dup 共享)一致。
	// 等待闭包在持 core 锁时读取,须无锁访问。
	nonblock atomic.Bool
}

func (e *Endpoint) acquire() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refs == 0 {
		return errno.BadHandle
	}
	return nil
}

func (e *Endpoint) blocked() bool {
	return e.nonblock.Load()
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
	duped := &Endpoint{c: c, read: e.read, refs: 1}
	duped.nonblock.Store(e.blocked())
	return duped, nil
}

func (e *Endpoint) Read(p []byte) (int, error) {
	return e.ReadContext(context.Background(), p)
}

func (e *Endpoint) ReadContext(ctx context.Context, p []byte) (int, error) {
	if err := e.acquire(); err != nil {
		return 0, err
	}
	if !e.read {
		return 0, errno.BadHandle
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
		if e.blocked() {
			return 0, errno.WouldBlock
		}
		if _, err := e.c.wake.Wait(ctx, func() (bool, wait.Reason, error) {
			e.c.mu.Lock()
			defer e.c.mu.Unlock()
			return len(e.c.buf) > 0 || e.c.writers == 0 || e.blocked(), wait.Data, nil
		}); err != nil {
			return 0, err
		}
	}
}

func (e *Endpoint) Write(p []byte) (int, error) {
	return e.WriteContext(context.Background(), p)
}

// WriteContext 写入管道。总长不超过 PipeBuf 的请求原子完成:空间不足
// 时阻塞等待(非阻塞模式返回 EWOULDBLOCK),不产生部分写入;更大的
// 请求按可用空间分块推进,允许部分完成。读端全部关闭时报 EPIPE
// (I15);SIGPIPE 的投递由系统调用层负责。
func (e *Endpoint) WriteContext(ctx context.Context, p []byte) (int, error) {
	if err := e.acquire(); err != nil {
		return 0, err
	}
	if e.read {
		return 0, errno.BadHandle
	}
	if len(p) == 0 {
		return 0, nil
	}
	// 原子性按整个请求判定,与 Linux pipe_write 一致;请求超过总容量
	// 时(Linux 容量最小一页、恒 ≥PIPE_BUF,不会出现)回退分块模式,
	// 避免永不满足的原子等待。
	atomic := len(p) <= PipeBuf && len(p) <= e.c.capacity
	written := 0
	for written < len(p) {
		remaining := len(p) - written
		e.c.mu.Lock()
		if e.c.readers == 0 {
			e.c.mu.Unlock()
			return written, errno.BrokenPipe
		}
		free := e.c.capacity - len(e.c.buf)
		if atomic {
			if free >= remaining {
				e.c.buf = append(e.c.buf, p[written:]...)
				e.c.mu.Unlock()
				e.c.notify()
				return len(p), nil
			}
		} else if free > 0 {
			n := min(free, remaining)
			e.c.buf = append(e.c.buf, p[written:written+n]...)
			written += n
			e.c.mu.Unlock()
			e.c.notify()
			continue
		}
		e.c.mu.Unlock()
		if e.blocked() {
			if written > 0 {
				return written, nil
			}
			return 0, errno.WouldBlock
		}
		if _, err := e.c.wake.Wait(ctx, func() (bool, wait.Reason, error) {
			e.c.mu.Lock()
			defer e.c.mu.Unlock()
			return len(e.c.buf) < e.c.capacity || e.c.readers == 0 || e.blocked(), wait.Data, nil
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

// StatusFlags 报告打开状态标志(F_GETFL):访问模式与 O_NONBLOCK。
func (e *Endpoint) StatusFlags() (uint32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refs == 0 {
		return 0, errno.BadHandle
	}
	var flags uint32
	if !e.read {
		flags |= flagWriteOnly
	}
	if e.nonblock.Load() {
		flags |= flagNonBlock
	}
	return flags, nil
}

// SetStatusFlags 应用 F_SETFL:只有 O_NONBLOCK 对管道有意义,其余位
// 忽略;切换后唤醒全部等待者,让它们按新模式重试(与 Linux 一致)。
func (e *Endpoint) SetStatusFlags(flags uint32) error {
	e.mu.Lock()
	if e.refs == 0 {
		e.mu.Unlock()
		return errno.BadHandle
	}
	e.nonblock.Store(flags&flagNonBlock != 0)
	e.mu.Unlock()
	e.c.notify()
	return nil
}

// Stat 返回管道端点的 fstat 视图(Linux:S_IFIFO,大小为零)。
func (e *Endpoint) Stat() (stdfs.FileInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refs == 0 {
		return nil, errno.BadHandle
	}
	return pipeFileInfo{}, nil
}

type pipeFileInfo struct{}

func (pipeFileInfo) Name() string         { return "pipe" }
func (pipeFileInfo) Size() int64          { return 0 }
func (pipeFileInfo) Mode() stdfs.FileMode { return stdfs.ModeNamedPipe | 0o600 }
func (pipeFileInfo) ModTime() time.Time   { return time.Time{} }
func (pipeFileInfo) IsDir() bool          { return false }
func (pipeFileInfo) Sys() any             { return nil }

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
