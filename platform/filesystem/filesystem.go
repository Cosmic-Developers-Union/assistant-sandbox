package filesystem

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type Filesystem struct {
	base string
	// mountRelative 为真时把客体绝对路径(/file)解释为相对挂载点,
	// 供 mountfs 把宿主目录挂到任意客体目录;缺省保持宿主路径语义。
	mountRelative bool
}

func NewFilesystem(base string) *Filesystem { return &Filesystem{base: base} }

// NewMountFilesystem 创建按客体绝对路径解释的挂载视图。
func NewMountFilesystem(base string) *Filesystem {
	return &Filesystem{base: base, mountRelative: true}
}
func (f *Filesystem) resolve(name string) (string, error) {
	if strings.ContainsAny(name, "\x00\\") {
		return "", &fs.PathError{Op: "resolve", Path: name, Err: fs.ErrPermission}
	}
	name = path.Clean(name)
	if f.mountRelative {
		// 客体绝对路径(/file)按挂载点相对路径解释,再落到宿主 base 下。
		name = path.Join(f.base, strings.TrimPrefix(name, "/"))
	} else if !path.IsAbs(name) {
		name = path.Join(f.base, name)
	}
	if f.base != "/" && name != f.base && !strings.HasPrefix(name, f.base+"/") {
		return "", &fs.PathError{Op: "resolve", Path: name, Err: fs.ErrPermission}
	}
	return name, nil
}

func (f *Filesystem) Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	file, err := f.open(p, int(flags), mode)
	if err != nil {
		return nil, err
	}
	return wrapRandomFile(file, flags), nil
}

// randomFile 为原生宿主文件补上 mmap 所需的随机访问与打开模式
// 查询(os.File 有 ReadAt/WriteAt,但没有模式自省接口)。
type randomFile struct {
	vmfs.File
	readable bool
	writable bool
	append   bool
	nonBlock bool
}

func wrapRandomFile(file vmfs.File, flags uint32) vmfs.File {
	access := flags & 3
	return &randomFile{
		File:     file,
		readable: access != vmfs.WriteOnly,
		writable: access != vmfs.ReadOnly,
		append:   flags&vmfs.Append != 0,
		nonBlock: flags&vmfs.NonBlock != 0,
	}
}

func (f *randomFile) ReadAt(buffer []byte, offset int64) (int, error) {
	if !f.readable {
		return 0, errno.BadHandle
	}
	reader, ok := f.File.(io.ReaderAt)
	if !ok {
		return 0, errno.Seek
	}
	return reader.ReadAt(buffer, offset)
}

func (f *randomFile) Write(buffer []byte) (int, error) {
	if f.append {
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return 0, err
		}
	}
	return f.File.Write(buffer)
}

func (f *randomFile) WriteAt(buffer []byte, offset int64) (int, error) {
	if !f.writable {
		return 0, errno.BadHandle
	}
	writer, ok := f.File.(io.WriterAt)
	if !ok {
		return 0, errno.Seek
	}
	return writer.WriteAt(buffer, offset)
}

func (f *randomFile) Truncate(size int64) error {
	truncater, ok := f.File.(interface{ Truncate(size int64) error })
	if !ok {
		return errno.NotSupported
	}
	return truncater.Truncate(size)
}

func (f *randomFile) Sync(bool) error {
	syncer, ok := f.File.(interface{ Sync() error })
	if !ok {
		return errno.NotSupported
	}
	return syncer.Sync()
}

func (f *randomFile) StatusFlags() (uint32, error) {
	flags := f.accessMode()
	if f.append {
		flags |= 0o2000
	}
	if f.nonBlock {
		flags |= 0o4000
	}
	return flags, nil
}

func (f *randomFile) SetStatusFlags(flags uint32) error {
	f.append = flags&0o2000 != 0
	f.nonBlock = flags&0o4000 != 0
	return nil
}

func (f *randomFile) accessMode() uint32 {
	switch {
	case f.readable && f.writable:
		return 2
	case f.writable:
		return 1
	default:
		return 0
	}
}

func (f *randomFile) Space(ctx context.Context) (vmfs.Space, error) {
	spacer, ok := f.File.(interface {
		Space(context.Context) (vmfs.Space, error)
	})
	if !ok {
		return vmfs.Space{}, errno.NotSupported
	}
	return spacer.Space(ctx)
}

func (f *randomFile) Readdir(limit int) ([]fs.DirEntry, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errno.NotDirectory
	}
	reader, ok := f.File.(interface {
		ReadDir(limit int) ([]fs.DirEntry, error)
	})
	if !ok {
		return nil, errno.NotSupported
	}
	entries, err := reader.ReadDir(limit)
	if errors.Is(err, io.EOF) && len(entries) > 0 {
		return entries, nil
	}
	return entries, err
}

func (f *randomFile) Readable() bool { return f.readable }

func (f *randomFile) Writable() bool { return f.writable }

func (f *Filesystem) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	return f.stat(p)
}

func (f *Filesystem) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	return f.readDir(p)
}

func (f *Filesystem) Mkdir(ctx context.Context, name string, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := f.resolve(name)
	if err != nil {
		return err
	}
	if p == f.base {
		return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrExist}
	}
	return f.mkdir(p, mode)
}

func (f *Filesystem) Remove(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := f.resolve(name)
	if err != nil {
		return err
	}
	return f.remove(p)
}

func (f *Filesystem) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a, err := f.resolve(from)
	if err != nil {
		return err
	}
	b, err := f.resolve(to)
	if err != nil {
		return err
	}
	return f.rename(a, b)
}

func (f *Filesystem) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	return f.lstat(p)
}

func (f *Filesystem) Link(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a, err := f.resolve(from)
	if err != nil {
		return err
	}
	b, err := f.resolve(to)
	if err != nil {
		return err
	}
	return f.link(a, b)
}

func (f *Filesystem) Symlink(ctx context.Context, target, link string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := f.resolve(link)
	if err != nil {
		return err
	}
	return f.symlink(target, p)
}

func (f *Filesystem) Readlink(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p, err := f.resolve(name)
	if err != nil {
		return "", err
	}
	return f.readlink(p)
}

func (f *Filesystem) SetTimes(ctx context.Context, name string, access, modification int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := f.resolve(name)
	if err != nil {
		return err
	}
	return f.setTimes(p, access, modification)
}
