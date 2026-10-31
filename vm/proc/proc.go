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

	// Waiting 是阻塞在等待队列中的状态:进程不可被调度,但仍可被
	// 唤醒、可被信号终止(H01)。Linux 中对应 S 状态。
	Waiting

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
	case Waiting:
		return "waiting"
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

	// TID 是线程标识,TGID 是线程组标识:单线程模型下二者都等于
	// 进程号;线程创建落地后同组线程共享 TGID、各自持有 TID
	// (K01/K02)。
	TID, TGID PID

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
	// groups 记录线程组存活成员:TGID → 组内 TID 集合(含组长)。
	// 线程退出递减,组空时组长转僵尸并通知父进程(K01/K02)。
	groups map[PID]map[PID]struct{}
}

func New(limit int) (*Table, error) {
	if limit < 1 {
		return nil, fmt.Errorf("进程预算必须为正数: %w", errno.Invalid)
	}
	return &Table{next: 1, limit: limit, entries: map[PID]Proc{}, groups: map[PID]map[PID]struct{}{}}, nil
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
		TID:        t.next,
		TGID:       t.next,
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
	t.join(p.ID, p.TGID)
	return clone(p), nil
}

// join 把 TID 登记进其线程组;组集合随用随建。
func (t *Table) join(tid, tgid PID) {
	set, ok := t.groups[tgid]
	if !ok {
		set = map[PID]struct{}{}
		t.groups[tgid] = set
	}
	set[tid] = struct{}{}
}

// CreateThread 在 parent 所属线程组内创建线程(K01/K02):新条目
// 的 TID 独立分配,TGID、进程组、会话与父指针继承组长语义;
// 程序名/目录/环境/掩码按 clone 语义复制创建线程当前值。
func (t *Table) CreateThread(parent PID, program string) (Proc, error) {
	pp, ok := t.entries[parent]
	if !ok {
		return Proc{}, errno.NoProcess
	}
	if pp.State != Running && pp.State != Waiting {
		return Proc{}, errno.Busy
	}
	if t.entries == nil || len(t.entries) >= t.limit || t.next == 0 {
		return Proc{}, errno.Limit
	}
	p := Proc{
		ID:         t.next,
		TID:        t.next,
		TGID:       pp.TGID,
		Parent:     pp.Parent,
		Program:    pp.Program,
		Args:       slices.Clone(pp.Args),
		Dir:        pp.Dir,
		Env:        maps.Clone(pp.Env),
		State:      Ready,
		Generation: pp.Generation,

		PGID:  pp.PGID,
		SID:   pp.SID,
		Umask: pp.Umask,
		Nice:  pp.Nice,
	}
	t.next++
	t.entries[p.ID] = p
	t.join(p.ID, p.TGID)
	return clone(p), nil
}

// Threads 返回线程组内存活成员(含组长),按 TID 排序。
func (t *Table) Threads(tgid PID) []Proc {
	set, ok := t.groups[tgid]
	if !ok {
		return nil
	}
	out := make([]Proc, 0, len(set))
	for tid := range set {
		if p, ok := t.entries[tid]; ok {
			out = append(out, clone(p))
		}
	}
	slices.SortFunc(out, func(a, b Proc) int { return int(a.ID) - int(b.ID) })
	return out
}

// ThreadCount 返回线程组内存活成员数。
func (t *Table) ThreadCount(tgid PID) int {
	return len(t.groups[tgid])
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
	return t.Transition(id, Running)
}

// Transition 把进程状态迁移到目标状态:不合法的迁移报 EBUSY。
// 合法迁移与 Linux 进程状态一致——就绪→运行;运行→等待/驻停/僵尸;
// 等待→运行/驻停/僵尸;驻停→运行/僵尸;僵尸是终态,只能被回收移除。
func (t *Table) Transition(id PID, to State) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	if !canTransit(p.State, to) {
		return errno.Busy
	}
	p.State = to
	t.entries[id] = p
	return nil
}

// canTransit 判断状态迁移是否合法。
func canTransit(from, to State) bool {
	switch from {
	case Ready:
		return to == Running
	case Running:
		return to == Waiting || to == Stopped || to == Zombie
	case Waiting:
		return to == Running || to == Stopped || to == Zombie
	case Stopped:
		// 驻停进程不占用等待队列:继续后回到运行,不能直接进入等待。
		return to == Running || to == Zombie
	case Zombie:
		return false
	}
	return false
}

func (t *Table) Exit(id PID, code uint8, signal uint8) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	// 驻停与阻塞进程可被 SIGKILL 直接终止:Stopped/Waiting →
	// Zombie 合法。
	if p.State != Running && p.State != Stopped && p.State != Waiting {
		return errno.Busy
	}
	// 线程组语义(H08/K02):exit_group 结束整组——非组长条目直接
	// 移除,组长条目承载退出状态转僵尸。单线程进程即普通退出。
	tgid := p.TGID
	set := t.groups[tgid]
	for tid := range set {
		for childID, child := range t.entries {
			if child.Parent == tid && childID != tid {
				child.Parent = t.init
				t.entries[childID] = child
			}
		}
		if tid != tgid {
			delete(t.entries, tid)
		}
	}
	t.groups[tgid] = map[PID]struct{}{tgid: {}}
	leader := t.entries[tgid]
	leader.ExitCode = code
	leader.ExitSignal = signal
	leader.State = Zombie
	t.entries[tgid] = leader
	return nil
}

