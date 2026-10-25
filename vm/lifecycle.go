package vm

import (
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// State 是 VM(内核)生命周期状态(C01):创建 → 运行 →(失败)→ 关闭。
// "启动"合并入运行态:内核构造完成即具备登记进程的能力;"停止"
// 是关闭流程中的内部阶段(Close 唤醒并回收),对外可观察状态只有
// 六个稳定值。
type State uint8

const (
	// StateCreated:已构造、尚未接受首个任务;仅允许进入运行或关闭。
	StateCreated State = iota
	// StateRunning:接受登记、执行、等待、信号与快照。
	StateRunning
	// StateStopping:关闭流程进行中(实例被中断、子任务回收);
	// 新任务被拒绝,既有等待被唤醒。
	StateStopping
	// StateFailed:执行器内 panic 或不可恢复错误后进入:一切新任务
	// 被拒绝,只允许关闭。
	StateFailed
	// StateClosed:终态;全部入口返回稳定错误,重复关闭幂等。
	StateClosed
)

func (s State) String() string {
	switch s {
	case StateCreated:
		return "created"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateFailed:
		return "failed"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// legalTransitions 是 C01 的状态机表:仅表中列出的转换合法,
// 其余一律返回稳定错误。
var legalTransitions = map[State][]State{
	StateCreated:  {StateRunning, StateClosed},
	StateRunning:  {StateStopping, StateFailed, StateClosed},
	StateStopping: {StateClosed, StateFailed},
	StateFailed:   {StateClosed},
	StateClosed:   {},
}

// transition 返回从 from 到 to 是否合法;非法转换的稳定错误由
// transitionError 统一给出。
func transition(from, to State) bool {
	for _, next := range legalTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// transitionError 是非法转换的稳定错误:同一非法转换永远返回
// 同一错误(EINVAL),错误文本携带两个状态名。
func transitionError(from, to State) error {
	return errno.Wrap(errno.Invalid, "VM 状态 "+from.String()+" 不允许转换到 "+to.String())
}

// lifecycle 持有 VM 状态并提供并发安全的转换;读侧快照无锁
// (state 是单一字,原子读写)。
type lifecycle struct {
	mu    sync.Mutex
	state State
}

func newLifecycle() lifecycle { return lifecycle{state: StateCreated} }

// get 返回当前状态快照。
func (l *lifecycle) get() State { return l.state }

// enter 尝试进入 to 状态:合法则更新并返回 nil;非法返回稳定错误。
// 已处于 to 时幂等成功(重复关闭、重复进入运行)。
func (l *lifecycle) enter(to State) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == to {
		return nil
	}
	if !transition(l.state, to) {
		return transitionError(l.state, to)
	}
	l.state = to
	return nil
}

// usable 报告当前状态是否接受新任务(登记/执行/等待):只有运行态。
func (l *lifecycle) usable() bool { return l.state == StateRunning }
