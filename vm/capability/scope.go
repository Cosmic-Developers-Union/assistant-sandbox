package capability

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/bridge"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/epoll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/eventfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	fs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/image"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/inotify"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/ipc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/language"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signalfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/storage"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/timerfd"
)

const (
	costMeta        = 1
	costNamespace   = 4
	costWriteBucket = 4096
)

func Scope(pid uint64, set Set, meter resources.Meter) Access {
	if _, err := Require(meter); err != nil {
		meter = nil
	}
	return &scoped{pid: pid, set: set, meter: meter}
}

type scoped struct {
	pid   uint64
	set   Set
	meter resources.Meter
}

func (s *scoped) charge(ctx context.Context, cost uint64) error {
	if s.meter == nil {
		return nil
	}
	return s.meter.Charge(ctx, s.pid, cost)
}

func (s *scoped) Filesystem() (fs.System, error) {
	base, err := s.set.Filesystem()
	if err != nil {
		return nil, err
	}
	return &chargeFS{base: base, scope: s}, nil
}

func (s *scoped) Time() (clock.Source, error) { return s.set.Time() }

func (s *scoped) Umask() (stdfs.FileMode, error) {
	if s.set.UmaskSource == nil {
		return 0, errno.Wrap(errno.NotImplemented, "装配层未提供创建掩码")
	}
	return s.set.UmaskSource.Umask(s.pid)
}

func (s *scoped) Random() (entropy.Source, error) { return s.set.Random() }

func (s *scoped) SetCredentials(ctx context.Context, uid, gid uint32) error {
	source, err := s.set.Credentials()
	if err != nil {
		return err
	}
	mutable, ok := source.(interface {
		Set(uint64, identity.Identity) error
	})
	if !ok {
		return errno.Wrap(errno.NotSupported, "身份表不可变")
	}
	return mutable.Set(s.pid, identity.Identity{UID: uid, GID: gid, Name: "sandbox"})
}

func (s *scoped) Credentials() (identity.Source, error) {
	base, err := s.set.Credentials()
	if err != nil {
		return nil, err
	}
	return scopedIdentity{source: base, pid: s.pid}, nil
}

func (s *scoped) Descriptors() (fd.Descriptors, error) {
	base, err := s.set.Descriptors()
	if err != nil {
		return nil, err
	}
	scoped := scopedFDs{base: base, pid: s.pid}

	if installer, ok := base.(fd.Installer); ok {
		return scopedInstaller{scopedFDs: scoped, installer: installer}, nil
	}
	return scoped, nil
}

func (s *scoped) AddressSpace() (Spaces, error) {
	base, err := s.set.AddressSpace()
	if err != nil {
		return nil, err
	}
	return scopedSpaces{base: base, pid: s.pid}, nil
}

type scopedSpaces struct {
	base Spaces
	pid  uint64
}

func (s scopedSpaces) BindAddressSpace(_ uint64, inner *memory.Table) (memory.Space, error) {
	return s.base.BindAddressSpace(s.pid, inner)
}

func (s *scoped) Meter() (resources.Meter, error) {
	if _, err := Require(s.meter); err != nil {
		return nil, err
	}
	return scopedMeter{base: s.meter, pid: s.pid}, nil
}

func (s *scoped) Host() (bridge.Bridge, error) { return s.set.Host() }

func (s *scoped) InitialImage() (image.Image, error) { return s.set.InitialImage() }

func (s *scoped) Events() (audit.Sink, error) { return s.set.Events() }

func (s *scoped) Languages() (language.Slots, error) { return s.set.Languages() }

func (s *scoped) SessionStore() (storage.Sessions, error) { return s.set.SessionStore() }

func (s *scoped) SnapshotStore() (storage.Snapshots, error) { return s.set.SnapshotStore() }

func (s *scoped) Journal() (replay.Journal, error) { return s.set.Journal() }

func (s *scoped) Network() (net.Network, error) { return s.set.Network() }

func (s *scoped) Communication() (ipc.IPC, error) { return s.set.Communication() }

type scopedFDs struct {
	base fd.Descriptors
	pid  uint64
}

func (s scopedFDs) Get(ctx context.Context, _ uint64, id uint32) (fd.Stream, error) {
	return s.base.Get(ctx, s.pid, id)
}

func (s scopedFDs) Dup(ctx context.Context, _ uint64, from, to uint32) error {
	return s.base.Dup(ctx, s.pid, from, to)
}

