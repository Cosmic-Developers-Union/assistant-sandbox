package filesystem

import (
	"context"
	"io/fs"
	"path"
	"strings"

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
	return f.open(p, int(flags), mode)
}

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
