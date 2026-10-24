package signal

import (
	"fmt"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Number uint8

const (
	HUP    Number = 1
	INT    Number = 2
	QUIT   Number = 3
	ILL    Number = 4
	TRAP   Number = 5
	ABRT   Number = 6
	BUS    Number = 7
	FPE    Number = 8
	KILL   Number = 9
	USR1   Number = 10
	SEGV   Number = 11
	USR2   Number = 12
	PIPE   Number = 13
	ALRM   Number = 14
	TERM   Number = 15
	STKFLT Number = 16
	CHLD   Number = 17
	CONT   Number = 18
	STOP   Number = 19
	TSTP   Number = 20
	TTIN   Number = 21
	TTOU   Number = 22
	URG    Number = 23
	XCPU   Number = 24
	XFSZ   Number = 25
	VTALRM Number = 26
	PROF   Number = 27
	WINCH  Number = 28
	IO     Number = 29
	PWR    Number = 30
	SYS    Number = 31
)

const Max = Number(31)

type Action uint8

const (
	Terminate Action = iota
	Core
	Ignore
	Stop
	Continue
)

func (a Action) String() string {
	switch a {
	case Terminate:
		return "terminate"
	case Core:
		return "core"
	case Ignore:
		return "ignore"
	case Stop:
		return "stop"
	case Continue:
		return "continue"
	default:
		return "unknown"
	}
}

var names = map[Number]string{
	HUP: "SIGHUP", INT: "SIGINT", QUIT: "SIGQUIT", ILL: "SIGILL", TRAP: "SIGTRAP",
	ABRT: "SIGABRT", BUS: "SIGBUS", FPE: "SIGFPE", KILL: "SIGKILL", USR1: "SIGUSR1",
	SEGV: "SIGSEGV", USR2: "SIGUSR2", PIPE: "SIGPIPE", ALRM: "SIGALRM", TERM: "SIGTERM",
	STKFLT: "SIGSTKFLT", CHLD: "SIGCHLD", CONT: "SIGCONT", STOP: "SIGSTOP", TSTP: "SIGTSTP",
	TTIN: "SIGTTIN", TTOU: "SIGTTOU", URG: "SIGURG", XCPU: "SIGXCPU", XFSZ: "SIGXFSZ",
	VTALRM: "SIGVTALRM", PROF: "SIGPROF", WINCH: "SIGWINCH", IO: "SIGIO", PWR: "SIGPWR", SYS: "SIGSYS",
}

func (n Number) Name() string { return names[n] }

func (n Number) Valid() bool { return n >= 1 && n <= Max }

func ByName(name string) (Number, error) {
	for n, known := range names {
		if known == name {
			return n, nil
		}
	}
	return 0, errno.Invalid
}

func Default(n Number) (Action, error) {
	if !n.Valid() {
		return 0, errno.Invalid
	}
	switch n {
	case QUIT, ILL, TRAP, ABRT, BUS, FPE, SEGV, SYS:
		return Core, nil
	case CHLD, URG, WINCH, CONT:
		return Ignore, nil
	case STOP, TSTP, TTIN, TTOU:
		return Stop, nil
	default:
		return Terminate, nil
	}
}

func Unmaskable(n Number) bool { return n == KILL || n == STOP }

// ActionConfig 是 rt_sigaction 登记的单个信号处置。
// Handler 为 SigDefault(0) 时按默认行为处置,SigIgnore(1) 时丢弃,
// 其余值是客户态处理器地址(处理器执行随 J03/J04 信号帧落地)。
type ActionConfig struct {
	Handler  uint64
	Flags    uint32
	Restorer uint64
	Mask     uint64
}

const (
	SigDefault uint64 = 0
	SigIgnore  uint64 = 1
)

// MaskHow 是 rt_sigprocmask 的 how 参数(SIG_BLOCK/SIG_UNBLOCK/SIG_SETMASK)。
type MaskHow uint8

const (
	MaskBlock MaskHow = iota + 1
	MaskUnblock
	MaskSetMask
)

// ApplyMask 以位图批量调整屏蔽集,返回调整前的屏蔽集;
// 位 0 与超出信号范围的位报 EINVAL(不伪成功)。
func (s *State) ApplyMask(how MaskHow, set uint64) (uint64, error) {
	if set&^maskRangeBits() != 0 {
		return 0, errno.Invalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	blocked := uint64(s.blocked)
	old := blocked
	switch how {
	case MaskBlock:
		blocked |= set
	case MaskUnblock:
		blocked &^= set
	case MaskSetMask:
		blocked = set
	default:
		return 0, errno.Invalid
	}
	s.blocked = uint32(blocked)
	return old, nil
}

// maskRangeBits 是客体信号(1..Max)在 64 位 sigset 中的合法位。
func maskRangeBits() uint64 {
	var bits uint64
	for n := Number(1); n <= Max; n++ {
		bits |= 1 << (n - 1)
	}
	return bits
}

// Mask 返回当前屏蔽位图。
func (s *State) Mask() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return uint64(s.blocked)
}

// DeliverableHandler 返回待投递且登记了客户态处理器的信号;
// 返回 false 表示当前没有需要经信号帧投递的信号。
func (s *State) DeliverableHandler() (Number, ActionConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	number, ok := s.deliverable()
	if !ok {
		return 0, ActionConfig{}, false
	}
	config := s.actions[number]
	if config.Handler <= SigIgnore {
		return 0, ActionConfig{}, false
	}
	return number, config, true
}

// BlockForHandler 在投递时并入处置掩码;nodefer 为 false 时同时
// 屏蔽本信号(SA_NODEFER 语义),返回投递前的屏蔽位图。
func (s *State) BlockForHandler(n Number, handlerMask uint64, nodefer bool) (uint64, error) {
	if !n.Valid() {
		return 0, errno.Invalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := uint64(s.blocked)
	s.blocked |= uint32(handlerMask & maskRangeBits())
	if !nodefer {
		s.blocked |= mask(n)
	}
	return old, nil
}

// RestoreHandlerMask 在 rt_sigreturn 时整体恢复屏蔽集。
func (s *State) RestoreHandlerMask(mask uint64) error {
	if mask&^maskRangeBits() != 0 {
		return errno.Invalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocked = uint32(mask)
	return nil
}

// SetAltStack 登记/清除备用信号栈。
func (s *State) SetAltStack(stack AltStack) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.altStack = stack
}

// AltStack 返回登记的备用信号栈。
func (s *State) AltStack() AltStack {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.altStack
}

// AltStack 是 sigaltstack 登记的备用信号栈。
type AltStack struct {
	Sp      uint64
	Size    uint64
	Present bool
}

type State struct {
	mu       sync.Mutex
	pending  uint32
	blocked  uint32
	ignored  uint32
	actions  [Max + 1]ActionConfig
	altStack AltStack
}

// SetAction 登记 rt_sigaction 处置;KILL/STOP 不可改,EINVAL。
func (s *State) SetAction(n Number, config ActionConfig) error {
	if !n.Valid() {
		return errno.Invalid
	}
	if Unmaskable(n) {
		return errno.Invalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions[n] = config
	return nil
}

// Action 返回登记的处置;未登记为零值(即 SIG_DFL)。
func (s *State) Action(n Number) (ActionConfig, error) {
	if !n.Valid() {
		return ActionConfig{}, errno.Invalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actions[n], nil
}

func mask(n Number) uint32 { return 1 << (n - 1) }

func (s *State) Raise(n Number) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !n.Valid() {
		return false
	}
	if s.pending&mask(n) != 0 {
		return false
	}
	s.pending |= mask(n)
	return true
}

func (s *State) Pending() []Number {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Number
	for n := Number(1); n <= Max; n++ {
		if s.pending&mask(n) != 0 {
			out = append(out, n)
		}
	}
	return out
}

func (s *State) SetMasked(n Number, masked bool) error {
	if !n.Valid() {
		return errno.Invalid
	}
	if Unmaskable(n) {
		return errno.NotSupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if masked {
		s.blocked |= mask(n)
	} else {
		s.blocked &^= mask(n)
	}
	return nil
}

func (s *State) SetIgnored(n Number, ignored bool) error {
	if !n.Valid() {
		return errno.Invalid
	}
	if Unmaskable(n) {
		return errno.NotSupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ignored {
		s.ignored |= mask(n)
	} else {
		s.ignored &^= mask(n)
	}
	return nil
}

func (s *State) deliverable() (Number, bool) {
	for n := Number(1); n <= Max; n++ {
		if s.pending&mask(n) == 0 {
			continue
		}
		if s.ignored&mask(n) != 0 {

			s.pending &^= mask(n)
			continue
		}
		if s.blocked&mask(n) != 0 {
			continue
		}
		return n, true
	}
	return 0, false
}

func (s *State) consume(n Number) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending &^= mask(n)
}

type Table struct {
	mu         sync.Mutex
	states     map[uint64]*State
	interrupts map[uint64]func(Number)
}

func New() *Table {
	return &Table{states: map[uint64]*State{}, interrupts: map[uint64]func(Number){}}
}

func (t *Table) Spawn(pid uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.states[pid] = &State{}
}

func (t *Table) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.states = map[uint64]*State{}
	t.interrupts = map[uint64]func(Number){}
}

func (t *Table) Discard(pid uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.states, pid)
	delete(t.interrupts, pid)
}

func (t *Table) ResetForExec(pid uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.states[pid]
	if !ok {
		return errno.NoProcess
	}
	state.mu.Lock()
	state.pending = 0
	state.actions = [Max + 1]ActionConfig{}
	state.mu.Unlock()
	delete(t.interrupts, pid)
	return nil
}

// SetAction 登记 pid 的 rt_sigaction 处置。
func (t *Table) SetAction(pid uint64, n Number, config ActionConfig) error {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return errno.NoProcess
	}
	return state.SetAction(n, config)
}

// Action 返回 pid 已登记的处置;未登记为零值(SIG_DFL)。
func (t *Table) Action(pid uint64, n Number) (ActionConfig, error) {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return ActionConfig{}, errno.NoProcess
	}
	return state.Action(n)
}

