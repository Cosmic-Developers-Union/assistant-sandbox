package legacy

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"
	"time"

	abi "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/experimental/sysfs"
	"github.com/tetratelabs/wazero/sys"
)

type Filesystem struct {
	abi.UnimplementedFS
	root       *os.Root
	readonly   bool
	maxFile    int64
	remaining  atomic.Int64
	handles    atomic.Int64
	operations atomic.Int64
}

type FilesystemConfig struct {
	ReadOnly                             bool
	MaxFileBytes, WriteBytes, Operations int64
}

func OpenFilesystem(dir string, cfg FilesystemConfig) (*Filesystem, error) {
	if cfg.MaxFileBytes <= 0 || cfg.WriteBytes <= 0 || cfg.Operations <= 0 {
		return nil, fmt.Errorf("文件系统预算必须为正数")
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	f := &Filesystem{root: root, readonly: cfg.ReadOnly, maxFile: cfg.MaxFileBytes}
	f.remaining.Store(cfg.WriteBytes)
	f.operations.Store(cfg.Operations)
	return f, nil
}

func (f *Filesystem) Close() error { return f.root.Close() }

func validPath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
func errno(err error) abi.Errno {
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return 0
	case errors.Is(err, fs.ErrNotExist):
		return abi.ENOENT
	case errors.Is(err, fs.ErrExist):
		return abi.EEXIST
	case errors.Is(err, fs.ErrPermission):
		return abi.EACCES
	case errors.Is(err, fs.ErrClosed):
		return abi.EBADF
	default:
		return abi.EIO
	}
}
func (f *Filesystem) OpenFile(name string, flags abi.Oflag, perm fs.FileMode) (abi.File, abi.Errno) {
	if !validPath(name) {
		return nil, abi.EACCES
	}
	writing := flags&(abi.O_WRONLY|abi.O_RDWR|abi.O_CREAT|abi.O_TRUNC|abi.O_APPEND) != 0
	if writing && f.readonly {
		return nil, abi.EROFS
	}
	if writing && !f.mutate() {
		return nil, abi.ERANGE
	}
	if f.handles.Add(1) > 256 {
		f.handles.Add(-1)
		return nil, abi.EAGAIN
	}
	owned := false
	defer func() {
		if !owned {
			f.handles.Add(-1)
		}
	}()

	info, err := f.root.Stat(name)
	if err == nil && !info.IsDir() && !info.Mode().IsRegular() {
		return nil, abi.EACCES
	}
	mode := os.O_RDONLY
	if flags&abi.O_RDWR != 0 {
		mode = os.O_RDWR
	} else if flags&abi.O_WRONLY != 0 {
		mode = os.O_WRONLY
	}
	for _, pair := range []struct {
		guest abi.Oflag
		host  int
	}{{abi.O_CREAT, os.O_CREATE}, {abi.O_TRUNC, os.O_TRUNC}, {abi.O_APPEND, os.O_APPEND}, {abi.O_EXCL, os.O_EXCL}} {
		if flags&pair.guest != 0 {
			mode |= pair.host
		}
	}
	file, err := f.root.OpenFile(name, mode|nonblockFlag(), perm.Perm()&0o666)
	if err != nil {
		return nil, errno(err)
	}
	info, err = file.Stat()
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		if closeErr := file.Close(); closeErr != nil {
			return nil, errno(closeErr)
		}
		return nil, abi.EACCES
	}
	if flags&abi.O_DIRECTORY != 0 && !info.IsDir() {
		if closeErr := file.Close(); closeErr != nil {
			return nil, errno(closeErr)
		}
		return nil, abi.ENOTDIR
	}
	adapter := &sysfs.AdaptFS{FS: openedFileFS{file: file}}
	abiFile, code := adapter.OpenFile(".", flags, perm)
	if code != 0 {
		if closeErr := file.Close(); closeErr != nil {
			return nil, errno(closeErr)
		}
		return nil, code
	}
	owned = true
	return &rootFile{File: abiFile, file: file, fs: f, append: flags&abi.O_APPEND != 0, writable: writing, name: name}, 0
}
func (f *Filesystem) Stat(name string) (sys.Stat_t, abi.Errno) {
	if !validPath(name) {
		return sys.Stat_t{}, abi.EACCES
	}
	info, err := f.root.Stat(name)
	if err != nil {
		return sys.Stat_t{}, errno(err)
	}
	return sys.NewStat_t(info), 0
}
func (f *Filesystem) Lstat(name string) (sys.Stat_t, abi.Errno) {
	if !validPath(name) {
		return sys.Stat_t{}, abi.EACCES
	}
	info, err := f.root.Lstat(name)
	if err != nil {
		return sys.Stat_t{}, errno(err)
	}
	return sys.NewStat_t(info), 0
}
func (f *Filesystem) Mkdir(name string, perm fs.FileMode) abi.Errno {
	if !f.mutate() {
		return abi.ERANGE
	}
	if f.readonly {
		return abi.EROFS
	}
	if !validPath(name) {
		return abi.EACCES
	}
	return errno(f.root.Mkdir(name, perm.Perm()&0o777))
}
func (f *Filesystem) Rename(from, to string) abi.Errno {
	if !f.mutate() {
		return abi.ERANGE
	}
	if f.readonly {
		return abi.EROFS
	}
	if !validPath(from) || !validPath(to) {
		return abi.EACCES
	}
	return errno(f.root.Rename(from, to))
}
func (f *Filesystem) Unlink(name string) abi.Errno {
	if !f.mutate() {
		return abi.ERANGE
	}
	if f.readonly {
		return abi.EROFS
	}
	if !validPath(name) {
		return abi.EACCES
	}
	info, err := f.root.Lstat(name)
	if err != nil {
		return errno(err)
	}
	if info.IsDir() {
		return abi.EISDIR
	}
	return errno(f.root.Remove(name))
}
func (f *Filesystem) Rmdir(name string) abi.Errno {
	if !f.mutate() {
		return abi.ERANGE
	}
	if f.readonly {
		return abi.EROFS
	}
	if !validPath(name) {
		return abi.EACCES
	}
	info, err := f.root.Lstat(name)
	if err != nil {
		return errno(err)
	}
	if !info.IsDir() {
		return abi.ENOTDIR
	}
	return errno(f.root.Remove(name))
}

