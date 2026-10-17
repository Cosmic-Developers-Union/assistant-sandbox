//go:build wasip1

package filesystem

import (
	"io/fs"
	"os"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func NewWorkspace(dir string) *Filesystem {
	if dir == "/work" {
		return NewFilesystem("/")
	}
	return NewFilesystem(dir)
}

func (f *Filesystem) open(name string, flags int, mode fs.FileMode) (vmfs.File, error) {
	return os.OpenFile(name, flags, mode)
}

func (f *Filesystem) SameFile(a, b fs.FileInfo) bool { return os.SameFile(a, b) }

func (f *Filesystem) stat(name string) (fs.FileInfo, error)      { return os.Stat(name) }
func (f *Filesystem) readDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (f *Filesystem) mkdir(name string, mode fs.FileMode) error  { return os.Mkdir(name, mode) }
func (f *Filesystem) remove(name string) error                   { return os.Remove(name) }
func (f *Filesystem) rename(a, b string) error                   { return os.Rename(a, b) }
func (f *Filesystem) lstat(name string) (fs.FileInfo, error)     { return os.Lstat(name) }
func (f *Filesystem) link(a, b string) error                     { return os.Link(a, b) }
func (f *Filesystem) symlink(target, link string) error          { return os.Symlink(target, link) }
func (f *Filesystem) readlink(name string) (string, error)       { return os.Readlink(name) }
