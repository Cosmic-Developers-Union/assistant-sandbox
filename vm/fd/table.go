package fd

import (
	"math"

	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/epoll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/eventfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/inotify"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/pipe"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signalfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/timerfd"
)

type Streams struct {
	Input         io.Reader
	Output, Error io.Writer
}

type reference struct {
	handle Stream
	refs   int

	owned bool
}

func (r *reference) release() error {
	r.refs--
	if r.refs == 0 && r.owned {
		return r.handle.Close()
	}
	return nil
}

type entry struct {
	ref     *reference
	cloexec bool
}

// Budget 是描述符表的可选预算钩子(O05):每占用一个描述符槽计费
// 一次,槽位消失归还——所有创建路径(显式安装、自动编号、管道、
// eventfd、signalfd、epoll、timerfd、dup)共享同一限额。未装配则
// 不设限。
type Budget interface {
	ChargeDescriptors(pid uint64, count int) error

	ReleaseDescriptors(pid uint64, count int)
}

type Table struct {
	mu      sync.Mutex
	entries map[uint64]map[uint32]*entry

	// timeSource 是 timerfd(L10)的时钟端口:内核装配;未装配时
	// timerfd 创建报 ENOSYS。
	timeSource clock.Source

	// budget 是共享描述符预算钩子(O05):内核装配。
	budget Budget
}

func New() *Table { return &Table{entries: map[uint64]map[uint32]*entry{}} }

// SetBudget 装配共享描述符预算(内核构造时调用一次)。
func (t *Table) SetBudget(budget Budget) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.budget = budget
}

// chargeLocked 对 pid 计 count 个描述符槽;失败返回错误且不改表。
func (t *Table) chargeLocked(pid uint64, count int) error {
	if t.budget == nil || count <= 0 {
		return nil
	}
	return t.budget.ChargeDescriptors(pid, count)
}

// releaseLocked 归还 pid 的 count 个描述符槽。
func (t *Table) releaseLocked(pid uint64, count int) {
	if t.budget == nil || count <= 0 {
		return
	}
	t.budget.ReleaseDescriptors(pid, count)
}

// SetTimeSource 装配 timerfd 的时钟端口(内核构造时调用一次)。
func (t *Table) SetTimeSource(source clock.Source) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timeSource = source
}

func (t *Table) Attach(pid uint64, s Streams) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries[pid] = map[uint32]*entry{
		0: newEntry(&stream{reader: s.Input, access: 0}, false),
		1: newEntry(&stream{writer: s.Output, access: 1}, false),
		2: newEntry(&stream{writer: s.Error, access: 1}, false),
	}
}

func (t *Table) Install(pid uint64, id uint32, handle Stream) error {
	if handle == nil {
		return errno.Invalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return errno.NoProcess
	}
	if _, taken := table[id]; taken {
		return errno.Exist
	}
	if err := t.chargeLocked(pid, 1); err != nil {
		return err
	}
	table[id] = newEntry(handle, true)
	return nil
}

func (t *Table) InstallNext(pid uint64, handle Stream) (uint32, error) {
	if handle == nil {
		return 0, errno.Invalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			if err := t.chargeLocked(pid, 1); err != nil {
				return 0, err
			}
			table[id] = newEntry(handle, true)
			return id, nil
		}
	}
	return 0, errno.Limit
}

func (t *Table) IsOpen(pid uint64, id uint32) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.entries[pid][id] != nil
}

func (t *Table) Opened(pid uint64) []uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]uint32, 0, len(t.entries[pid]))
	for id := range t.entries[pid] {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func newEntry(handle Stream, owned bool) *entry {
	return &entry{ref: &reference{handle: handle, refs: 1, owned: owned}}
}

func (t *Table) Detach(pid uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil
	}
	var closeErr error
	for _, e := range table {
		closeErr = errors.Join(closeErr, e.ref.release())
	}
	t.releaseLocked(pid, len(table))
	delete(t.entries, pid)
	return closeErr
}

func (t *Table) Get(ctx context.Context, pid uint64, id uint32) (Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries[pid] == nil {
		return nil, errno.NoProcess
	}
	e := t.entries[pid][id]
	if e == nil {
		return nil, errno.BadHandle
	}
	return e.ref.handle, nil
}