// ExitThread 退出单个线程(K02/H08):非组长条目直接移除(线程无
// 僵尸,join 靠 clear_child_tid 唤醒);组长条目在组内存活期间保留
// 为占位僵尸(不可回收),组内最后一个线程退出时以该线程的状态
// 定稿并报告组完成。groupDone=true 表示整个进程已转僵尸。
func (t *Table) ExitThread(id PID, code uint8, signal uint8) (bool, error) {
	p, ok := t.entries[id]
	if !ok {
		return false, errno.NoProcess
	}
	if p.State != Running && p.State != Stopped && p.State != Waiting {
		return false, errno.Busy
	}
	p.ExitCode = code
	p.ExitSignal = signal
	for childID, child := range t.entries {
		if child.Parent == id && childID != id {
			child.Parent = t.init
			t.entries[childID] = child
		}
	}
	set := t.groups[p.TGID]
	if id == p.TGID {
		p.State = Zombie
		t.entries[id] = p
		return len(set) == 1, nil
	}
	delete(t.entries, id)
	delete(set, id)
	leader := t.entries[p.TGID]
	if leader.State != Zombie {
		// 组长仍在运行:组未结束(最后退出的成员若是组长,由
		// 组长分支定稿)。
		return false, nil
	}
	// 组长已先退出并保留占位僵尸:全组就此结束,以最后退出的
	// 线程状态定稿。
	leader.ExitCode = code
	leader.ExitSignal = signal
	t.entries[leader.ID] = leader
	return true, nil
}

// groupFinished 判断组长的僵尸占位是否已可回收:组内只剩组长自身。
func (t *Table) groupFinished(p Proc) bool {
	return p.State == Zombie && len(t.groups[p.TGID]) <= 1
}

// Reapable 报告 id 是否处于可回收终态:僵尸且(组长时)整组已结束。
// 等待路径以此判断唤醒,避免组长占位僵尸提前唤醒 wait*。
func (t *Table) Reapable(id PID) bool {
	p, ok := t.entries[id]
	if !ok {
		return false
	}
	return t.groupFinished(p)
}

// Stop 把运行中的进程转入驻停状态;僵尸与已驻停进程报错。阻塞
// 中的进程同样可以被 SIGSTOP 驻停(Linux:S → T)。
func (t *Table) Stop(id PID) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	if p.State != Running && p.State != Waiting {
		return errno.Busy
	}
	return t.Transition(id, Stopped)
}

// Continue 把驻停进程恢复为运行;非驻停进程按 Linux 语义视为无操作成功。
func (t *Table) Continue(id PID) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	if p.State == Stopped {
		return t.Transition(id, Running)
	}
	return nil
}

func (t *Table) Reap(parent, id PID) (Proc, error) {
	p, ok := t.entries[id]
	if !ok || p.Parent != parent {
		return Proc{}, errno.NoChild
	}
	if p.State != Zombie || !t.groupFinished(p) {
		// 组长僵尸在组内仍有存活线程时不可回收(K02):等待继续。
		return Proc{}, errno.Busy
	}
	delete(t.entries, id)
	delete(t.groups, p.TGID)
	return clone(p), nil
}

func (t *Table) WaitAny(parent PID) (Proc, bool) {
	for _, id := range slices.Sorted(maps.Keys(t.entries)) {
		if p := t.entries[id]; p.Parent == parent && t.groupFinished(p) {
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

// SetPGID 按 setpgid(2) 的 Linux 权限规则调整进程组(H11):
//   - 目标必须是调用方自身或其子进程,否则 EPERM;
//   - 目标与调用方必须同会话,否则 EPERM;
//   - 目标是会话组长时 EPERM;
//   - 已 execve 的子进程报 EACCES(Linux:子进程已 exec 后不可改组);
//   - 指定进程组必须是同会话中已存在的组,否则 EPERM。
//
// pgid=0 表示以目标进程号作为组号。
func (t *Table) SetPGID(caller, target, pgid PID) error {
	callerProc, ok := t.entries[caller]
	if !ok {
		return errno.NoProcess
	}
	p, ok := t.entries[target]
	if !ok {
		return errno.NoProcess
	}
	if target != caller {
		if p.Parent != caller {
			return errno.NotPermitted
		}
		if p.Generation > 1 {
			return errno.Permission
		}
	}
	if p.SID != callerProc.SID {
		return errno.NotPermitted
	}
	if p.SID == p.ID {
		// 会话组长不能被移出其会话。
		return errno.NotPermitted
	}
	if pgid == 0 {
		pgid = target
	}
	if pgid != target {
		group, ok := t.entries[pgid]
		if !ok {
			// 进程组不存在:Linux 报 ESRCH。
			return errno.NoProcess
		}
		if group.PGID != pgid || group.SID != callerProc.SID {
			return errno.NotPermitted
		}
	}
	p.PGID = pgid
	t.entries[target] = p
	return nil
}

// SetSID 按 setsid(2) 建立新会话:调用方成为会话组长与进程组组长。
// 调用方已是进程组组长时 Linux 报 EPERM。
func (t *Table) SetSID(id PID) error {
	p, ok := t.entries[id]
	if !ok {
		return errno.NoProcess
	}
	if p.PGID == id {
		return errno.NotPermitted
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
