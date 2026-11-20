package procfs

import (
	"io"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// dirHandle 是 procfs 目录的打开句柄:支持 Readdir/Seek,供 getdents64
// 与 opendir/readdir 使用(N07)。
type dirHandle struct {
	name    string
	entries []stdfs.DirEntry
	offset  int
	closed  bool
}

func newDirHandle(name string, entries []stdfs.DirEntry) *dirHandle {
	return &dirHandle{name: name, entries: entries}
}

func (d *dirHandle) Readdir(limit int) ([]stdfs.DirEntry, error) {
	if d.closed {
		return nil, errno.Wrap(errno.Cancelled, "目录句柄已关闭")
	}
	if limit <= 0 || d.offset >= len(d.entries) {
		return nil, nil
	}
	end := min(d.offset+limit, len(d.entries))
	batch := append([]stdfs.DirEntry(nil), d.entries[d.offset:end]...)
	d.offset = end
	return batch, nil
}

func (d *dirHandle) Read([]byte) (int, error) {
	if d.closed {
		return 0, errno.Wrap(errno.Cancelled, "目录句柄已关闭")
	}
	return 0, errno.Wrap(errno.IsDirectory, "是目录: "+d.name)
}

func (d *dirHandle) Write([]byte) (int, error) {
	if d.closed {
		return 0, errno.Wrap(errno.Cancelled, "目录句柄已关闭")
	}
	return 0, errno.Wrap(errno.ReadOnlyFS, "procfs 是只读视图")
}

func (d *dirHandle) Seek(offset int64, whence int) (int64, error) {
	if d.closed {
		return 0, errno.Wrap(errno.Cancelled, "目录句柄已关闭")
	}
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = int64(d.offset)
	case io.SeekEnd:
		base = int64(len(d.entries))
	default:
		return 0, errno.Wrap(errno.Invalid, "非法的定位基准")
	}
	next := base + offset
	if next < 0 {
		return 0, errno.Wrap(errno.Invalid, "定位到负偏移")
	}
	d.offset = min(int(next), len(d.entries))
	return int64(d.offset), nil
}

func (d *dirHandle) Stat() (stdfs.FileInfo, error) {
	if d.closed {
		return nil, errno.Wrap(errno.Cancelled, "目录句柄已关闭")
	}
	return info{name: d.name, mode: stdfs.ModeDir | 0o555}, nil
}

func (d *dirHandle) Close() error {
	d.closed = true
	return nil
}
