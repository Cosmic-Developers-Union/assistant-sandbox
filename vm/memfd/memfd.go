// Package memfd implements anonymous in-memory files created by
// memfd_create(2). The descriptor is backed by a resizable byte slice and
// therefore supports file-style read/write/seek, stat, truncate and mmap.
package memfd

import (
	"io"
	stdfs "io/fs"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const (
	maxFileSize = 1 << 31
	accessRW    = uint32(2)
)

var nextInode atomic.Uint64

type File struct {
	mu       sync.Mutex
	name     string
	inode    uint64
	data     []byte
	offset   int64
	append   bool
	nonblock bool
	closed   bool
}

func New(name string) *File {
	return &File{
		name:  name,
		inode: nextInode.Add(1),
	}
}

func (f *File) Name() string { return f.name }

func (f *File) Read(buffer []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errno.BadHandle
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if f.offset >= int64(len(f.data)) {
		return 0, io.EOF
	}
	if f.offset < 0 {
		return 0, errno.Invalid
	}
	n := copy(buffer, f.data[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *File) ReadAt(buffer []byte, offset int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errno.BadHandle
	}
	if offset < 0 {
		return 0, errno.Invalid
	}
	if offset >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(buffer, f.data[offset:])
	if n < len(buffer) {
		return n, io.EOF
	}
	return n, nil
}

func (f *File) Write(buffer []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errno.BadHandle
	}
	if f.append {
		f.offset = int64(len(f.data))
	}
	if f.offset < 0 {
		return 0, errno.Invalid
	}
	end := f.offset + int64(len(buffer))
	if end > maxFileSize {
		return 0, errno.NoSpace
	}
	if end > int64(len(f.data)) {
		grown := make([]byte, end)
		copy(grown, f.data)
		f.data = grown
	}
	n := copy(f.data[f.offset:], buffer)
	f.offset += int64(n)
	return n, nil
}

func (f *File) WriteAt(buffer []byte, offset int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errno.BadHandle
	}
	if offset < 0 {
		return 0, errno.Invalid
	}
	end := offset + int64(len(buffer))
	if end > maxFileSize {
		return 0, errno.NoSpace
	}
	if end > int64(len(f.data)) {
		grown := make([]byte, end)
		copy(grown, f.data)
		f.data = grown
	}
	return copy(f.data[offset:], buffer), nil
}

func (f *File) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errno.BadHandle
	}
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = f.offset
	case io.SeekEnd:
		base = int64(len(f.data))
	default:
		return 0, errno.Invalid
	}
	next := base + offset
	if next < 0 {
		return 0, errno.Invalid
	}
	f.offset = next
	return next, nil
}

func (f *File) Truncate(size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errno.BadHandle
	}
	if size < 0 || size > maxFileSize {
		return errno.Invalid
	}
	if size <= int64(len(f.data)) {
		f.data = f.data[:size]
		return nil
	}
	grown := make([]byte, size)
	copy(grown, f.data)
	f.data = grown
	return nil
}

func (f *File) Readable() bool { return true }
func (f *File) Writable() bool { return true }

func (f *File) StatusFlags() (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errno.BadHandle
	}
	flags := accessRW
	if f.append {
		flags |= 0o2000
	}
	if f.nonblock {
		flags |= 0o4000
	}
	return flags, nil
}

func (f *File) SetStatusFlags(flags uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errno.BadHandle
	}
	f.append = flags&0o2000 != 0
	f.nonblock = flags&0o4000 != 0
	return nil
}

func (f *File) Stat() (stdfs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errno.BadHandle
	}
	return fileInfo{name: f.name, size: int64(len(f.data)), inode: f.inode}, nil
}

func (f *File) Sync(bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errno.BadHandle
	}
	return nil
}

func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errno.BadHandle
	}
	f.closed = true
	f.data = nil
	return nil
}

type fileInfo struct {
	name  string
	size  int64
	inode uint64
}

func (i fileInfo) Name() string         { return i.name }
func (i fileInfo) Size() int64          { return i.size }
func (i fileInfo) Mode() stdfs.FileMode { return 0o600 }
func (i fileInfo) ModTime() time.Time   { return time.Time{} }
func (i fileInfo) IsDir() bool          { return false }
func (i fileInfo) Sys() any {
	return &vmfs.Metadata{Inode: i.inode, NLink: 1}
}

var (
	_ io.Reader   = (*File)(nil)
	_ io.ReaderAt = (*File)(nil)
	_ io.Writer   = (*File)(nil)
	_ io.WriterAt = (*File)(nil)
	_ io.Seeker   = (*File)(nil)
	_ io.Closer   = (*File)(nil)
)
