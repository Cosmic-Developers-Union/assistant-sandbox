package wasm

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

type Config struct {
	Name string

	Entry string

	Args []string

	Env []string

	Dir string

	Stdin []byte

	Stdout, Stderr Writer

	MaxMemoryPages uint32

	MaxOutputBytes int64

	Hosts *Registry

	Bindings []HostBinding

	Cancel <-chan struct{}
}

type Writer interface {
	Write([]byte) (int, error)
}

type Instance interface {
	Wait(ctx context.Context) (Result, error)

	Interrupt(n signal.Number) error

	Close() error
}

type Result struct {
	Code uint8

	Signal signal.Number

	Reason Reason
}

type Reason uint8

const (
	Exited Reason = iota

	Interrupted

	Fault

	Quota
)

func (r Reason) String() string {
	switch r {
	case Exited:
		return "exited"
	case Interrupted:
		return "interrupted"
	case Fault:
		return "fault"
	case Quota:
		return "quota"
	default:
		return "unknown"
	}
}

type Engine interface {
	Instantiate(ctx context.Context, module *Module, moduleBytes []byte, config Config) (Instance, error)

	Close() error
}

var ErrNoEngine = errno.Wrap(errno.NotSupported, "当前部署没有可用的 WASM 执行引擎")

type hostModule interface {
	Close(context.Context) error
}

type runState struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	result      Result
	waitErr     error
	closed      bool
	finished    bool
	module      hostModule
	interrupted bool

	pending bool

	onInterrupt func()
}

func newRunState() *runState {
	return &runState{done: make(chan struct{})}
}

func (s *runState) finish(result Result, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.finished = true
	s.result = result
	s.waitErr = err
	close(s.done)
}

func (s *runState) outcome() (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, s.waitErr
}

func (s *runState) wait(ctx context.Context) (Result, error) {
	select {
	case <-s.done:
		return s.outcome()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (s *runState) setInterruptHook(action func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onInterrupt = action
}

func (s *runState) interrupt() error {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return errno.Wrap(errno.Busy, "实例已结束，无需中断")
	}
	action := s.onInterrupt
	module := s.module
	cancel := s.cancel
	if module == nil {

		s.pending = true
	}
	s.interrupted = true
	s.mu.Unlock()
	if module != nil && action != nil {
		action()
	}
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *runState) attachModule(module hostModule) {
	s.mu.Lock()
	if module != nil {
		s.module = module
	}
	pending := s.pending
	action := s.onInterrupt
	s.pending = false
	s.mu.Unlock()
	if pending && action != nil {
		action()
	}
}

func (s *runState) releaseModule() error {
	s.mu.Lock()
	module := s.module
	s.mu.Unlock()
	if module == nil {
		return nil
	}
	return module.Close(context.Background())
}

func (s *runState) close() (cancel context.CancelFunc, first bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	s.closed = true
	return s.cancel, true
}
