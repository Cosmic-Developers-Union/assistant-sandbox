package common

import (
	"context"
	"io"
	stdfs "io/fs"
	"math"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/eventfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signalfd"
)

type ProcessDescriptors struct {
	Table fd.Descriptors

	PID uint64
}

func (p ProcessDescriptors) Read(ctx context.Context, descriptor uint64, buffer []byte) (int, error) {
	stream, err := p.stream(ctx, descriptor)
	if err != nil {
		return 0, err
	}
	return stream.Read(buffer)
}

func (p ProcessDescriptors) Write(ctx context.Context, descriptor uint64, buffer []byte) (int, error) {
	stream, err := p.stream(ctx, descriptor)
	if err != nil {
		return 0, err
	}
	return stream.Write(buffer)
}

func (p ProcessDescriptors) Close(ctx context.Context, descriptor uint64) error {
	if p.Table == nil {
		return errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	if descriptor > math.MaxUint32 {
		return errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	return p.Table.Close(ctx, p.PID, uint32(descriptor))
}

func (p ProcessDescriptors) Seek(ctx context.Context, descriptor uint64, offset int64, whence SeekWhence) (int64, error) {
	stream, err := p.stream(ctx, descriptor)
	if err != nil {
		return 0, err
	}
	seeker, ok := stream.(io.Seeker)
	if !ok {
		return 0, errno.Wrap(errno.Seek, "描述符不支持定位")
	}
	return seeker.Seek(offset, int(whence))
}

func (p ProcessDescriptors) Get(ctx context.Context, descriptor uint64) (any, error) {
	if p.Table == nil {
		return nil, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	if descriptor > math.MaxUint32 {
		return nil, errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	return p.Table.Get(ctx, p.PID, uint32(descriptor))
}

// asm-generic 的 fcntl 命令与文件状态标志。
const (
	fDupFD        = 0
	fGetFD        = 1
	fSetFD        = 2
	fGetFL        = 3
	fSetFL        = 4
	fDupFDCloexec = 1030

	oAppend   = 0o2000
	oNonBlock = 0o4000
)

// statusFlagger 是句柄的可选能力:报告打开状态标志(F_GETFL)。
type statusFlagger interface {
	StatusFlags() (uint32, error)
}

// statusFlagSetter 是句柄的可选能力:应用 F_SETFL。
type statusFlagSetter interface {
	SetStatusFlags(flags uint32) error
}

func (p ProcessDescriptors) Fcntl(ctx context.Context, descriptor uint64, cmd uint64, arg uint64) (uint64, error) {
	if p.Table == nil {
		return 0, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	if descriptor > math.MaxUint32 {
		return 0, errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	table, ok := p.Table.(*fd.Table)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 fcntl")
	}
	switch cmd {
	case fGetFD: // F_GETFD
		cloexec, err := table.Cloexec(p.PID, uint32(descriptor))
		if err != nil {
			return 0, err
		}
		if cloexec {
			return 1, nil // FD_CLOEXEC
		}
		return 0, nil
	case fSetFD: // F_SETFD
		if err := table.SetCloexec(p.PID, uint32(descriptor), arg&1 != 0); err != nil {
			return 0, err
		}
		return 0, nil
	case fDupFD: // F_DUPFD
		return p.dupAt(ctx, table, descriptor, arg)
	case fDupFDCloexec: // F_DUPFD_CLOEXEC
		id, err := p.dupAt(ctx, table, descriptor, arg)
		if err != nil {
			return 0, err
		}
		if err := table.SetCloexec(p.PID, uint32(id), true); err != nil {
			return 0, err
		}
		return id, nil
	case fGetFL: // F_GETFL
		if p.Table == nil {
			return 0, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
		}
		handle, err := p.Table.Get(ctx, p.PID, uint32(descriptor))
		if err != nil {
			return 0, err
		}
		flagger, ok := handle.(statusFlagger)
		if !ok {
			return 0, errno.Wrap(errno.NotSupported, "描述符不支持状态标志查询")
		}
		flags, err := flagger.StatusFlags()
		return uint64(flags), err
	case fSetFL: // F_SETFL
		handle, err := p.Table.Get(ctx, p.PID, uint32(descriptor))
		if err != nil {
			return 0, err
		}
		setter, ok := handle.(statusFlagSetter)
		if !ok {
			// 管道/eventfd 的非阻塞切换未纳入兼容配置,明确拒绝。
			return 0, errno.Wrap(errno.NotSupported, "描述符不支持状态标志设置")
		}
		if err := setter.SetStatusFlags(uint32(arg)); err != nil {
			return 0, err
		}
		return 0, nil
	default:
		return 0, errno.Wrap(errno.NotSupported, "fcntl 命令不被支持")
	}
}

func (p ProcessDescriptors) dupAt(ctx context.Context, creator fd.Creator, descriptor, minID uint64) (uint64, error) {
	if minID > math.MaxUint32 {
		return 0, errno.Wrap(errno.Invalid, "起始描述符超出表示范围")
	}
	id, err := creator.DupAt(ctx, p.PID, descriptor, minID)
	return uint64(id), err
}

func (p ProcessDescriptors) DupFrom(ctx context.Context, descriptor uint64) (uint64, error) {
	if p.Table == nil {
		return 0, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	if descriptor > math.MaxUint32 {
		return 0, errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	creator, ok := p.Table.(fd.Creator)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 dup")
	}
	return p.dupAt(ctx, creator, descriptor, 0)
}

func (p ProcessDescriptors) Pipe(ctx context.Context) (uint64, uint64, error) {
	if p.Table == nil {
		return 0, 0, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	creator, ok := p.Table.(fd.Creator)
	if !ok {
		return 0, 0, errno.Wrap(errno.NotSupported, "描述符表不支持 pipe")
	}
	r, w, err := creator.Pipe(p.PID)
	if err != nil {
		return 0, 0, err
	}
	return uint64(r), uint64(w), nil
}

func (p ProcessDescriptors) EventFD(ctx context.Context, initialValue, flags uint64) (uint64, error) {
	if p.Table == nil {
		return 0, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	creator, ok := p.Table.(fd.Creator)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 eventfd")
	}
	descriptor, err := creator.EventFD(p.PID, initialValue, eventfd.Options{
		Semaphore: flags&efdSemaphore != 0,
		NonBlock:  flags&efdNonBlock != 0,
	})
	if err != nil {
		return 0, err
	}
	if flags&efdCloexec != 0 {
		if installer, ok := p.Table.(fd.Installer); ok {
			if err := installer.SetCloexec(p.PID, descriptor, true); err != nil {
				return uint64(descriptor), err
			}
		}
	}
	return uint64(descriptor), nil
}

func (p ProcessDescriptors) SignalFD(ctx context.Context, mask, flags uint64) (uint64, error) {
	if p.Table == nil {
		return 0, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	creator, ok := p.Table.(fd.Creator)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 signalfd")
	}
	descriptor, err := creator.SignalFD(p.PID, mask, signalfd.Options{
		NonBlock: flags&sfdNonBlock != 0,
	})
	if err != nil {
		return 0, err
	}
	if flags&sfdCloexec != 0 {
		if installer, ok := p.Table.(fd.Installer); ok {
			if err := installer.SetCloexec(p.PID, descriptor, true); err != nil {
				return uint64(descriptor), err
			}
		}
	}
	return uint64(descriptor), nil
}

func (p ProcessDescriptors) UpdateSignalFD(ctx context.Context, descriptor, mask, flags uint64) error {
	if p.Table == nil {
		return errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	creator, ok := p.Table.(fd.Creator)
	if !ok {
		return errno.Wrap(errno.NotSupported, "描述符表不支持 signalfd")
	}
	if descriptor > math.MaxUint32 {
		return errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	handle, err := creator.SignalFDAt(p.PID, uint32(descriptor))
	if err != nil {
		return err
	}
	if err := handle.SetMask(mask); err != nil {
		return err
	}
	handle.SetNonBlock(flags&sfdNonBlock != 0)
	return nil
}

func (p ProcessDescriptors) Dup2(ctx context.Context, oldfd, newfd uint64, cloexec bool) error {
	if p.Table == nil {
		return errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	if oldfd > math.MaxUint32 || newfd > math.MaxUint32 {
		return errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	if err := p.Table.Dup(ctx, p.PID, uint32(oldfd), uint32(newfd)); err != nil {
		return err
	}
	if cloexec {
		if installer, ok := p.Table.(fd.Installer); ok {
			_ = installer.SetCloexec(p.PID, uint32(newfd), true)
		}
	}
	return nil
}

func (p ProcessDescriptors) stream(ctx context.Context, descriptor uint64) (fd.Stream, error) {
	if p.Table == nil {
		return nil, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	if descriptor > math.MaxUint32 {
		return nil, errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	return p.Table.Get(ctx, p.PID, uint32(descriptor))
}

type DescriptorStore interface {
	InstallNext(pid uint64, handle fd.Stream) (uint32, error)

	SetCloexec(pid uint64, id uint32, cloexec bool) error

	Close(ctx context.Context, pid uint64, id uint32) error
}

type ProcessFiles struct {
	System fs.System

	Store DescriptorStore

	PID uint64

	Dir string
}

// resolver 返回按工作目录解析客体路径的统一入口(I01)。
func (p ProcessFiles) resolver() Resolver { return NewResolver(p.Dir) }

func (p ProcessFiles) Mkdir(ctx context.Context, path string, mode uint32) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target, err := p.resolver().Resolve(path)
	if err != nil {
		return err
	}
	return p.System.Mkdir(ctx, target, stdfs.FileMode(mode))
}

func (p ProcessFiles) Remove(ctx context.Context, path string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target, err := p.resolver().Resolve(path)
	if err != nil {
		return err
	}
	return p.System.Remove(ctx, target)
}

func (p ProcessFiles) Symlink(ctx context.Context, target, linkpath string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	linkpath, err := p.resolver().Resolve(linkpath)
	if err != nil {
		return err
	}
	return p.System.Symlink(ctx, target, linkpath)
}

func (p ProcessFiles) Link(ctx context.Context, oldpath, newpath string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	oldpath, newpath, err := p.resolver().ResolvePair(oldpath, newpath)
	if err != nil {
		return err
	}
	return p.System.Link(ctx, oldpath, newpath)
}

// LinkFollowing 转发 linkat(2) AT_SYMLINK_FOLLOW 能力;
// 底层文件系统不支持时明确 ENOTSUP。
func (p ProcessFiles) LinkFollowing(ctx context.Context, oldpath, newpath string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	oldpath, newpath, err := p.resolver().ResolvePair(oldpath, newpath)
	if err != nil {
		return err
	}
	follow, ok := p.System.(fs.LinkFollow)
	if !ok {
		return errno.Wrap(errno.NotSupported, "文件系统不支持跟随符号链接建链")
	}
	return follow.LinkFollowing(ctx, oldpath, newpath)
}

func (p ProcessFiles) ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error) {
	if p.System == nil {
		return nil, errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target, err := p.resolver().Resolve(path)
	if err != nil {
		return nil, err
	}
	return p.System.ReadDir(ctx, target)
}

func (p ProcessFiles) Rename(ctx context.Context, oldpath, newpath string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	oldpath, newpath, err := p.resolver().ResolvePair(oldpath, newpath)
	if err != nil {
		return err
	}
	return p.System.Rename(ctx, oldpath, newpath)
}

func (p ProcessFiles) Statfs(ctx context.Context, path string) (StatfsInfo, error) {
	if p.System == nil {
		return StatfsInfo{}, errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target, err := p.resolver().Resolve(path)
	if err != nil {
		return StatfsInfo{}, err
	}
	capacity, ok := p.System.(fs.Capacity)
	if !ok {
		return StatfsInfo{}, errno.Wrap(errno.NotSupported, "文件系统不支持容量查询")
	}
	space, err := capacity.Space(ctx, target)
	if err != nil {
		return StatfsInfo{}, err
	}
	return StatfsInfo{Bytes: space.Bytes, Inodes: space.Inodes}, nil
}

func (p ProcessFiles) SetTimes(ctx context.Context, path string, atime, mtime int64, follow bool) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target, err := p.resolver().Resolve(path)
	if err != nil {
		return err
	}
	if setter, ok := p.System.(fs.LinkTimestamps); ok {
		return setter.SetTimesFollow(ctx, target, atime, mtime, follow)
	}
	if follow {
		if setter, ok := p.System.(fs.Timestamps); ok {
			return setter.SetTimes(ctx, target, atime, mtime)
		}
	}
	return errno.Wrap(errno.NotSupported, "文件系统不支持时间更新")
}

func (p ProcessFiles) Readlink(ctx context.Context, path string) (string, error) {
	if p.System == nil {
		return "", errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target, err := p.resolver().Resolve(path)
	if err != nil {
		return "", err
	}
	return p.System.Readlink(ctx, target)
}

func (p ProcessFiles) Stat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	if p.System == nil {
		return nil, errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target, err := p.resolver().Resolve(path)
	if err != nil {
		return nil, err
	}
	return p.System.Stat(ctx, target)
}

func (p ProcessFiles) Lstat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	if p.System == nil {
		return nil, errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target, err := p.resolver().Resolve(path)
	if err != nil {
		return nil, err
	}
	return p.System.Lstat(ctx, target)
}

func (p ProcessFiles) Open(ctx context.Context, request OpenRequest) (uint64, error) {
	if p.System == nil || p.Store == nil {
		return 0, errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	if err := request.Flags.Validate(); err != nil {
		return 0, err
	}
	target, err := p.resolver().Resolve(request.Path)
	if err != nil {
		return 0, err
	}
	file, err := p.System.Open(ctx, target, rawOpenFlags(request.Flags), stdfs.FileMode(request.Perm))
	if err != nil {
		return 0, err
	}
	id, err := p.Store.InstallNext(p.PID, file)
	if err != nil {

		_ = file.Close()
		return 0, err
	}
	if request.Flags.CloseOnExec {
		if err := p.Store.SetCloexec(p.PID, id, true); err != nil {
			_ = p.Store.Close(ctx, p.PID, id)
			return 0, err
		}
	}
	return uint64(id), nil
}

func rawOpenFlags(flags OpenFlags) uint32 {
	raw := uint32(flags.Mode)
	if flags.Create {
		raw |= fs.Create
	}
	if flags.Exclusive {
		raw |= fs.Exclusive
	}
	if flags.Truncate {
		raw |= fs.Truncate
	}
	if flags.Append {
		raw |= fs.Append
	}
	return raw
}

var (
	_ Descriptors     = ProcessDescriptors{}
	_ Files           = ProcessFiles{}
	_ DescriptorStore = (*fd.Table)(nil)

	_ Clock = clock.Source(nil)
)