func (s scopedFDs) Close(ctx context.Context, _ uint64, id uint32) error {
	return s.base.Close(ctx, s.pid, id)
}

func (s scopedFDs) Terminal(ctx context.Context, _ uint64, id uint32) (fd.Terminal, error) {
	return s.base.Terminal(ctx, s.pid, id)
}

func (s scopedFDs) SetTerminalState(ctx context.Context, _ uint64, id uint32, state fd.TerminalState) error {
	setter, ok := s.base.(fd.TerminalSetter)
	if !ok {
		return errno.Wrap(errno.NotSupported, "描述符表不支持终端状态")
	}
	return setter.SetTerminalState(ctx, s.pid, id, state)
}

// Pipe 实现 fd.Creator:以作用域绑定的进程创建管道描述符。
func (s scopedFDs) Pipe(uint64) (uint32, uint32, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.Pipe(s.pid)
	}
	return 0, 0, errno.Wrap(errno.NotSupported, "描述符表不支持管道")
}

// EventFD 实现 fd.Creator:以作用域绑定的进程创建 eventfd。
func (s scopedFDs) EventFD(_ uint64, initialValue uint64, options eventfd.Options) (uint32, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.EventFD(s.pid, initialValue, options)
	}
	return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 eventfd")
}

func (s scopedFDs) Memfd(_ uint64, name string) (uint32, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.Memfd(s.pid, name)
	}
	return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 memfd")
}

// SignalFD 实现 fd.Creator:以作用域绑定的进程创建 signalfd。
func (s scopedFDs) SignalFD(_ uint64, mask uint64, options signalfd.Options) (uint32, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.SignalFD(s.pid, mask, options)
	}
	return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 signalfd")
}

// SignalFDAt 实现 fd.Creator:取作用域绑定进程的 signalfd。
func (s scopedFDs) SignalFDAt(_ uint64, id uint32) (*signalfd.FD, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.SignalFDAt(s.pid, id)
	}
	return nil, errno.Wrap(errno.NotSupported, "描述符表不支持 signalfd")
}

// TimerFD 实现 fd.Creator:以作用域绑定的进程创建 timerfd。
func (s scopedFDs) TimerFD(_ uint64, clockID uint64, nonblock bool) (uint32, error) {
	creator, ok := s.base.(fd.Creator)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 timerfd")
	}
	return creator.TimerFD(s.pid, clockID, nonblock)
}

// TimerFDAt 实现 fd.Creator:取作用域绑定进程的 timerfd。
func (s scopedFDs) TimerFDAt(_ uint64, id uint32) (*timerfd.FD, error) {
	creator, ok := s.base.(fd.Creator)
	if !ok {
		return nil, errno.Wrap(errno.NotSupported, "描述符表不支持 timerfd")
	}
	return creator.TimerFDAt(s.pid, id)
}

// Epoll 实现 fd.Creator:以作用域绑定的进程创建 epoll 实例。
func (s scopedFDs) Epoll(uint64) (uint32, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.Epoll(s.pid)
	}
	return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 epoll")
}

// EpollAt 实现 fd.Creator:取作用域绑定进程的 epoll 实例。
func (s scopedFDs) EpollAt(_ uint64, id uint32) (*epoll.FD, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.EpollAt(s.pid, id)
	}
	return nil, errno.Wrap(errno.NotSupported, "描述符表不支持 epoll")
}

// Inotify 实现 fd.Creator:以作用域绑定的进程创建 inotify 实例(I20)。
func (s scopedFDs) Inotify(_ uint64, nonblock bool) (uint32, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.Inotify(s.pid, nonblock)
	}
	return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 inotify")
}

// InotifyAt 实现 fd.Creator:取作用域绑定进程的 inotify 实例。
func (s scopedFDs) InotifyAt(_ uint64, id uint32) (*inotify.FD, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.InotifyAt(s.pid, id)
	}
	return nil, errno.Wrap(errno.NotSupported, "描述符表不支持 inotify")
}

// DupAt 实现 fd.Creator:以作用域绑定的进程复制到 ≥min 的最低空闲编号。
func (s scopedFDs) DupAt(ctx context.Context, _, from, min uint64) (uint32, error) {
	if creator, ok := s.base.(fd.Creator); ok {
		return creator.DupAt(ctx, s.pid, from, min)
	}
	return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 dup")
}