// ApplyMask 以 rt_sigprocmask 语义批量调整 pid 的屏蔽集。
func (t *Table) ApplyMask(pid uint64, how MaskHow, set uint64) (uint64, error) {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return 0, errno.NoProcess
	}
	return state.ApplyMask(how, set)
}

// DeliverableHandler 返回 pid 待投递且登记了客户态处理器的信号。
func (t *Table) DeliverableHandler(pid uint64) (Number, ActionConfig, bool) {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return 0, ActionConfig{}, false
	}
	return state.DeliverableHandler()
}

// BlockForHandler 在 pid 投递信号时并入处置掩码并返回旧屏蔽集。
func (t *Table) BlockForHandler(pid uint64, n Number, handlerMask uint64, nodefer bool) (uint64, error) {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return 0, errno.NoProcess
	}
	return state.BlockForHandler(n, handlerMask, nodefer)
}

// RestoreHandlerMask 在 pid 的 rt_sigreturn 时恢复屏蔽集。
func (t *Table) RestoreHandlerMask(pid uint64, mask uint64) error {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return errno.NoProcess
	}
	return state.RestoreHandlerMask(mask)
}

// SetAltStack 登记 pid 的备用信号栈。
func (t *Table) SetAltStack(pid uint64, stack AltStack) {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return
	}
	state.SetAltStack(stack)
}