func (t *Table) Dup(ctx context.Context, pid uint64, from, to uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return errno.NoProcess
	}
	source := table[from]
	if source == nil {
		return errno.BadHandle
	}
	if from == to {
		return nil
	}
	if target := table[to]; target != nil {
		closeErr := target.ref.release()
		delete(table, to)
		t.releaseLocked(pid, 1)
		if err := t.chargeLocked(pid, 1); err != nil {
			// 预算拒绝:回滚到释放前状态(目标槽位以关闭形态消失)。
			return errors.Join(closeErr, err)
		}
		source.ref.refs++
		table[to] = &entry{ref: source.ref}
		return closeErr
	}
	if err := t.chargeLocked(pid, 1); err != nil {
		return err
	}
	source.ref.refs++
	table[to] = &entry{ref: source.ref}
	return nil
}

// Fork 把 parent 的全部描述符复制给 child:条目与 CLOEXEC 标志逐一
// 复制并共享底层句柄(与 fork 语义一致,打开文件描述共享偏移);
// 返回复制的描述符个数。
// Share 让 child 与 parent 共用同一打开描述符集合(CLONE_FILES,
// K02):任一侧的安装/关闭对双方可见,预算不重复计费——共享安装
// 由执行安装的一方计费。线程退出用 DropAlias 摘除别名而不关闭
// 句柄;最后一个别名照常由 Detach 清算。
func (t *Table) Share(parent, child uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	source := t.entries[parent]
	if source == nil {
		return errno.NoProcess
	}
	t.entries[child] = source
	return nil
}

// DropAlias 摘除 child 的描述符别名(CLONE_FILES 线程退出):
// 不触碰共享集合中的任何句柄,也不归还预算(费用记在安装方)。
func (t *Table) DropAlias(child uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, child)
}

func (t *Table) Fork(parent, child uint64) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	source := t.entries[parent]
	if source == nil {
		return 0, errno.NoProcess
	}
	if t.entries[child] == nil {
		t.entries[child] = map[uint32]*entry{}
	}
	count := 0
	for id, sourceEntry := range source {
		sourceEntry.ref.refs++
		t.entries[child][id] = &entry{ref: sourceEntry.ref, cloexec: sourceEntry.cloexec}
		count++
	}
	return count, nil
}

// Passed 是经 SCM_RIGHTS 传递的打开文件描述(M08)。发送侧 Retain
// 增加底层句柄引用;接收侧 InstallPassed 把它安装为独立描述符槽。
// 两侧共享同一打开文件描述(偏移与状态一致,与 Linux 相同)。传输
// 失败或控制消息被丢弃时 Release 归还这次引用;安装后 Release 为空
// 操作(引用已转交描述符槽)。
type Passed struct {
	ref *reference

	mu      sync.Mutex
	settled bool
}

// Handle 返回被传递的底层句柄(只读用途)。
func (p *Passed) Handle() Stream { return p.ref.handle }

// Release 归还未安装的传递引用;重复调用幂等。
func (p *Passed) Release() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.settled {
		p.mu.Unlock()
		return nil
	}
	p.settled = true
	p.mu.Unlock()
	return p.ref.release()
}

// RightsTransfer 是描述符表的 SCM_RIGHTS 能力(M08):Retain 取得
// 发送侧引用,InstallPassed 在接收进程安装。
type RightsTransfer interface {
	Retain(pid uint64, id uint32) (*Passed, error)

	InstallPassed(pid uint64, passed *Passed) (uint32, error)
}

// Retain 按 fd 取底层打开文件描述并增加一次引用(SCM_RIGHTS 发送
// 侧);发送方随后关闭自己的描述符不影响接收方。
func (t *Table) Retain(pid uint64, id uint32) (*Passed, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil, errno.NoProcess
	}
	e := table[id]
	if e == nil {
		return nil, errno.BadHandle
	}
	e.ref.refs++
	return &Passed{ref: e.ref}, nil
}