type scopedInstaller struct {
	scopedFDs
	installer fd.Installer
}

func (s scopedInstaller) InstallNext(_ uint64, handle fd.Stream) (uint32, error) {
	return s.installer.InstallNext(s.pid, handle)
}

func (s scopedInstaller) SetCloexec(_ uint64, id uint32, flag bool) error {
	return s.installer.SetCloexec(s.pid, id, flag)
}

// Cloexec 实现 fd.Installer:查询作用域绑定进程的 FD_CLOEXEC 标志。
func (s scopedInstaller) Cloexec(_ uint64, id uint32) (bool, error) {
	return s.installer.Cloexec(s.pid, id)
}

// Retain 实现 fd.RightsTransfer:以作用域绑定进程取 SCM_RIGHTS 引用
// (M08)。
func (s scopedInstaller) Retain(_ uint64, id uint32) (*fd.Passed, error) {
	transfer, ok := s.installer.(fd.RightsTransfer)
	if !ok {
		return nil, errno.Wrap(errno.NotSupported, "描述符表不支持 SCM_RIGHTS 传递")
	}
	return transfer.Retain(s.pid, id)
}

// InstallPassed 实现 fd.RightsTransfer:把传递引用安装到作用域绑定
// 进程(M08)。
func (s scopedInstaller) InstallPassed(_ uint64, passed *fd.Passed) (uint32, error) {
	transfer, ok := s.installer.(fd.RightsTransfer)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 SCM_RIGHTS 接收")
	}
	return transfer.InstallPassed(s.pid, passed)
}

type scopedIdentity struct {
	source identity.Source
	pid    uint64
}

func (s scopedIdentity) Lookup(ctx context.Context, pid uint64) (identity.Identity, error) {
	if pid != s.pid {
		return identity.Identity{}, errno.Permission
	}
	return s.source.Lookup(ctx, s.pid)
}

type scopedMeter struct {
	base resources.Meter
	pid  uint64
}

func (s scopedMeter) View(ctx context.Context, _ uint64) (resources.View, error) {
	return s.base.View(ctx, s.pid)
}

func (s scopedMeter) Charge(ctx context.Context, _ uint64, work uint64) error {
	return s.base.Charge(ctx, s.pid, work)
}

func (s scopedMeter) ChargeKind(ctx context.Context, _ uint64, kind resources.Kind, amount uint64) error {
	return s.base.ChargeKind(ctx, s.pid, kind, amount)
}

func (s scopedMeter) ReleaseKind(ctx context.Context, _ uint64, kind resources.Kind, amount uint64) error {
	return s.base.ReleaseKind(ctx, s.pid, kind, amount)
}

type chargeFS struct {
	base  fs.System
	scope *scoped
}

func (c chargeFS) WithAccessor(uid, gid uint32) (fs.System, error) {
	accessor, ok := c.base.(fs.AccessorFS)
	if !ok {
		return nil, errno.Wrap(errno.NotSupported, "底层文件系统不支持身份视图")
	}
	base, err := accessor.WithAccessor(uid, gid)
	if err != nil {
		return nil, err
	}
	c.base = base
	return c, nil
}

func (c chargeFS) WithAccessorFunc(owner func() (uint32, uint32)) (fs.System, error) {
	accessor, ok := c.base.(fs.AccessorFuncFS)
	if !ok {
		if static, ok := c.base.(fs.AccessorFS); ok {
			uid, gid := owner()
			base, err := static.WithAccessor(uid, gid)
			if err != nil {
				return nil, err
			}
			c.base = base
			return c, nil
		}
		return nil, errno.Wrap(errno.NotSupported, "底层文件系统不支持动态身份视图")
	}
	base, err := accessor.WithAccessorFunc(owner)
	if err != nil {
		return nil, err
	}
	c.base = base
	return c, nil
}

func (c chargeFS) maskCreate(mode stdfs.FileMode) stdfs.FileMode {
	if c.scope.set.UmaskSource == nil {
		return mode
	}
	umask, err := c.scope.set.UmaskSource.Umask(c.scope.pid)
	if err != nil {
		return mode
	}
	return mode &^ (umask & stdfs.ModePerm)
}

func (c chargeFS) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (fs.File, error) {
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return nil, err
	}
	file, err := c.base.Open(ctx, name, flags, c.maskCreate(mode))
	if err != nil {
		return nil, err
	}
	return &chargeFile{base: file, scope: c.scope}, nil
}

