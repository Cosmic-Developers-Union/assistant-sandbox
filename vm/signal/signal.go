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

type State struct {
	mu      sync.Mutex
	pending uint32
	blocked uint32
	ignored uint32
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
	state.mu.Unlock()
	delete(t.interrupts, pid)
	return nil
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
