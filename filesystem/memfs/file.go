package memfs

import (
	"cmp"
	"io"
	stdfs "io/fs"
	"slices"
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
	dirPos     uint64
	appendMode bool
	writable   bool
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
	if h.node.kind == kindDir {
		// Linux 语义:read(2) 对目录句柄返回 EISDIR,目录内容经 readdir 读取。
		return 0, errno.IsDirectory
	}
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
	if h.node.kind == kindDir {
		// 目录句柄只以只读方式打开;write(2) 返回 EBADF。
		return 0, errno.BadHandle
	}
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
	if h.node.kind == kindDir {
		return h.seekDirectoryLocked(offset, whence)
	}
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

// seekDirectoryLocked 定位目录游标:SEEK_SET/SEEK_CUR 按条目 cookie
// (inode 号)重新定位,SEEK_END 不受支持。rewinddir 即 Seek(0, SEEK_SET)。
// 调用方持有 h.mu 与 f.mu.RLock。
func (h *handle) seekDirectoryLocked(offset int64, whence int) (int64, error) {
	h.fs.mu.RUnlock()
	switch whence {
	case 0:
		if offset < 0 {
			return 0, errno.Invalid
		}
		h.dirPos = uint64(offset)
	case 1:
		target := int64(h.dirPos) + offset
		if target < 0 {
			return 0, errno.Invalid
		}
		h.dirPos = uint64(target)
	default:
		return 0, errno.Wrap(errno.NotSupported, "目录句柄不支持相对末尾定位")
	}
	return int64(h.dirPos), nil
}

// Readdir 以 getdents64 语义返回自当前游标起的至多 limit 条目录项:
// 只返回仍存在的条目,按 cookie(inode 号,即创建顺序)排序。
// 游标(dirPos)是下一个待返回的 cookie,与 seekdir/lseek 的
// 语义一致:Seek(c, SEEK_SET) 后从 cookie 为 c 的条目继续。
// 返回 0 条表示目录已读完。limit<=0 报 EINVAL。
func (h *handle) Readdir(limit int) ([]stdfs.DirEntry, error) {
	if limit <= 0 {
		return nil, errno.Invalid
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errno.BadHandle
	}
	h.fs.mu.RLock()
	defer h.fs.mu.RUnlock()
	if h.node.kind != kindDir {
		return nil, errno.NotDirectory
	}
	type pending struct {
		cookie uint64
		node   *inode
	}
	var remaining []pending
	for _, child := range h.node.children {
		if child.id >= h.dirPos {
			remaining = append(remaining, pending{cookie: child.id, node: child})
		}
	}
	slices.SortFunc(remaining, func(a, b pending) int { return cmp.Compare(a.cookie, b.cookie) })
	if len(remaining) > limit {
		remaining = remaining[:limit]
	}
	out := make([]stdfs.DirEntry, 0, len(remaining))
	for _, item := range remaining {
		h.dirPos = item.cookie + 1
		out = append(out, dirEntry{info: fileInfo{node: item.node}})
	}
	return out, nil
}

// Truncate 以字节长度截断文件:收缩丢弃尾部,扩张补零(空洞)。
// 需要句柄以可写方式打开;目录报 EISDIR;扩张受单文件与总量预算约束。
func (h *handle) Truncate(size int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errno.BadHandle
	}
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	if h.node.kind == kindDir {
		return errno.IsDirectory
	}
	if !h.writable || size < 0 {
		return errno.Invalid
	}
	if size > int64(len(h.node.data)) {
		if size > int64(h.fs.maxBytes) {
			return errno.NoSpace
		}
		if h.fs.maxTotalBytes > 0 {
			used := h.fs.usedBytes() - int64(len(h.node.data))
			if used+size > h.fs.maxTotalBytes {
				return errno.NoSpace
			}
		}
		grown := make([]byte, size)
		copy(grown, h.node.data)
		h.node.data = grown
	} else {
		h.node.data = h.node.data[:size]
	}
	now := h.fs.now()
	h.node.mtime = now
	h.node.atime = now
	return nil
}

// Sync 把句柄数据冲入稳定存储。memfs 以宿主内存为存储介质,
// VM 存活期内数据不会丢失,同步是真实的空操作;
// fdatasync 与 fsync 在此语义下没有可观察差异。
func (h *handle) Sync(bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errno.BadHandle
	}
	return nil
}

// SetTimes 以纳秒更新句柄文件的访问与修改时间(utimensat 的 fd 路径)。
func (h *handle) SetTimes(atime, mtime int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errno.BadHandle
	}
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	if h.node.kind == kindSymlink {
		return errno.Invalid
	}
	h.node.atime = atime
	h.node.mtime = mtime
	return nil
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
	return &vmfs.Metadata{
		Inode: i.node.id, NLink: i.node.nlink, UID: i.node.uid, GID: i.node.gid,
		AtimeNanos: i.node.atime,
		MtimeNanos: i.node.mtime,
		CtimeNanos: i.node.mtime, // memfs 不单独维护 ctime,以 mtime 充当
	}
}

type dirEntry struct{ info fileInfo }

func (e dirEntry) Name() string                  { return e.info.Name() }
func (e dirEntry) IsDir() bool                   { return e.info.IsDir() }
func (e dirEntry) Type() stdfs.FileMode          { return e.info.Mode().Type() }
func (e dirEntry) Info() (stdfs.FileInfo, error) { return e.info, nil }