func (c chargeFS) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return nil, err
	}
	return c.base.Stat(ctx, name)
}

func (c chargeFS) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return nil, err
	}
	return c.base.Lstat(ctx, name)
}

func (c chargeFS) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return nil, err
	}
	return c.base.ReadDir(ctx, name)
}

func (c chargeFS) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	if err := c.scope.charge(ctx, costNamespace); err != nil {
		return err
	}
	return c.base.Mkdir(ctx, name, c.maskCreate(mode))
}

func (c chargeFS) Remove(ctx context.Context, name string) error {
	if err := c.scope.charge(ctx, costNamespace); err != nil {
		return err
	}
	return c.base.Remove(ctx, name)
}

func (c chargeFS) Rename(ctx context.Context, from, to string) error {
	if err := c.scope.charge(ctx, costNamespace); err != nil {
		return err
	}
	return c.base.Rename(ctx, from, to)
}

func (c chargeFS) Link(ctx context.Context, from, to string) error {
	if err := c.scope.charge(ctx, costNamespace); err != nil {
		return err
	}
	return c.base.Link(ctx, from, to)
}

// LinkFollowing 转发 linkat(2) AT_SYMLINK_FOLLOW 能力;
// 底层文件系统不支持时明确 ENOTSUP。
func (c chargeFS) LinkFollowing(ctx context.Context, from, to string) error {
	if err := c.scope.charge(ctx, costNamespace); err != nil {
		return err
	}
	follow, ok := c.base.(fs.LinkFollow)
	if !ok {
		return errno.Wrap(errno.NotSupported, "文件系统不支持跟随符号链接建链")
	}
	return follow.LinkFollowing(ctx, from, to)
}

func (c chargeFS) Symlink(ctx context.Context, target, link string) error {
	if err := c.scope.charge(ctx, costNamespace); err != nil {
		return err
	}
	return c.base.Symlink(ctx, target, link)
}

func (c chargeFS) Readlink(ctx context.Context, name string) (string, error) {
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return "", err
	}
	return c.base.Readlink(ctx, name)
}

func (c chargeFS) AccessTime(ctx context.Context, name string) (int64, error) {
	times, ok := c.base.(fs.AccessTimes)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "文件系统不支持读取访问时间")
	}
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return 0, err
	}
	return times.AccessTime(ctx, name)
}

func (c chargeFS) Chmod(ctx context.Context, name string, mode stdfs.FileMode) error {
	modes, ok := c.base.(fs.Modes)
	if !ok {
		return errno.Wrap(errno.NotSupported, "文件系统不支持 chmod")
	}
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return err
	}
	return modes.Chmod(ctx, name, mode)
}

func (c chargeFS) Chown(ctx context.Context, name string, uid, gid int64) error {
	ownership, ok := c.base.(fs.Ownership)
	if !ok {
		return errno.Wrap(errno.NotSupported, "文件系统不支持 chown")
	}
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return err
	}
	return ownership.Chown(ctx, name, uid, gid)
}

func (c chargeFS) Space(ctx context.Context, name string) (fs.Space, error) {
	capacity, ok := c.base.(fs.Capacity)
	if !ok {
		return fs.Space{}, errno.Wrap(errno.NotSupported, "文件系统没有容量能力")
	}
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return fs.Space{}, err
	}
	return capacity.Space(ctx, name)
}

func (c chargeFS) Filesystems(ctx context.Context) ([]fs.Space, error) {
	capacity, ok := c.base.(fs.Capacity)
	if !ok {
		return nil, errno.Wrap(errno.NotSupported, "文件系统没有容量能力")
	}
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return nil, err
	}
	return capacity.Filesystems(ctx)
}

func (c chargeFS) Mkfifo(ctx context.Context, name string, mode stdfs.FileMode) error {
	fifos, ok := c.base.(fs.Fifos)
	if !ok {
		return errno.Wrap(errno.NotSupported, "文件系统不能创建命名管道")
	}
	if err := c.scope.charge(ctx, costNamespace); err != nil {
		return err
	}
	return fifos.Mkfifo(ctx, name, c.maskCreate(mode))
}

type chargeFile struct {
	base  fs.File
	scope *scoped
}

func (f *chargeFile) Read(p []byte) (int, error) { return f.base.Read(p) }