// InstallPassed 把传递引用安装为接收进程的新描述符(M08):编号取
// ≥3 的最低空闲槽位,占用一个描述符预算;失败时引用仍归调用方,
// 由 Release 归还。同一传递引用只能安装一次。
func (t *Table) InstallPassed(pid uint64, passed *Passed) (uint32, error) {
	if passed == nil {
		return 0, errno.Invalid
	}
	passed.mu.Lock()
	defer passed.mu.Unlock()
	if passed.settled {
		return 0, errno.Invalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	if err := t.chargeLocked(pid, 1); err != nil {
		return 0, err
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			table[id] = &entry{ref: passed.ref}
			passed.settled = true
			return id, nil
		}
	}
	t.releaseLocked(pid, 1)
	return 0, errno.Limit
}

// DupAt 把 from 复制到 ≥ min 的最低空闲编号并返回该编号;
// 复制共享同一底层句柄(偏移与状态共享),与 dup/fcntl F_DUPFD 一致。
func (t *Table) DupAt(ctx context.Context, pid, from, min uint64) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	if from > math.MaxUint32 {
		return 0, errno.BadHandle
	}
	source := table[uint32(from)]
	if source == nil {
		return 0, errno.BadHandle
	}
	for id := uint32(min); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			if err := t.chargeLocked(pid, 1); err != nil {
				return 0, err
			}
			source.ref.refs++
			table[id] = &entry{ref: source.ref}
			return id, nil
		}
	}
	return 0, errno.Limit
}

// Pipe 创建并安装一对匿名管道端点(vm/pipe,容量与语义与 Linux 一致:
// EOF/EPIPE/PIPE_BUF 原子写/非阻塞,poll 就绪按缓冲状态报告)。
func (t *Table) Pipe(pid uint64) (uint32, uint32, error) {
	readEnd, writeEnd, err := pipe.New(pipe.DefaultCapacity, pipe.Options{})
	if err != nil {
		return 0, 0, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, 0, errno.NoProcess
	}
	if err := t.chargeLocked(pid, 2); err != nil {
		return 0, 0, err
	}
	var readID, writeID uint32
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			readID = id
			table[id] = newEntry(readEnd, true)
			break
		}
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			writeID = id
			table[id] = newEntry(writeEnd, true)
			break
		}
	}
	if readID == 0 || writeID == 0 {
		// 槽位不足:撤销半装状态并归还计费。
		if readID != 0 {
			delete(table, readID)
			_ = readEnd.Close()
		}
		if writeID != 0 {
			delete(table, writeID)
			_ = writeEnd.Close()
		}
		t.releaseLocked(pid, 2)
		return 0, 0, errno.Limit
	}
	return readID, writeID, nil
}

// EventFD 创建并安装一个 eventfd,语义与 eventfd(2) 一致。
func (t *Table) EventFD(pid uint64, initialValue uint64, options eventfd.Options) (uint32, error) {
	handle, err := eventfd.New(initialValue, options)
	if err != nil {
		return 0, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	if err := t.chargeLocked(pid, 1); err != nil {
		return 0, err
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			table[id] = newEntry(handle, true)
			return id, nil
		}
	}
	t.releaseLocked(pid, 1)
	return 0, errno.Limit
}

func (t *Table) Memfd(pid uint64, name string) (uint32, error) {
	return t.InstallNext(pid, memfd.New(name))
}

// SignalFD 创建并安装一个 signalfd,语义与 signalfd4 一致。
// mask 是信号掩码位图(位 n-1 对应信号 n),为空由 signalfd.New 拒绝。
func (t *Table) SignalFD(pid uint64, mask uint64, options signalfd.Options) (uint32, error) {
	handle, err := signalfd.New(mask, options)
	if err != nil {
		return 0, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	if err := t.chargeLocked(pid, 1); err != nil {
		return 0, err
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			table[id] = newEntry(handle, true)
			return id, nil
		}
	}
	t.releaseLocked(pid, 1)
	return 0, errno.Limit
}

// Inotify 创建并安装一个 inotify 实例(I20),语义与 inotify_init1 一致。
func (t *Table) Inotify(pid uint64, nonblock bool) (uint32, error) {
	handle := inotify.New(inotify.Options{NonBlock: nonblock})
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	if err := t.chargeLocked(pid, 1); err != nil {
		return 0, err
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			table[id] = newEntry(handle, true)
			return id, nil
		}
	}
	t.releaseLocked(pid, 1)
	return 0, errno.Limit
}

