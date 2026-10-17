package readonly

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type FS struct{ base vmfs.System }

func New(base vmfs.System) (*FS, error) {
	if base == nil {
		return nil, errno.Invalid
	}
	return &FS{base: base}, nil
}

func (f *FS) Open(ctx context.Context, p string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if flags&0b11 != 0 || flags&(vmfs.Create|vmfs.Truncate|vmfs.Append) != 0 {
		return nil, errno.ReadOnlyFS
	}
	return f.base.Open(ctx, p, flags, mode)
}

func (f *FS) Stat(ctx context.Context, p string) (stdfs.FileInfo, error) {
	return f.base.Stat(ctx, p)
}

func (f *FS) Lstat(ctx context.Context, p string) (stdfs.FileInfo, error) {
	return f.base.Lstat(ctx, p)
}

func (f *FS) ReadDir(ctx context.Context, p string) ([]stdfs.DirEntry, error) {
	return f.base.ReadDir(ctx, p)
}

func (f *FS) Mkdir(ctx context.Context, p string, mode stdfs.FileMode) error {
	return errno.ReadOnlyFS
}

func (f *FS) Remove(ctx context.Context, p string) error {
	return errno.ReadOnlyFS
}

func (f *FS) Rename(ctx context.Context, from, to string) error {
	return errno.ReadOnlyFS
}

func (f *FS) Link(ctx context.Context, from, to string) error {
	return errno.ReadOnlyFS
}

func (f *FS) Symlink(ctx context.Context, target, linkPath string) error {
	return errno.ReadOnlyFS
}

func (f *FS) Readlink(ctx context.Context, p string) (string, error) {
	return f.base.Readlink(ctx, p)
}

func (f *FS) SetTimes(ctx context.Context, p string, atime, mtime int64) error {
	return errno.ReadOnlyFS
}
