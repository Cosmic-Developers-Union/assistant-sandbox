package executable

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"

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

	// Diagnostic 携带致命故障时的 CPU 状态(T02);正常退出或配额
	// 终止时为 nil。报告层可据此生成可复现崩溃快照。
	Diagnostic *Diagnostic
}

// Diagnostic 是致命故障发生时的客体 CPU 快照(T02)。字段均为值
// 类型,不引用内核或宿主内存。
type Diagnostic struct {
	PC        uint64
	Registers []uint64
	Floats    []uint64
	FCSR      uint32
	Trap      string
}

// DiagnosticError 让执行器在返回错误的同时携带 CPU 快照;错误链
// 仍可被 errors.As 识别为原始故障/信号类型。
type DiagnosticError struct {
	Err        error
	Diagnostic Diagnostic
}

func (e *DiagnosticError) Error() string { return e.Err.Error() }

func (e *DiagnosticError) Unwrap() error { return e.Err }

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

	// Diagnostic 是致命信号发生时的客体 CPU 快照(T02)。
	Diagnostic *Diagnostic
}

func (d Death) Error() string {
	if d.Detail == "" {
		return fmt.Sprintf("信号 %v 终止", d.Signal)
	}
	return fmt.Sprintf("信号 %v 终止: %s", d.Signal, d.Detail)
}

type Builtin struct{}

func NewBuiltin() *Builtin { return &Builtin{} }

// pauseGate 驻停程序执行:程序上下文的 Err() 在驻停期间阻塞,
// 解释器在指令边界的 ctx.Err() 检查处暂停推进;恢复或父上下文
// 结束(如关闭强制终止)后放行。无需程序主动配合。
type pauseGate struct {
	mu        sync.Mutex
	cond      *sync.Cond
	parent    context.Context
	parked    atomic.Bool
	parentErr atomic.Pointer[error]
	cancelled chan struct{}

	// observer 观察驻停状态变化(H02):进入驻停前与恢复后各回调
	// 一次,供内核归还/重新排队执行权;回调在锁外执行。
	observer func(parked bool)
}

func newPauseGate(parent context.Context, done <-chan struct{}) *pauseGate {
	gate := &pauseGate{
		parent:    parent,
		cancelled: make(chan struct{}),
	}
	gate.cond = sync.NewCond(&gate.mu)
	// 父上下文取消(关闭/强制终止)或实例结束时唤醒所有驻停的 Err() 调用。
	go func() {
		select {
		case <-parent.Done():
			err := parent.Err()
			gate.parentErr.Store(&err)
			close(gate.cancelled)
		case <-done:
		}
		gate.cond.Broadcast()
	}()
	return gate
}

func (g *pauseGate) park() {
	g.mu.Lock()
	g.parked.Store(true)
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *pauseGate) resume() {
	g.mu.Lock()
	g.parked.Store(false)
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *pauseGate) Err() error {
	if err := g.parentErr.Load(); err != nil {
		return *err
	}
	if !g.parked.Load() {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	observed := false
	notify := func(parked bool) {
		if g.observer == nil {
			return
		}
		observer := g.observer
		g.mu.Unlock()
		observer(parked)
		g.mu.Lock()
	}
	defer func() {
		// 只有通知过驻停(true)的路径才在返回前通知恢复(false),
		// 未驻停的常规检查不触发观察者。
		if observed {
			notify(false)
		}
	}()
	for g.parked.Load() {
		// 父上下文取消(KILL/关闭等强制终止)优先于驻停:
		// 唤醒等待者放行,程序经 ctx.Err() 观察取消。
		if err := g.parent.Err(); err != nil {
			return err
		}
		if !observed {
			observed = true
			notify(true)
		}
		g.cond.Wait()
	}
	return g.parent.Err()
}

// observeParking 注册驻停观察函数;nil 清除。
func (g *pauseGate) observeParking(fn func(parked bool)) {
	g.mu.Lock()
	g.observer = fn
	g.mu.Unlock()
}

// ObserveParking 注册驻停观察函数(H02):进入驻停前以 true 回调,
// 恢复或因取消离开驻停时以 false 回调;供内核在驻停期间归还执行权。
func (i *builtinInstance) ObserveParking(fn func(parked bool)) {
	i.gate.observeParking(fn)
}

// gateContext 把 Err() 的驻停语义接入程序上下文;其余委托父上下文。
type gateContext struct {
	context.Context

	gate *pauseGate
}

func (c gateContext) Err() error { return c.gate.Err() }

func (c gateContext) Done() <-chan struct{} { return c.gate.cancelled }

var _ context.Context = gateContext{}

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
	gate := newPauseGate(runCtx, inst.done)
	inst.gate = gate
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
			code, runErr = program.Run(gateContext{Context: runCtx, gate: gate}, p)
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
				result = Result{Code: signal.ExitCode(death.Signal), Signal: death.Signal, Reason: Signaled, Diagnostic: death.Diagnostic}
				runErr = nil
			}
		}
		if runErr != nil {
			var diagnostic *DiagnosticError
			if errors.As(runErr, &diagnostic) {
				result.Diagnostic = &diagnostic.Diagnostic
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
	gate   *pauseGate

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

// stopFamily 报告信号是否属于停止族(SIGSTOP/TSTP/TTIN/TTOU)。
func stopFamily(n signal.Number) bool {
	switch n {
	case signal.STOP, signal.TSTP, signal.TTIN, signal.TTOU:
		return true
	}
	return false
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
	// 停止族驻停执行,CONT 恢复;其余信号按被信号终止取消。
	switch {
	case stopFamily(n):
		i.gate.park()
		return nil
	case n == signal.CONT:
		i.gate.resume()
		return nil
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
