package capability

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/bridge"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/image"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/ipc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/language"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/storage"
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

var _ Spaces = scopedSpaces{}
