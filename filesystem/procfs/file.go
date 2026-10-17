package procfs

import (
	"io"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type file struct {
	name   string
	data   []byte
	offset int64
	closed bool
}

func newFile(name string, data []byte) *file {
	return &file{name: name, data: data}
}

func (f *file) Read(buffer []byte) (int, error) {
	if f.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	if f.offset >= int64(len(f.data)) {

		return 0, io.EOF
	}
	read := copy(buffer, f.data[f.offset:])
	f.offset += int64(read)
	return read, nil
}

func (f *file) Write([]byte) (int, error) {
	if f.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	return 0, errno.Wrap(errno.ReadOnlyFS, "procfs 是只读视图")
}

func (f *file) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	var base int64
	switch whence {
	case seekStart:
		base = 0
	case seekCurrent:
		base = f.offset
	case seekEnd:
		base = int64(len(f.data))
	default:
		return 0, errno.Wrap(errno.Invalid, "非法的定位基准")
	}
	next := base + offset
	if next < 0 {
		return 0, errno.Wrap(errno.Invalid, "定位到负偏移")
	}
	f.offset = next
	return next, nil
}

func (f *file) Stat() (stdfs.FileInfo, error) {
	if f.closed {
		return nil, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	return info{name: f.name, mode: 0o444, size: int64(len(f.data))}, nil
}

func (f *file) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	f.data = nil
	return nil
}

const (
	seekStart   = 0
	seekCurrent = 1
	seekEnd     = 2
)
