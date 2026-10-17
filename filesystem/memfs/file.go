package memfs

import (
	"io"
	stdfs "io/fs"
	"sync"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type handle struct {
	fs         *FS
	node       *inode
	mu         sync.Mutex
	off        int64
	appendMode bool
	closed     bool
}

func (h *handle) Read(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errno.BadHandle
	}
	h.fs.mu.RLock()
	defer h.fs.mu.RUnlock()
	if h.off >= int64(len(h.node.data)) {
		return 0, io.EOF
	}
	if h.off < 0 {
		return 0, errno.Invalid
	}
	n := copy(p, h.node.data[h.off:])
	h.off += int64(n)
	return n, nil
}

func (h *handle) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errno.BadHandle
	}
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	if h.appendMode {
		h.off = int64(len(h.node.data))
	}
	if h.off < 0 {
		return 0, errno.Invalid
	}
	end := h.off + int64(len(p))
	if end > int64(h.fs.maxBytes) {
		return 0, errno.NoSpace
	}

	if h.fs.maxTotalBytes > 0 {
		used := h.fs.usedBytes() - int64(len(h.node.data))
		if used+end > h.fs.maxTotalBytes {
			return 0, errno.NoSpace
		}
	}
	if end > int64(len(h.node.data)) {
		grown := make([]byte, end)
		copy(grown, h.node.data)
		h.node.data = grown
	}
	n := copy(h.node.data[h.off:], p)
	h.off += int64(n)
	now := h.fs.now()
	h.node.mtime = now
	h.node.atime = now
	return n, nil
}

func (h *handle) Seek(offset int64, whence int) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errno.BadHandle
	}
	h.fs.mu.RLock()
	size := int64(len(h.node.data))
	h.fs.mu.RUnlock()
	switch whence {
	case 0:

	case 1:
		offset += h.off
	case 2:
		offset += size
	default:
		return 0, errno.Invalid
	}
	if offset < 0 {
		return 0, errno.Invalid
	}
	h.off = offset
	return h.off, nil
}

func (h *handle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errno.BadHandle
	}
	h.closed = true
	f := h.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	h.node.opened--
	if h.node.nlink == 0 && h.node.opened == 0 {
		f.release(h.node)
	}
	return nil
}

func (h *handle) Stat() (stdfs.FileInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errno.BadHandle
	}
	h.fs.mu.RLock()
	defer h.fs.mu.RUnlock()
	return fileInfo{node: h.node}, nil
}

type fileInfo struct{ node *inode }

func (i fileInfo) Name() string { return i.node.name }
func (i fileInfo) Size() int64 {
	switch i.node.kind {
	case kindDir:
		return dirNominalSize
	case kindSymlink:
		return int64(len(i.node.target))
	default:
		return int64(len(i.node.data))
	}
}
func (i fileInfo) Mode() stdfs.FileMode { return i.node.mode }
func (i fileInfo) ModTime() time.Time {
	return time.Unix(0, i.node.mtime)
}
func (i fileInfo) IsDir() bool { return i.node.kind == kindDir }
func (i fileInfo) Sys() any {
	return &vmfs.Metadata{Inode: i.node.id, NLink: i.node.nlink, UID: i.node.uid, GID: i.node.gid}
}

type dirEntry struct{ info fileInfo }

func (e dirEntry) Name() string                  { return e.info.Name() }
func (e dirEntry) IsDir() bool                   { return e.info.IsDir() }
func (e dirEntry) Type() stdfs.FileMode          { return e.info.Mode().Type() }
func (e dirEntry) Info() (stdfs.FileInfo, error) { return e.info, nil }