func (f *chargeFile) Write(p []byte) (int, error) {
	buckets := uint64(len(p))/costWriteBucket + 1
	if err := f.scope.charge(context.Background(), buckets); err != nil {
		return 0, err
	}
	return f.base.Write(p)
}

func (f *chargeFile) Seek(offset int64, whence int) (int64, error) {
	return f.base.Seek(offset, whence)
}

func (f *chargeFile) Close() error { return f.base.Close() }

func (f *chargeFile) Stat() (stdfs.FileInfo, error) { return f.base.Stat() }

// IsTerminal 标识底层句柄是否真的具备终端尺寸能力。chargeFile
// 总是实现 Dimensions 以便转发 PTY 查询,若无此标记,普通文件也会
// 被 isatty 误判为终端。
func (f *chargeFile) IsTerminal() bool {
	_, ok := f.base.(interface{ Dimensions() (uint16, uint16) })
	return ok
}

// 终端能力转发(P08):PTY/终端句柄经作用域包装后仍可被 isatty/
// ioctl 识别,否则真实路径恒报 ENOTTY。
func (f *chargeFile) Dimensions() (uint16, uint16) {
	if terminal, ok := f.base.(interface{ Dimensions() (uint16, uint16) }); ok {
		return terminal.Dimensions()
	}
	return 24, 80
}

func (f *chargeFile) State() fd.TerminalState {
	if terminal, ok := f.base.(fd.StatefulTerminal); ok {
		return terminal.State()
	}
	return fd.TerminalState{}
}

func (f *chargeFile) SetState(state fd.TerminalState) error {
	if terminal, ok := f.base.(fd.StatefulTerminal); ok {
		return terminal.SetState(state)
	}
	return errno.Wrap(errno.NotSupported, "句柄不支持终端状态")
}

func (f *chargeFile) PTYNumber() uint32 {
	if numbered, ok := f.base.(interface{ PTYNumber() uint32 }); ok {
		return numbered.PTYNumber()
	}
	return 0
}

func (f *chargeFile) Foreground() uint64 {
	if terminal, ok := f.base.(fd.ForegroundTerminal); ok {
		return terminal.Foreground()
	}
	return 0
}

func (f *chargeFile) SetForeground(pgid uint64) error {
	if terminal, ok := f.base.(fd.ForegroundTerminal); ok {
		return terminal.SetForeground(pgid)
	}
	return errno.NotATerminal
}

func (f *chargeFile) ForegroundSupported() bool {
	terminal, ok := f.base.(fd.ForegroundTerminal)
	return ok && terminal.ForegroundSupported()
}

// GuestPath 转发客体路径查询(I20):inotify 的 fd 侧事件按路径
// 匹配监视;底层句柄无该能力时 ok=false(无事件,尽力而为)。
func (f *chargeFile) GuestPath() (string, bool) {
	pather, ok := f.base.(interface {
		GuestPath() (string, bool)
	})
	if !ok {
		return "", false
	}
	return pather.GuestPath()
}

// LockIdentity 转发锁对象标识(I14):fcntl 文件锁经作用域句柄同样
// 按 inode 键控。
func (f *chargeFile) LockIdentity() uint64 {
	lockable, ok := f.base.(interface {
		LockIdentity() uint64
	})
	if !ok {
		return 0
	}
	return lockable.LockIdentity()
}

// 以下为底层句柄可选能力的转发:ABI 处理器按能力类型断言,包装层
// 不转发会使真实(经 capability 作用域)路径恒 ENOTSUP(L11 同类)。
// 缺能力时保持与底层一致的不实现错误。

func (f *chargeFile) truncateTo(size int64) error {
	truncater, ok := f.base.(interface {
		Truncate(size int64) error
	})
	if !ok {
		return errno.NotSupported
	}
	return truncater.Truncate(size)
}

// Truncate 供 truncater 断言:包装自身实现该能力,按底层有无转发。
func (f *chargeFile) Truncate(size int64) error { return f.truncateTo(size) }

func (f *chargeFile) Sync(fdatasync bool) error {
	syncer, ok := f.base.(interface {
		Sync(bool) error
	})
	if !ok {
		return errno.NotSupported
	}
	return syncer.Sync(fdatasync)
}

func (f *chargeFile) SetTimes(atime, mtime int64) error {
	setter, ok := f.base.(interface {
		SetTimes(atime, mtime int64) error
	})
	if !ok {
		return errno.NotSupported
	}
	return setter.SetTimes(atime, mtime)
}

