package proc

import (
	"fmt"
	stdfs "io/fs"
	"maps"
	"slices"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type PID uint64

type State uint8

const (
	Ready State = iota
	Running

	// Stopped 是收到 SIGSTOP 族后的驻停状态:任务暂停推进,
	// SIGCONT 恢复,SIGKILL 可直接终止(J07)。
	Stopped

	Zombie
)

func (s State) String() string {
	switch s {
	case Ready:
		return "ready"
	case Running:
		return "running"
	case Stopped:
		return "stopped"
	case Zombie:
		return "zombie"
	default:
		return "unknown"
	}
}

type Proc struct {
	Control Control `json:"-"`

	Resources  capability.Access `json:"-"`
	ID, Parent PID

	Generation uint64
	Program    string
	Args       []string
	Dir        string
	Env        map[string]string
	State      State
	ExitCode   uint8
	ExitSignal uint8

	Umask stdfs.FileMode

	Nice int

	PGID, SID PID
}

const (
	NiceLow  = -20
	NiceHigh = 19
)

const DefaultNice = 0

type Table struct {
	next    PID
	limit   int
	init    PID
	entries map[PID]Proc
}

func New(limit int) (*Table, error) {
	if limit < 1 {
		return nil, fmt.Errorf("进程预算必须为正数: %w", errno.Invalid)
	}
	return &Table{next: 1, limit: limit, entries: map[PID]Proc{}}, nil
}

func clone(p Proc) Proc {
	p.Args = slices.Clone(p.Args)
	p.Env = maps.Clone(p.Env)
	return p
}

func (t *Table) Init() PID { return t.init }

func (t *Table) Limit() int { return t.limit }

func (t *Table) Create(parent PID, program string, args []string, dir string, env map[string]string) (Proc, error) {
	if program == "" || dir == "" || dir[0] != '/' {
		return Proc{}, fmt.Errorf("进程需要程序名与客体绝对目录: %w", errno.Invalid)
	}
	p := Proc{
		ID:         t.next,
		Program:    program,
		Args:       slices.Clone(args),
		Dir:        dir,
		Env:        maps.Clone(env),
		State:      Ready,
		Generation: 1,

		Umask: DefaultUmask,
	}
	if parent == 0 {
		if t.init != 0 {
			return Proc{}, fmt.Errorf("启动进程已存在: %w", errno.Busy)
		}
		p.Parent = 0
		p.PGID = p.ID
		p.SID = p.ID
		t.init = p.ID
	} else {
		pp, ok := t.entries[parent]
		if !ok {
			return Proc{}, errno.NoProcess
		}
		if pp.State != Running {
			return Proc{}, errno.Busy
		}
		p.Parent = parent
		p.PGID = pp.PGID
		p.SID = pp.SID
		p.Umask = pp.Umask
		p.Nice = pp.Nice
	}
	if t.entries == nil || len(t.entries) >= t.limit || t.next == 0 {
		return Proc{}, errno.Limit
	}
	t.next++
	t.entries[p.ID] = p
	return clone(p), nil
}

const DefaultUmask stdfs.FileMode = 0o022

func (t *Table) SetUmask(id PID, mask stdfs.FileMode) error {
	if mask&^stdfs.ModePerm != 0 {
		return fmt.Errorf("创建掩码只能是权限位: %w", errno.Invalid)
	}
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	p.Umask = mask
	t.entries[id] = p
	return nil
}

func (t *Table) SetNice(id PID, nice int) error {
	if nice < NiceLow || nice > NiceHigh {
		return fmt.Errorf("优先级 %d 超出边界 [%d, %d]: %w", nice, NiceLow, NiceHigh, errno.Invalid)
	}
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	p.Nice = nice
	t.entries[id] = p
	return nil
}

func (t *Table) Get(id PID) (Proc, error) {
	p, ok := t.entries[id]
	if !ok {
		return Proc{}, errno.NoProcess
	}
	return clone(p), nil
}

func (t *Table) GetLive(id PID, generation uint64) (Proc, error) {
	p, err := t.Get(id)
	if err != nil {
		return Proc{}, err
	}
	if p.Generation != generation {
		return Proc{}, errno.NoProcess
	}
	return p, nil
}

func (t *Table) List() []Proc {
	out := make([]Proc, 0, len(t.entries))
	for _, id := range slices.Sorted(maps.Keys(t.entries)) {
		out = append(out, clone(t.entries[id]))
	}
	return out
}

func (t *Table) Children(parent PID) []Proc {
	var out []Proc
	for _, id := range slices.Sorted(maps.Keys(t.entries)) {
		if t.entries[id].Parent == parent {
			out = append(out, clone(t.entries[id]))
		}
	}
	return out
}

func (t *Table) Start(id PID) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	if p.State != Ready {
		return errno.Busy
	}
	p.State = Running
	t.entries[id] = p
	return nil
}