// InotifyAt 取已存在描述符上的 inotify 实例(add_watch/rm_watch 用);
// 描述符不存在报 EBADF,不是 inotify 报 EINVAL。
func (t *Table) InotifyAt(pid uint64, id uint32) (*inotify.FD, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil, errno.NoProcess
	}
	e := table[id]
	if e == nil {
		return nil, errno.BadHandle
	}
	handle, ok := e.ref.handle.(*inotify.FD)
	if !ok {
		return nil, errno.Invalid
	}
	return handle, nil
}

// Epoll 创建并安装一个 epoll 实例(L07)。
func (t *Table) Epoll(pid uint64) (uint32, error) {
	handle := epoll.New()
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	if err := t.chargeLocked(pid, 1); err != nil {
		return 0, err
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			table[id] = newEntry(handle, true)
			return id, nil
		}
	}
	t.releaseLocked(pid, 1)
	return 0, errno.Limit
}

// TimerFD 创建并安装一个 timerfd(L10);时钟端口未装配报 ENOSYS。
func (t *Table) TimerFD(pid uint64, clockID uint64, nonblock bool) (uint32, error) {
	t.mu.Lock()
	source := t.timeSource
	t.mu.Unlock()
	if source == nil {
		return 0, errno.Wrap(errno.NotImplemented, "时钟端口未装配")
	}
	handle, err := timerfd.New(clockID, timerfd.Options{NonBlock: nonblock}, source)
	if err != nil {
		return 0, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return 0, errno.NoProcess
	}
	if err := t.chargeLocked(pid, 1); err != nil {
		return 0, err
	}
	for id := uint32(3); id < ^uint32(0); id++ {
		if _, taken := table[id]; !taken {
			table[id] = newEntry(handle, true)
			return id, nil
		}
	}
	t.releaseLocked(pid, 1)
	return 0, errno.Limit
}

// TimerFDAt 取已存在描述符上的 timerfd(settime/gettime 用);
// 描述符不存在报 EBADF,不是 timerfd 报 EINVAL。
func (t *Table) TimerFDAt(pid uint64, id uint32) (*timerfd.FD, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil, errno.NoProcess
	}
	e := table[id]
	if e == nil {
		return nil, errno.BadHandle
	}
	handle, ok := e.ref.handle.(*timerfd.FD)
	if !ok {
		return nil, errno.Invalid
	}
	return handle, nil
}

// EpollAt 取已存在描述符上的 epoll 实例(epoll_ctl/epoll_pwait 用);
// 描述符不存在报 EBADF,不是 epoll 实例报 EINVAL。
func (t *Table) EpollAt(pid uint64, id uint32) (*epoll.FD, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil, errno.NoProcess
	}
	e := table[id]
	if e == nil {
		return nil, errno.BadHandle
	}
	handle, ok := e.ref.handle.(*epoll.FD)
	if !ok {
		return nil, errno.Invalid
	}
	return handle, nil
}

// SignalFDAt 取已存在描述符上的 signalfd(signalfd4 更新掩码用);
// 描述符不存在报 errno.BadHandle,不是 signalfd 报 errno.Invalid。
func (t *Table) SignalFDAt(pid uint64, id uint32) (*signalfd.FD, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil, errno.NoProcess
	}
	e := table[id]
	if e == nil {
		return nil, errno.BadHandle
	}
	handle, ok := e.ref.handle.(*signalfd.FD)
	if !ok {
		return nil, errno.Invalid
	}
	return handle, nil
}

// SignalFDs 返回进程当前持有的全部 signalfd,供内核把被屏蔽
// 信号路由到 fd 队列(L11)。
func (t *Table) SignalFDs(pid uint64) []*signalfd.FD {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*signalfd.FD
	for _, e := range t.entries[pid] {
		if handle, ok := e.ref.handle.(*signalfd.FD); ok {
			out = append(out, handle)
		}
	}
	return out
}

func (t *Table) Close(ctx context.Context, pid uint64, id uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return errno.NoProcess
	}
	e := table[id]
	if e == nil {
		return errno.BadHandle
	}
	delete(table, id)
	t.releaseLocked(pid, 1)
	return e.ref.release()
}

func (t *Table) Cloexec(pid uint64, id uint32) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[pid][id]
	if e == nil {
		return false, errno.BadHandle
	}
	return e.cloexec, nil
}