func (f *chargeFile) SetTimesFollow(atime, mtime int64) error {
	setter, ok := f.base.(interface {
		SetTimesFollow(atime, mtime int64) error
	})
	if !ok {
		return errno.NotSupported
	}
	return setter.SetTimesFollow(atime, mtime)
}

func (f *chargeFile) Readdir(limit int) ([]stdfs.DirEntry, error) {
	reader, ok := f.base.(interface {
		Readdir(limit int) ([]stdfs.DirEntry, error)
	})
	if !ok {
		return nil, errno.NotSupported
	}
	return reader.Readdir(limit)
}

func (f *chargeFile) StatusFlags() (uint32, error) {
	flagger, ok := f.base.(interface {
		StatusFlags() (uint32, error)
	})
	if !ok {
		return 0, errno.NotSupported
	}
	return flagger.StatusFlags()
}

func (f *chargeFile) SetStatusFlags(flags uint32) error {
	setter, ok := f.base.(interface {
		SetStatusFlags(flags uint32) error
	})
	if !ok {
		return errno.NotSupported
	}
	return setter.SetStatusFlags(flags)
}

func (f *chargeFile) Space(ctx context.Context) (fs.Space, error) {
	spacer, ok := f.base.(interface {
		Space(ctx context.Context) (fs.Space, error)
	})
	if !ok {
		return fs.Space{}, errno.NotSupported
	}
	return spacer.Space(ctx)
}

func (f *chargeFile) ResolveGuestPath(name string) (string, error) {
	resolver, ok := f.base.(interface {
		ResolveGuestPath(name string) (string, error)
	})
	if !ok {
		return "", errno.NotDirectory
	}
	return resolver.ResolveGuestPath(name)
}

// randomAccess 是底层句柄的随机访问能力(pread/pwrite 语义与
// 打开访问模式查询);文件映射(E08/E09)经此直达底层数据。
type randomAccess interface {
	ReadAt(p []byte, off int64) (int, error)

	WriteAt(p []byte, off int64) (int, error)

	Readable() bool

	Writable() bool
}

func (f *chargeFile) random() (randomAccess, bool) {
	random, ok := f.base.(randomAccess)
	return random, ok
}

func (f *chargeFile) ReadAt(p []byte, off int64) (int, error) {
	random, ok := f.random()
	if !ok {
		return 0, errno.Seek
	}
	return random.ReadAt(p, off)
}

func (f *chargeFile) WriteAt(p []byte, off int64) (int, error) {
	random, ok := f.random()
	if !ok {
		return 0, errno.Seek
	}
	// 计费口径与 Write 一致:按写入桶数。
	buckets := uint64(len(p))/costWriteBucket + 1
	if err := f.scope.charge(context.Background(), buckets); err != nil {
		return 0, err
	}
	return random.WriteAt(p, off)
}

func (f *chargeFile) Readable() bool {
	random, ok := f.random()
	return ok && random.Readable()
}

func (f *chargeFile) Writable() bool {
	random, ok := f.random()
	return ok && random.Writable()
}

func (c chargeFS) SetTimes(ctx context.Context, name string, atime, mtime int64) error {
	timestamps, ok := c.base.(fs.Timestamps)
	if !ok {
		return errno.NotImplemented
	}
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return err
	}
	return timestamps.SetTimes(ctx, name, atime, mtime)
}

func (c chargeFS) SetTimesFollow(ctx context.Context, name string, atime, mtime int64, follow bool) error {
	timestamps, ok := c.base.(fs.LinkTimestamps)
	if !ok {
		return errno.Wrap(errno.NotSupported, "文件系统不支持按链接方式更新时间")
	}
	if err := c.scope.charge(ctx, costMeta); err != nil {
		return err
	}
	return timestamps.SetTimesFollow(ctx, name, atime, mtime, follow)
}

func (c chargeFS) Snapshot(ctx context.Context) (fs.Snapshotted, error) {
	snapshooter, ok := c.base.(fs.Snapshotter)
	if !ok {
		return nil, errno.NotImplemented
	}
	if err := c.scope.charge(ctx, costNamespace); err != nil {
		return nil, err
	}
	return snapshooter.Snapshot(ctx)
}

var _ fd.Installer = scopedInstaller{}

var _ fd.RightsTransfer = scopedInstaller{}

var _ Spaces = scopedSpaces{}
