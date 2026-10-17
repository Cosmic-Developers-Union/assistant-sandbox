//go:build !wasip1

package filesystem

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func NewWorkspace(dir string) *Filesystem { return NewFilesystem(dir) }

func (f *Filesystem) relative(name string) string {
	return filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(name, f.base), "/"))
}

func (f *Filesystem) setTimes(name string, access, modification int64) error {
	_, err := withRoot(f, func(root *os.Root) (struct{}, error) {
		return struct{}{}, root.Chtimes(f.relative(name), time.Unix(0, access), time.Unix(0, modification))
	})
	return err
}
func (f *Filesystem) root() (*os.Root, error) { return os.OpenRoot(filepath.FromSlash(f.base)) }
func (f *Filesystem) open(name string, flags int, mode fs.FileMode) (vmfs.File, error) {
	return withRoot(f, func(root *os.Root) (vmfs.File, error) {
		relative := f.relative(name)
		if relative == "" {
			relative = "."
		}
		return root.OpenFile(relative, flags, mode)
	})
}

func (f *Filesystem) SameFile(a, b fs.FileInfo) bool { return os.SameFile(a, b) }

func (f *Filesystem) stat(name string) (fs.FileInfo, error) {
	return withRoot(f, func(root *os.Root) (fs.FileInfo, error) {
		relative := f.relative(name)
		if relative == "" {
			relative = "."
		}
		return root.Stat(relative)
	})
}

func (f *Filesystem) lstat(name string) (fs.FileInfo, error) {
	return withRoot(f, func(root *os.Root) (fs.FileInfo, error) {
		relative := f.relative(name)
		if relative == "" {
			relative = "."
		}
		return root.Lstat(relative)
	})
}

func (f *Filesystem) link(a, b string) error {
	_, err := withRoot(f, func(root *os.Root) (struct{}, error) {
		return struct{}{}, root.Link(f.relative(a), f.relative(b))
	})
	return err
}

func (f *Filesystem) symlink(target, link string) error {
	_, err := withRoot(f, func(root *os.Root) (struct{}, error) {
		return struct{}{}, root.Symlink(target, f.relative(link))
	})
	return err
}

func (f *Filesystem) readlink(name string) (string, error) {
	return withRoot(f, func(root *os.Root) (string, error) { return root.Readlink(f.relative(name)) })
}
func (f *Filesystem) readDir(name string) ([]fs.DirEntry, error) {
	return withRoot(f, func(root *os.Root) ([]fs.DirEntry, error) {
		relative := f.relative(name)
		if relative == "" {
			relative = "."
		}
		return fs.ReadDir(root.FS(), relative)
	})
}
func (f *Filesystem) mkdir(name string, mode fs.FileMode) error {
	_, err := withRoot(f, func(root *os.Root) (struct{}, error) { return struct{}{}, root.Mkdir(f.relative(name), mode) })
	return err
}
func (f *Filesystem) remove(name string) error {
	_, err := withRoot(f, func(root *os.Root) (struct{}, error) { return struct{}{}, root.Remove(f.relative(name)) })
	return err
}
func (f *Filesystem) rename(a, b string) error {
	_, err := withRoot(f, func(root *os.Root) (struct{}, error) { return struct{}{}, root.Rename(f.relative(a), f.relative(b)) })
	return err
}

func withRoot[T any](f *Filesystem, action func(*os.Root) (T, error)) (result T, resultErr error) {
	root, err := f.root()
	if err != nil {
		return result, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	return action(root)
}
