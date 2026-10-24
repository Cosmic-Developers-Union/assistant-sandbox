package executable

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

type Reason uint8

const (
	Exited Reason = iota
	Signaled
	Fault
	Quota
)

func (r Reason) String() string {
	switch r {
	case Exited:
		return "exited"
	case Signaled:
		return "signaled"
	case Fault:
		return "fault"
	case Quota:
		return "quota"
	default:
		return "unknown"
	}
}

type Result struct {
	Code   uint8
	Signal signal.Number
	Reason Reason
}

type Instance interface {
	Wait(ctx context.Context) (Result, error)

	Interrupt(n signal.Number) error

	Close() error
}

type Executor interface {
	Start(ctx context.Context, program Program, p Proc) (Instance, error)
}

type Proc = proc.Proc

type interrupt struct{ n signal.Number }

func (i interrupt) Error() string { return fmt.Sprintf("信号 %v 终止", i.n) }

// Death 报告客体进程按默认处置被信号终止(典型来源:同步故障,
// 如非法指令→SIGILL、无效访问→SIGSEGV)。执行器据此把退出归类为
// Signaled,父进程经 wait 观察到信号终止而非普通退出。
type Death struct {
	Signal signal.Number

	// Detail 携带诊断上下文(故障类别与地址),不进入 wait 状态。
	Detail string
}

func (d Death) Error() string {
	if d.Detail == "" {
		return fmt.Sprintf("信号 %v 终止", d.Signal)
	}
	return fmt.Sprintf("信号 %v 终止: %s", d.Signal, d.Detail)
}

type Builtin struct{}

func NewBuiltin() *Builtin { return &Builtin{} }

func (Builtin) Start(ctx context.Context, program Program, p Proc) (Instance, error) {
	if program == nil {
		return nil, errno.Invalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	inst := &builtinInstance{
		done:   make(chan struct{}),
		cancel: cancel,
	}
	go func() {
		defer close(inst.done)
		var (
			code   uint8
			runErr error
		)
		func() {
			defer func() {
				if fault := recover(); fault != nil {

					runErr = fmt.Errorf("客体程序 %s 发生故障: %v\n%s", p.Program, fault, debug.Stack())
					inst.settle(Result{Code: 125, Reason: Fault}, runErr)
					return
				}
			}()
			code, runErr = program.Run(runCtx, p)
		}()
		result := Result{Code: code, Reason: Exited}
		if cause := context.Cause(runCtx); cause != nil {
			var sig interrupt
			if errors.As(cause, &sig) {
				result = Result{Code: signal.ExitCode(sig.n), Signal: sig.n, Reason: Signaled}
				runErr = nil
			}
		}
		if runErr != nil && result.Reason == Exited {
			var death Death
			if errors.As(runErr, &death) && death.Signal.Valid() {
				result = Result{Code: signal.ExitCode(death.Signal), Signal: death.Signal, Reason: Signaled}
				runErr = nil
			}
		}
		if runErr != nil && result.Reason == Exited && result.Code == 0 {
			result.Code = 125
		}
		inst.settle(result, runErr)
	}()
	return inst, nil
}

type builtinInstance struct {
	done   chan struct{}
	cancel context.CancelCauseFunc

	mu      sync.Mutex
	result  Result
	err     error
	settled bool
}

func (i *builtinInstance) settle(result Result, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.settled {
		return
	}
	i.settled = true
	i.result = result
	i.err = err
}

func (i *builtinInstance) Wait(ctx context.Context) (Result, error) {
	select {
	case <-i.done:
		i.mu.Lock()
		defer i.mu.Unlock()
		return i.result, i.err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (i *builtinInstance) Interrupt(n signal.Number) error {
	if !n.Valid() {
		return errno.Invalid
	}
	select {
	case <-i.done:
		return errno.NoProcess
	default:
	}
	i.cancel(interrupt{n: n})
	return nil
}

func (i *builtinInstance) Close() error {
	select {
	case <-i.done:
		i.mu.Lock()
		defer i.mu.Unlock()
		i.settled = true
		return nil
	default:
		return errno.Busy
	}
}