func (t *Table) SetCloexec(pid uint64, id uint32, cloexec bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[pid][id]
	if e == nil {
		return errno.BadHandle
	}
	e.cloexec = cloexec
	return nil
}

func (t *Table) CloseCloexec(ctx context.Context, pid uint64) ([]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	table := t.entries[pid]
	if table == nil {
		return nil, errno.NoProcess
	}
	var closed []uint32
	for id, e := range table {
		if !e.cloexec {
			continue
		}
		closed = append(closed, id)
	}
	slices.Sort(closed)
	var closeErr error
	for _, id := range closed {
		e := table[id]
		delete(table, id)
		closeErr = errors.Join(closeErr, e.ref.release())
	}
	return closed, closeErr
}

type terminalStream interface {
	Dimensions() (rows, columns uint16)
}

func (t *Table) Terminal(ctx context.Context, pid uint64, id uint32) (Terminal, error) {
	t.mu.Lock()
	table := t.entries[pid]
	if table == nil {
		t.mu.Unlock()
		return Terminal{}, errno.NoProcess
	}
	entry := table[id]
	if entry == nil {
		t.mu.Unlock()
		return Terminal{}, errno.BadHandle
	}
	handle := entry.ref.handle
	t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Terminal{}, err
	}
	// 作用域包装器可能为了转发 PTY 能力而无条件实现 Dimensions;
	// 显式的 IsTerminal 标记可将其与真实终端区分。
	if marker, ok := handle.(interface{ IsTerminal() bool }); ok && !marker.IsTerminal() {
		return Terminal{}, nil
	}
	terminal, ok := handle.(terminalStream)
	if !ok {
		return Terminal{}, nil
	}
	rows, columns := terminal.Dimensions()
	info := Terminal{TTY: true, Rows: rows, Columns: columns}
	if stateful, ok := handle.(StatefulTerminal); ok {
		info.State = stateful.State()
	}
	return info, nil
}

func (t *Table) SetTerminalState(ctx context.Context, pid uint64, id uint32, state TerminalState) error {
	t.mu.Lock()
	table := t.entries[pid]
	if table == nil {
		t.mu.Unlock()
		return errno.NoProcess
	}
	entry := table[id]
	if entry == nil {
		t.mu.Unlock()
		return errno.BadHandle
	}
	handle := entry.ref.handle
	t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	stateful, ok := handle.(StatefulTerminal)
	if !ok {
		return errno.NotSupported
	}
	return stateful.SetState(state)
}

type stream struct {
	reader io.Reader
	writer io.Writer

	// access 是打开访问模式(F_GETFL 的 O_RDONLY/O_WRONLY 位),
	// nonblock 是运行时可切换的 O_NONBLOCK。
	access   uint32
	nonblock atomic.Bool
}

func (s *stream) Read(p []byte) (int, error) {
	if s.reader == nil {
		return 0, io.EOF
	}
	if s.nonblock.Load() {
		if terminal, ok := s.reader.(interface {
			Ready() poll.Readiness
		}); ok && terminal.Ready()&poll.Readable == 0 {
			return 0, errno.WouldBlock
		}
	}
	return s.reader.Read(p)
}
func (s *stream) Write(p []byte) (int, error) {
	if s.writer == nil {
		return 0, io.ErrClosedPipe
	}
	return s.writer.Write(p)
}
func (s *stream) Close() error { return nil }

// StatusFlags 报告标准流状态标志(F_GETFL):访问模式与 O_NONBLOCK。
func (s *stream) StatusFlags() (uint32, error) {
	flags := s.access
	if s.nonblock.Load() {
		flags |= 0o4000
	}
	return flags, nil
}

// SetStatusFlags 应用 F_SETFL:标准流只记录 O_NONBLOCK。
func (s *stream) SetStatusFlags(flags uint32) error {
	s.nonblock.Store(flags&0o4000 != 0)
	return nil
}

// streamInfo 是标准流/管道句柄的 fstat 视图:字符设备或 FIFO,
// 使 libc/运行时对 stdin/stdout/stderr 的 fstat 不再 ENOTSUP。
type streamInfo struct {
	name string
	pipe bool
}