type rootFile struct {
	abi.File
	file             *os.File
	fs               *Filesystem
	append, writable bool
	closed           atomic.Bool
	name             string
}

func (f *rootFile) Stat() (sys.Stat_t, abi.Errno) {
	info, err := f.file.Stat()
	if err != nil {
		return sys.Stat_t{}, errno(err)
	}
	return sys.NewStat_t(info), 0
}
func (f *rootFile) Dev() (uint64, abi.Errno)         { st, e := f.Stat(); return st.Dev, e }
func (f *rootFile) Ino() (sys.Inode, abi.Errno)      { st, e := f.Stat(); return st.Ino, e }
func (f *rootFile) IsDir() (bool, abi.Errno)         { st, e := f.Stat(); return st.Mode.IsDir(), e }
func (f *rootFile) IsAppend() bool                   { return f.append }
func (f *rootFile) Read(buf []byte) (int, abi.Errno) { n, e := f.file.Read(buf); return n, errno(e) }
func (f *rootFile) Pread(buf []byte, off int64) (int, abi.Errno) {
	n, e := f.file.ReadAt(buf, off)
	return n, errno(e)
}

func (f *rootFile) Readdir(n int) ([]abi.Dirent, abi.Errno) {
	entries, e := f.file.ReadDir(n)
	result := make([]abi.Dirent, 0, len(entries))
	for _, v := range entries {
		result = append(result, abi.Dirent{Name: v.Name(), Type: v.Type()})
	}
	return result, errno(e)
}
func (f *rootFile) reserve(n int64) bool {
	for {
		old := f.fs.remaining.Load()
		if n > old {
			return false
		}
		if f.fs.remaining.CompareAndSwap(old, old-n) {
			return true
		}
	}
}
func (f *rootFile) Write(buf []byte) (int, abi.Errno) {
	if !f.writable {
		return 0, abi.EBADF
	}
	off, err := f.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, errno(err)
	}
	if f.append {
		info, err := f.file.Stat()
		if err != nil {
			return 0, errno(err)
		}
		off = info.Size()
	}
	if off < 0 || int64(len(buf)) > f.fs.maxFile-off || !f.reserve(int64(len(buf))) {
		return 0, abi.ERANGE
	}
	n, e := f.file.Write(buf)
	return n, errno(e)
}
func (f *rootFile) Pwrite(buf []byte, off int64) (int, abi.Errno) {
	if !f.writable {
		return 0, abi.EBADF
	}
	if off < 0 || int64(len(buf)) > f.fs.maxFile-off || !f.reserve(int64(len(buf))) {
		return 0, abi.ERANGE
	}
	n, e := f.file.WriteAt(buf, off)
	return n, errno(e)
}
func (f *rootFile) Truncate(size int64) abi.Errno {
	if !f.writable {
		return abi.EBADF
	}
	if size < 0 {
		return abi.EINVAL
	}
	if size > f.fs.maxFile {
		return abi.ERANGE
	}
	info, e := f.file.Stat()
	if e != nil {
		return errno(e)
	}
	if size > info.Size() && !f.reserve(size-info.Size()) {
		return abi.ERANGE
	}
	return errno(f.file.Truncate(size))
}
func (f *rootFile) Sync() abi.Errno     { return errno(f.file.Sync()) }
func (f *rootFile) Datasync() abi.Errno { return f.Sync() }
func (f *rootFile) Name() string        { return f.name }

func (f *rootFile) Close() abi.Errno {
	if f.closed.Swap(true) {
		return 0
	}
	f.fs.handles.Add(-1)
	return errno(f.file.Close())
}

func (f *Filesystem) mutate() bool { return f.operations.Add(-1) >= 0 }

func (f *rootFile) Utimens(atim, mtim int64) abi.Errno {
	if f.fs.readonly {
		return abi.EROFS
	}
	info, err := f.file.Stat()
	if err != nil {
		return errno(err)
	}
	if atim == abi.UTIME_OMIT {
		atim = info.ModTime().UnixNano()
	}
	if mtim == abi.UTIME_OMIT {
		mtim = info.ModTime().UnixNano()
	}
	return errno(f.fs.root.Chtimes(f.name, time.Unix(0, atim), time.Unix(0, mtim)))
}

type openedFileFS struct{ file *os.File }

func (f openedFileFS) Open(string) (fs.File, error) { return f.file, nil }
