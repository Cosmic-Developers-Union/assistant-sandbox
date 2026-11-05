package memfs

import (
	"cmp"
	"context"
	"io"
	stdfs "io/fs"
	"path"
	"slices"
	"strings"
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
	accessMode uint32
	closed     bool
}

func (h *handle) Read(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errno.BadHandle
	}
	h.fs.st.mu.RLock()
	defer h.fs.st.mu.RUnlock()
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
	h.fs.st.mu.Lock()
	defer h.fs.st.mu.Unlock()
	if h.node.kind == kindDir {
		// 目录句柄只以只读方式打开;write(2) 返回 EBADF。
		return 0, errno.BadHandle
	}
	if !h.writable {
		// fd 未以写方式打开(含 O_APPEND|O_RDONLY)。
		return 0, errno.BadHandle
	}
	if h.appendMode {
		h.off = int64(len(h.node.data))
	}
	if h.off < 0 {
		return 0, errno.Invalid
	}
	// Linux 语义:常规文件按可用空间短写,返回已完成字节数;
	// 只有一字节都写不进才报 ENOSPC(I06)。零长度写恒成功。
	allowed := int64(len(p))
	if limit := int64(h.fs.maxBytes) - h.off; limit < allowed {
		allowed = limit
	}
	if h.fs.maxTotalBytes > 0 {
		used := h.fs.usedBytes() - int64(len(h.node.data))
		if limit := h.fs.maxTotalBytes - used - h.off; limit < allowed {
			allowed = limit
		}
	}
	if allowed <= 0 {
		if len(p) == 0 {
			return 0, nil
		}
		return 0, errno.NoSpace
	}
	end := h.off + allowed
	if end > int64(len(h.node.data)) {
		grown := make([]byte, end)
		copy(grown, h.node.data)
		h.node.data = grown
	}
	n := copy(h.node.data[h.off:], p[:allowed])
	h.off += int64(n)
	now := h.fs.now()
	h.node.mtime = now
	h.node.atime = now
	return n, nil
}

// ReadAt 在绝对偏移读取,不移动句柄游标(pread 语义;mmap 的文件
// 映射后备也经此读取,E08/E09)。越过数据末端短读:返回已读字节,
// 无错误;目录句柄报 EISDIR。
func (h *handle) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errno.Invalid
	}
	h.fs.st.mu.RLock()
	defer h.fs.st.mu.RUnlock()
	if h.node.kind == kindDir {
		return 0, errno.IsDirectory
	}
	if off >= int64(len(h.node.data)) {
		return 0, nil
	}
	return copy(p, h.node.data[off:]), nil
}

// WriteAt 在绝对偏移写入,不移动句柄游标(pwrite 语义;共享映射的
// 写入经此直通并即时持久,E09)。预算规则与 Write 相同:按可用
// 空间短写;越过现末端的写入以零扩展。只读句柄报 EBADF。
func (h *handle) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errno.Invalid
	}
	h.mu.Lock()
	closed, writable := h.closed, h.writable
	h.mu.Unlock()
	if closed {
		return 0, errno.BadHandle
	}
	if !writable {
		return 0, errno.BadHandle
	}
	h.fs.st.mu.Lock()
	defer h.fs.st.mu.Unlock()
	if h.node.kind == kindDir {
		return 0, errno.BadHandle
	}
	if len(p) == 0 {
		return 0, nil
	}
	allowed := int64(len(p))
	if limit := int64(h.fs.maxBytes) - off; limit < allowed {
		allowed = limit
	}
	if h.fs.maxTotalBytes > 0 {
		used := h.fs.usedBytes() - int64(len(h.node.data))
		if limit := h.fs.maxTotalBytes - used - off; limit < allowed {
			allowed = limit
		}
	}
	if allowed <= 0 {
		return 0, errno.NoSpace
	}
	end := off + allowed
	if end > int64(len(h.node.data)) {
		grown := make([]byte, end)
		copy(grown, h.node.data)
		h.node.data = grown
	}
	n := copy(h.node.data[off:], p[:allowed])
	now := h.fs.now()
	h.node.mtime = now
	h.node.atime = now
	return n, nil
}

// Readable 报告句柄是否以读方式打开。
func (h *handle) Readable() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed && h.accessMode != 1
}

// Writable 报告句柄是否以写方式打开。
func (h *handle) Writable() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed && h.writable
}

func (h *handle) Seek(offset int64, whence int) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errno.BadHandle
	}
	h.fs.st.mu.RLock()
	if h.node.kind == kindDir {
		return h.seekDirectoryLocked(offset, whence)
	}
	size := int64(len(h.node.data))
	h.fs.st.mu.RUnlock()
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
// 调用方持有 h.mu 与 f.st.mu.RLock。
func (h *handle) seekDirectoryLocked(offset int64, whence int) (int64, error) {
	h.fs.st.mu.RUnlock()
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
	h.fs.st.mu.RLock()
	defer h.fs.st.mu.RUnlock()
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
	h.fs.st.mu.Lock()
	defer h.fs.st.mu.Unlock()
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

// StatusFlags 报告句柄的打开状态标志(F_GETFL 视图):
// 访问模式 + O_APPEND。
func (h *handle) StatusFlags() (uint32, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errno.BadHandle
	}
	flags := h.accessMode
	if h.appendMode {
		flags |= 0o2000 // O_APPEND
	}
	return flags, nil
}

// SetStatusFlags 应用 F_SETFL:访问模式位不可改(忽略),
// O_APPEND 切换追加写,O_NONBLOCK 对常规文件是无效果的成功
// (与 Linux 语义一致),其余位忽略。
func (h *handle) SetStatusFlags(flags uint32) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errno.BadHandle
	}
	if flags&0o2000 != 0 {
		h.appendMode = true
	} else {
		h.appendMode = false
	}
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

// Space 报告句柄所在文件系统的容量(fstatfs 的句柄路径)。
func (h *handle) Space(ctx context.Context) (vmfs.Space, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return vmfs.Space{}, errno.BadHandle
	}
	return h.fs.Space(ctx, "/")
}

// SetTimes 以纳秒更新句柄文件的访问与修改时间(utimensat 的 fd 路径)。
func (h *handle) SetTimes(atime, mtime int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errno.BadHandle
	}
	h.fs.st.mu.Lock()
	defer h.fs.st.mu.Unlock()
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
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
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
	h.fs.st.mu.RLock()
	defer h.fs.st.mu.RUnlock()
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

// ResolveGuestPath 把相对路径解析为以本句柄目录为基准的客体绝对
// 路径(I02):目录句柄跟随 inode,目录被重命名后解析结果反映新
// 位置;词法拼接后的符号链接由文件系统按客体语义解析。
func (h *handle) ResolveGuestPath(name string) (string, error) {
	h.mu.Lock()
	closed := h.closed
	node := h.node
	h.mu.Unlock()
	if closed {
		return "", errno.Wrap(errno.BadHandle, "目录句柄已关闭")
	}
	if node.kind != kindDir {
		return "", errno.Wrap(errno.NotDirectory, "描述符不是目录")
	}
	if name == "" {
		return "", errno.Wrap(errno.Invalid, "客体路径不能为空")
	}
	if strings.HasPrefix(name, "/") {
		return name, nil
	}
	return path.Join(h.fs.pathOf(node), name), nil
}
