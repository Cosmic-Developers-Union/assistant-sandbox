package common

import (
	"context"
	"io"
	stdfs "io/fs"
	"math"
	pathpkg "path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/eventfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
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

func (p ProcessDescriptors) Truncate(ctx context.Context, descriptor uint64, size int64) error {
	if p.Table == nil {
		return errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	if descriptor > math.MaxUint32 {
		return errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	stream, err := p.Table.Get(ctx, p.PID, uint32(descriptor))
	if err != nil {
		return err
	}
	type truncater interface {
		Truncate(int64) error
	}
	if t, ok := stream.(truncater); ok {
		return t.Truncate(size)
	}
	return errno.Wrap(errno.NotSupported, "描述符不支持截断")
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
	case 1: // F_GETFD
		cloexec, err := table.Cloexec(p.PID, uint32(descriptor))
		if err != nil {
			return 0, err
		}
		if cloexec {
			return 1, nil // FD_CLOEXEC
		}
		return 0, nil
	case 2: // F_SETFD
		if err := table.SetCloexec(p.PID, uint32(descriptor), arg&1 != 0); err != nil {
			return 0, err
		}
		return 0, nil
	default:
		return 0, errno.Wrap(errno.NotSupported, "fcntl 命令不被支持")
	}
}

func (p ProcessDescriptors) Pipe(ctx context.Context) (uint64, uint64, error) {
	if p.Table == nil {
		return 0, 0, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	table, ok := p.Table.(*fd.Table)
	if !ok {
		return 0, 0, errno.Wrap(errno.NotSupported, "描述符表不支持 pipe")
	}
	r, w, err := table.Pipe(p.PID)
	if err != nil {
		return 0, 0, err
	}
	return uint64(r), uint64(w), nil
}

func (p ProcessDescriptors) EventFD(ctx context.Context, initialValue, flags uint64) (uint64, error) {
	if p.Table == nil {
		return 0, errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	table, ok := p.Table.(*fd.Table)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 eventfd")
	}
	descriptor, err := table.EventFD(p.PID, initialValue, eventfd.Options{
		Semaphore: flags&efdSemaphore != 0,
		NonBlock:  flags&efdNonBlock != 0,
	})
	if err != nil {
		return 0, err
	}
	if flags&efdCloexec != 0 {
		if err := table.SetCloexec(p.PID, descriptor, true); err != nil {
			return uint64(descriptor), err
		}
	}
	return uint64(descriptor), nil
}

func (p ProcessDescriptors) Dup2(ctx context.Context, oldfd, newfd uint64, cloexec bool) error {
	if p.Table == nil {
		return errno.Wrap(errno.NotImplemented, "描述符端口未装配")
	}
	if oldfd > math.MaxUint32 || newfd > math.MaxUint32 {
		return errno.Wrap(errno.BadHandle, "描述符编号超出表示范围")
	}
	table, ok := p.Table.(*fd.Table)
	if !ok {
		return errno.Wrap(errno.NotSupported, "描述符表不支持 dup")
	}
	if err := table.Dup(ctx, p.PID, uint32(oldfd), uint32(newfd)); err != nil {
		return err
	}
	if cloexec {
		_ = table.SetCloexec(p.PID, uint32(newfd), true)
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

func (p ProcessFiles) Mkdir(ctx context.Context, path string, mode uint32) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target := path
	if !strings.HasPrefix(target, "/") {
		target = pathpkg.Join(p.Dir, target)
	}
	return p.System.Mkdir(ctx, target, stdfs.FileMode(mode))
}

func (p ProcessFiles) Remove(ctx context.Context, path string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target := path
	if !strings.HasPrefix(target, "/") {
		target = pathpkg.Join(p.Dir, target)
	}
	return p.System.Remove(ctx, target)
}

func (p ProcessFiles) Symlink(ctx context.Context, target, linkpath string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	if !strings.HasPrefix(linkpath, "/") {
		linkpath = pathpkg.Join(p.Dir, linkpath)
	}
	return p.System.Symlink(ctx, target, linkpath)
}

func (p ProcessFiles) Link(ctx context.Context, oldpath, newpath string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	if !strings.HasPrefix(oldpath, "/") {
		oldpath = pathpkg.Join(p.Dir, oldpath)
	}
	if !strings.HasPrefix(newpath, "/") {
		newpath = pathpkg.Join(p.Dir, newpath)
	}
	return p.System.Link(ctx, oldpath, newpath)
}

func (p ProcessFiles) ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error) {
	if p.System == nil {
		return nil, errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target := path
	if !strings.HasPrefix(target, "/") {
		target = pathpkg.Join(p.Dir, target)
	}
	return p.System.ReadDir(ctx, target)
}

func (p ProcessFiles) Rename(ctx context.Context, oldpath, newpath string) error {
	if p.System == nil {
		return errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	if !strings.HasPrefix(oldpath, "/") {
		oldpath = pathpkg.Join(p.Dir, oldpath)
	}
	if !strings.HasPrefix(newpath, "/") {
		newpath = pathpkg.Join(p.Dir, newpath)
	}
	return p.System.Rename(ctx, oldpath, newpath)
}

func (p ProcessFiles) Readlink(ctx context.Context, path string) (string, error) {
	if p.System == nil {
		return "", errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target := path
	if !strings.HasPrefix(target, "/") {
		target = pathpkg.Join(p.Dir, target)
	}
	return p.System.Readlink(ctx, target)
}

func (p ProcessFiles) Stat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	if p.System == nil {
		return nil, errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	target := path
	if !strings.HasPrefix(target, "/") {
		target = pathpkg.Join(p.Dir, target)
	}
	return p.System.Stat(ctx, target)
}

func (p ProcessFiles) Open(ctx context.Context, request OpenRequest) (uint64, error) {
	if p.System == nil || p.Store == nil {
		return 0, errno.Wrap(errno.NotImplemented, "文件端口未装配")
	}
	if err := request.Flags.Validate(); err != nil {
		return 0, err
	}
	path := request.Path
	if !strings.HasPrefix(path, "/") {

		path = pathpkg.Join(p.Dir, path)
	}
	file, err := p.System.Open(ctx, path, rawOpenFlags(request.Flags), stdfs.FileMode(request.Perm))
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