func (i streamInfo) Name() string { return i.name }
func (i streamInfo) Size() int64  { return 0 }
func (i streamInfo) Mode() stdfs.FileMode {
	if i.pipe {
		return stdfs.ModeNamedPipe | 0o600
	}
	return stdfs.ModeDevice | stdfs.ModeCharDevice | 0o600
}
func (i streamInfo) ModTime() time.Time { return time.Time{} }
func (i streamInfo) IsDir() bool        { return false }
func (i streamInfo) Sys() any           { return nil }

func (s *stream) Stat() (stdfs.FileInfo, error) {
	return streamInfo{name: "stream", pipe: !s.IsTerminal()}, nil
}

func (s *stream) ReadContext(ctx context.Context, p []byte) (int, error) {
	if blocking, ok := s.reader.(interface {
		ReadContext(context.Context, []byte) (int, error)
	}); ok {
		return blocking.ReadContext(ctx, p)
	}
	return s.Read(p)
}

// Terminal capability forwarding: standard streams wrap the host-provided
// reader/writer, so a PTY slave installed as fd 0/1/2 must remain visible as
// a terminal to isatty/tcgetattr/tcsetpgrp.
func (s *stream) terminalSource() (terminalStream, bool) {
	if terminal, ok := s.reader.(terminalStream); ok {
		return terminal, true
	}
	terminal, ok := s.writer.(terminalStream)
	return terminal, ok
}

func (s *stream) IsTerminal() bool {
	_, ok := s.terminalSource()
	return ok
}

func (s *stream) Dimensions() (uint16, uint16) {
	if terminal, ok := s.terminalSource(); ok {
		return terminal.Dimensions()
	}
	return 0, 0
}

func (s *stream) State() TerminalState {
	if terminal, ok := s.terminalSource(); ok {
		if stateful, ok := terminal.(StatefulTerminal); ok {
			return stateful.State()
		}
	}
	return TerminalState{}
}

func (s *stream) SetState(state TerminalState) error {
	if terminal, ok := s.terminalSource(); ok {
		if stateful, ok := terminal.(StatefulTerminal); ok {
			return stateful.SetState(state)
		}
	}
	return errno.NotATerminal
}

func (s *stream) Foreground() uint64 {
	if terminal, ok := s.terminalSource(); ok {
		if foreground, ok := terminal.(ForegroundTerminal); ok {
			return foreground.Foreground()
		}
	}
	return 0
}

func (s *stream) SetForeground(pgid uint64) error {
	if terminal, ok := s.terminalSource(); ok {
		if foreground, ok := terminal.(ForegroundTerminal); ok {
			return foreground.SetForeground(pgid)
		}
	}
	return errno.NotATerminal
}

func (s *stream) ForegroundSupported() bool {
	terminal, ok := s.terminalSource()
	if !ok {
		return false
	}
	foreground, ok := terminal.(ForegroundTerminal)
	return ok && foreground.ForegroundSupported()
}

func (s *stream) pollSource() (poll.Source, bool) {
	if source, ok := s.reader.(poll.Source); ok {
		return source, true
	}
	source, ok := s.writer.(poll.Source)
	return source, ok
}

func (s *stream) Ready() poll.Readiness {
	if source, ok := s.pollSource(); ok {
		return source.Ready()
	}
	return poll.Readable | poll.Writable
}

func (s *stream) OnChange(fn func()) func() {
	if source, ok := s.pollSource(); ok {
		return source.OnChange(fn)
	}
	return func() {}
}

func (t *Table) SetTabStops(stops []uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, table := range t.entries {
		for _, entry := range table {
			if entry == nil {
				continue
			}
			if setter, ok := entry.ref.handle.(TabStopSetter); ok {
				return setter.SetTabStops(stops)
			}
		}
	}
	return errno.NotSupported
}

func (t *Table) TabStops() []uint16 {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, table := range t.entries {
		for _, entry := range table {
			if entry == nil {
				continue
			}
			if setter, ok := entry.ref.handle.(TabStopSetter); ok {
				return setter.TabStops()
			}
		}
	}
	return nil
}

var (
	_ Descriptors = (*Table)(nil)

	_ Installer = (*Table)(nil)

	_ TabStopSetter = (*Table)(nil)
)