// AltStack 返回 pid 登记的备用信号栈。
func (t *Table) AltStack(pid uint64) AltStack {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return AltStack{}
	}
	return state.AltStack()
}

// Mask 返回 pid 当前屏蔽位图。
func (t *Table) Mask(pid uint64) (uint64, error) {
	t.mu.Lock()
	state := t.states[pid]
	t.mu.Unlock()
	if state == nil {
		return 0, errno.NoProcess
	}
	return state.Mask(), nil
}

func (t *Table) Ignored(pid uint64, n Number) bool {
	s := t.state(pid)
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ignored&mask(n) != 0
}

func (t *Table) state(pid uint64) *State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.states[pid]
}

func (t *Table) Raise(pid uint64, n Number) error {
	s := t.state(pid)
	if s == nil {
		return errno.NoProcess
	}
	if !n.Valid() {
		return errno.Invalid
	}
	s.Raise(n)
	return nil
}

func (t *Table) Pending(pid uint64) ([]Number, error) {
	s := t.state(pid)
	if s == nil {
		return nil, errno.NoProcess
	}
	return s.Pending(), nil
}

func (t *Table) SetMasked(pid uint64, n Number, masked bool) error {
	s := t.state(pid)
	if s == nil {
		return errno.NoProcess
	}
	return s.SetMasked(n, masked)
}

func (t *Table) SetIgnored(pid uint64, n Number, ignored bool) error {
	s := t.state(pid)
	if s == nil {
		return errno.NoProcess
	}
	return s.SetIgnored(n, ignored)
}

func (t *Table) BindInterrupt(pid uint64, hook func(Number)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if hook == nil {
		delete(t.interrupts, pid)
	} else {
		t.interrupts[pid] = hook
	}
}

func (t *Table) Interrupt(pid uint64, n Number) bool {
	t.mu.Lock()
	hook := t.interrupts[pid]
	t.mu.Unlock()
	if hook == nil {
		return false
	}
	hook(n)
	return true
}

func (t *Table) Deliverable(pid uint64) (Number, bool) {
	s := t.state(pid)
	if s == nil {
		return 0, false
	}
	return s.deliverable()
}

func (t *Table) Consume(pid uint64, n Number) {
	if s := t.state(pid); s != nil {
		s.consume(n)
	}
}

func ExitCode(n Number) uint8 { return uint8(128) + uint8(n) }

func (n Number) String() string {
	if name := n.Name(); name != "" {
		return name
	}
	return fmt.Sprintf("signal(%d)", n)
}