func (t *Table) Exit(id PID, code uint8, signal uint8) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	// 驻停进程可被 SIGKILL 直接终止:Stopped → Zombie 合法。
	if p.State != Running && p.State != Stopped {
		return errno.Busy
	}
	p.State = Zombie
	p.ExitCode = code
	p.ExitSignal = signal
	for childID, child := range t.entries {
		if child.Parent == id && childID != id {
			child.Parent = t.init
			t.entries[childID] = child
		}
	}
	t.entries[id] = p
	return nil
}

// Stop 把运行中的进程转入驻停状态;僵尸与已驻停进程报错。
func (t *Table) Stop(id PID) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	if p.State != Running {
		return errno.Busy
	}
	p.State = Stopped
	t.entries[id] = p
	return nil
}

// Continue 把驻停进程恢复为运行;非驻停进程按 Linux 语义视为无操作成功。
func (t *Table) Continue(id PID) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	if p.State == Stopped {
		p.State = Running
		t.entries[id] = p
	}
	return nil
}

func (t *Table) Reap(parent, id PID) (Proc, error) {
	p, ok := t.entries[id]
	if !ok || p.Parent != parent {
		return Proc{}, errno.NoChild
	}
	if p.State != Zombie {
		return Proc{}, errno.Busy
	}
	delete(t.entries, id)
	return clone(p), nil
}

func (t *Table) WaitAny(parent PID) (Proc, bool) {
	for _, id := range slices.Sorted(maps.Keys(t.entries)) {
		if p := t.entries[id]; p.Parent == parent && p.State == Zombie {
			return clone(p), true
		}
	}
	return Proc{}, false
}

func (t *Table) HasChildren(parent PID) bool {
	for _, p := range t.entries {
		if p.Parent == parent {
			return true
		}
	}
	return false
}

func (t *Table) SetPGID(caller, target, pgid PID) error {
	p, ok := t.entries[target]
	if !ok {
		return errno.NoProcess
	}
	if caller != target && p.Parent != caller {
		return errno.Permission
	}
	if p.SID == target {

		return errno.Permission
	}
	if pgid == 0 {
		pgid = target
	}
	if pgid != target {
		group, ok := t.entries[pgid]
		if !ok {
			return errno.Permission
		}
		if group.PGID != pgid || group.SID != p.SID {
			return errno.Permission
		}
	}
	p.PGID = pgid
	t.entries[target] = p
	return nil
}

func (t *Table) SetSID(id PID) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	if p.PGID == id {
		return errno.Permission
	}
	p.SID = id
	p.PGID = id
	t.entries[id] = p
	return nil
}

func (t *Table) Replace(id PID, program string, args []string, dir string, env map[string]string) (before, after Proc, err error) {
	if program == "" || dir == "" || dir[0] != '/' {
		return Proc{}, Proc{}, fmt.Errorf("进程需要程序名与客体绝对目录: %w", errno.Invalid)
	}
	p, ok := t.entries[id]
	if !ok {
		return Proc{}, Proc{}, errno.NoProcess
	}
	if p.State != Running {
		return Proc{}, Proc{}, errno.Busy
	}
	before = clone(p)
	p.Program = program
	p.Args = slices.Clone(args)
	p.Dir = dir
	p.Env = maps.Clone(env)

	p.Generation++
	t.entries[id] = p
	return before, clone(p), nil
}
