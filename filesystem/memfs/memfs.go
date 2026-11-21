package memfs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const (
	maxPathLength   = 4096
	maxNameLength   = 255
	maxSymlinkHops  = 8
	defaultMaxNodes = 8192
	defaultMaxBytes = 32 << 20
	dirNominalSize  = 4096
)

type Options struct {
	MaxNodes int

	MaxFileBytes int

	TotalBytes int64
}

// fsState 是 memfs 的可变核心:视图(FS 副本)共享同一指针。
type fsState struct {
	mu     sync.RWMutex
	root   *inode
	nodes  uint64
	nextID uint64
	live   map[*inode]struct{}
}

type FS struct {
	st    *fsState
	clock clock.Source

	maxNodes      int
	maxBytes      int
	maxTotalBytes int64

	// accessor 是多身份检查的调用方身份(I13):nil 表示单用户语义
	// (调用方视为文件所有者,沿用属主位判定)。
	accessor *accessorIdentity
	// accessorFunc 是动态身份来源(O01):每次判定读取当前 UID/GID。
	accessorFunc func() (uid, gid uint32)
}

// accessorIdentity 是 WithAccessor 绑定的调用方身份。
type accessorIdentity struct {
	uid uint32
	gid uint32
}

// WithAccessor 返回以 (uid,gid) 身份执行后续操作的视图(I13/O01):
// 视图共享同一棵树与锁;权限按 owner/group/other 分类判定,新建节点
// 归属该身份,sticky 目录约束删除/改名。
func (f *FS) WithAccessor(uid, gid uint32) (vmfs.System, error) {
	return f.WithAccessorFunc(func() (uint32, uint32) { return uid, gid })
}

// WithAccessorFunc 返回动态身份视图(O01):setuid/setgid 后后续
// 权限判定与新建节点立即使用新的 UID/GID。
func (f *FS) WithAccessorFunc(owner func() (uid, gid uint32)) (vmfs.System, error) {
	if owner == nil {
		return nil, errno.Wrap(errno.Invalid, "身份来源不能为空")
	}
	view := *f
	view.accessor = nil
	view.accessorFunc = owner
	return &view, nil
}

// owner 返回调用方身份;ok=false 表示单用户语义。
func (f *FS) owner() (uint32, uint32, bool) {
	if f.accessor != nil {
		return f.accessor.uid, f.accessor.gid, true
	}
	if f.accessorFunc != nil {
		uid, gid := f.accessorFunc()
		return uid, gid, true
	}
	return 0, 0, false
}

// classOf 按 accessor 分类调用方对节点的权限类别:0 属主/1 组/2 其他;
// 无 accessor 时视为属主(单用户语义)。
func (f *FS) classOf(node *inode) int {
	uid, gid, ok := f.owner()
	if !ok {
		return 0
	}
	if node.uid == uid {
		return 0
	}
	if node.gid == gid {
		return 1
	}
	return 2
}

// permBits 返回调用方类别对应的 rwx 位组。
func permBits(node *inode, class int) stdfs.FileMode {
	perm := node.mode.Perm()
	switch class {
	case 0:
		return (perm >> 6) & 7
	case 1:
		return (perm >> 3) & 7
	default:
		return perm & 7
	}
}

// canRead/canWrite/canExec 按类别判定;root(uid 0)对非属主类别读写
// 豁免,执行仍需任一 x 位(Linux 语义)。
func (f *FS) canRead(node *inode) bool {
	class := f.classOf(node)
	if uid, _, ok := f.owner(); ok && uid == 0 && class != 0 {
		return true
	}
	return permBits(node, class)&4 != 0
}

func (f *FS) canWrite(node *inode) bool {
	class := f.classOf(node)
	if uid, _, ok := f.owner(); ok && uid == 0 && class != 0 {
		return true
	}
	return permBits(node, class)&2 != 0
}

func (f *FS) canExec(node *inode) bool {
	return permBits(node, f.classOf(node))&1 != 0
}

// mayModifyIn 是 sticky 目录的删除/改名约束(I13):目录带 sticky 位
// 且调用方非目录属主时,仅允许操作属主为调用方的条目;root 豁免。
func (f *FS) mayModifyIn(dir, node *inode) bool {
	if dir.mode&stdfs.ModeSticky == 0 {
		return true
	}
	uid, _, ok := f.owner()
	if !ok || uid == 0 {
		return true
	}
	return node.uid == uid || dir.uid == uid
}

type inodeKind uint8

const (
	kindFile inodeKind = iota
	kindDir
	kindSymlink

	kindFifo
)

type inode struct {
	id   uint64
	kind inodeKind
	mode stdfs.FileMode

	uid      uint32
	gid      uint32
	atime    int64
	mtime    int64
	nlink    uint64
	opened   int
	data     []byte
	children map[string]*inode
	target   string
	parent   *inode
	name     string
}

// pathOf 返回节点当前所在的客体绝对路径:沿父指针回溯,目录被
// 重命名后即时反映新位置(目录句柄跟随 inode,I02)。
func (f *FS) pathOf(node *inode) string {
	f.st.mu.RLock()
	defer f.st.mu.RUnlock()
	var parts []string
	for cur := node; cur != f.st.root && cur != nil; cur = cur.parent {
		parts = append(parts, cur.name)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return "/" + strings.Join(parts, "/")
}

func New(clk clock.Source, opts Options) (*FS, error) {
	if clk == nil {
		return nil, errno.Invalid
	}
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = defaultMaxNodes
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = defaultMaxBytes
	}
	f := &FS{clock: clk, maxNodes: opts.MaxNodes, maxBytes: opts.MaxFileBytes,
		maxTotalBytes: opts.TotalBytes, st: &fsState{nextID: 1, live: map[*inode]struct{}{}}}
	now, err := clk.Wall(context.Background())
	if err != nil {
		return nil, err
	}
	f.st.root = &inode{
		id:       f.st.nextID,
		kind:     kindDir,
		mode:     stdfs.ModeDir | 0o755,
		atime:    now,
		mtime:    now,
		nlink:    1,
		children: map[string]*inode{},
		name:     "/",
	}
	f.st.nodes = 1
	f.st.live[f.st.root] = struct{}{}
	f.st.root.parent = f.st.root
	return f, nil
}

func (f *FS) Clock() clock.Source { return f.clock }

func split(p string) ([]string, error) {
	if p == "" {
		return nil, errno.Invalid
	}
	if len(p) > maxPathLength {
		return nil, errno.NameTooLong
	}
	if strings.ContainsRune(p, 0) {
		return nil, errno.Invalid
	}
	if !strings.HasPrefix(p, "/") {
		return nil, errno.Invalid
	}
	trimmed := strings.TrimPrefix(path.Clean(p), "/")
	if trimmed == "" || trimmed == "." {
		return nil, nil
	}
	parts := strings.Split(trimmed, "/")
	for _, part := range parts {
		if part == "" || part == "." {
			return nil, errno.Invalid
		}
		if len(part) > maxNameLength {
			return nil, errno.NameTooLong
		}
	}
	return parts, nil
}

func (f *FS) resolve(dir *inode, comps []string, depth int, followLast bool) (*inode, error) {
	if depth > maxSymlinkHops {
		return nil, errno.LinkLoop
	}
	cur := dir
	for i, comp := range comps {
		last := i == len(comps)-1
		switch comp {
		case ".":
			continue
		case "..":
			cur = cur.parent
			continue
		}
		if err := f.traversable(cur); err != nil {
			return nil, err
		}
		child, ok := cur.children[comp]
		if !ok {
			return nil, errno.NotFound
		}
		if child.kind == kindSymlink && (!last || followLast) {
			start, tcomps, err := f.linkPath(cur, child.target)
			if err != nil {
				return nil, err
			}
			cur, err = f.resolve(start, append(tcomps, comps[i+1:]...), depth+1, followLast)
			if err != nil {
				return nil, err
			}
			continue
		}
		cur = child
	}
	return cur, nil
}

func (f *FS) traversable(dir *inode) error {
	if dir.kind != kindDir {
		return errno.NotDirectory
	}
	if !f.canExec(dir) {
		return errno.Permission
	}
	return nil
}

func (f *FS) linkPath(parent *inode, target string) (*inode, []string, error) {
	if target == "" {
		return nil, nil, errno.Invalid
	}
	if len(target) > maxPathLength {
		return nil, nil, errno.NameTooLong
	}
	if strings.ContainsRune(target, 0) {
		return nil, nil, errno.Invalid
	}
	if strings.HasPrefix(target, "/") {
		trimmed := strings.TrimPrefix(path.Clean(target), "/")
		if trimmed == "" || trimmed == "." {
			return f.st.root, nil, nil
		}
		return f.st.root, strings.Split(trimmed, "/"), nil
	}
	cleaned := path.Clean(target)
	if cleaned == "." {
		return parent, nil, nil
	}
	return parent, strings.Split(cleaned, "/"), nil
}

func (f *FS) lookup(p string, followLast bool) (*inode, *inode, string, error) {
	comps, err := split(p)
	if err != nil {
		return nil, nil, "", err
	}
	if len(comps) == 0 {
		return f.st.root, f.st.root, "/", nil
	}

	head, tail := comps[:len(comps)-1], comps[len(comps)-1]
	dir, err := f.resolve(f.st.root, head, 1, true)
	if err != nil {
		return nil, nil, "", err
	}
	if err := f.traversable(dir); err != nil {
		return nil, nil, "", err
	}
	child, ok := dir.children[tail]
	if !ok {
		return nil, nil, "", errno.NotFound
	}
	if child.kind == kindSymlink && followLast {
		start, tcomps, err := f.linkPath(dir, child.target)
		if err != nil {
			return nil, nil, "", err
		}
		node, err := f.resolve(start, tcomps, 1, true)
		if err != nil {
			return nil, nil, "", err
		}
		return node, node.parent, node.name, nil
	}
	return child, dir, tail, nil
}

func (f *FS) now() int64 {
	value, err := f.clock.Wall(context.Background())
	if err != nil {
		return 0
	}
	return value
}

func (f *FS) checkWritable(p string) (*inode, string, error) {
	comps, err := split(p)
	if err != nil {
		return nil, "", err
	}
	if len(comps) == 0 {
		return nil, "", errno.Invalid
	}
	head, tail := comps[:len(comps)-1], comps[len(comps)-1]
	dir, err := f.resolve(f.st.root, head, 1, true)
	if err != nil {
		return nil, "", err
	}
	if err := f.traversable(dir); err != nil {
		return nil, "", err
	}
	if !f.canWrite(dir) {
		return nil, "", errno.Permission
	}
	return dir, tail, nil
}

// applyCreateMode 保留创建模式的特殊位( setuid/setgid/sticky ):
// stdfs 编码下它们高于 9 位,Perm() 会丢弃。
func applyCreateMode(mode stdfs.FileMode) stdfs.FileMode {
	return mode.Perm() | mode&(stdfs.ModeSetuid|stdfs.ModeSetgid|stdfs.ModeSticky)
}

func (f *FS) newNode(kind inodeKind, mode stdfs.FileMode, now int64) (*inode, error) {
	if f.st.nodes >= uint64(f.maxNodes) {
		return nil, errno.NoSpace
	}
	f.st.nextID++
	f.st.nodes++
	node := &inode{id: f.st.nextID, kind: kind, mode: mode, atime: now, mtime: now, nlink: 1}
	if uid, gid, ok := f.owner(); ok {
		node.uid = uid
		node.gid = gid
	}
	f.st.live[node] = struct{}{}
	return node, nil
}

func (f *FS) release(node *inode) {
	f.st.nodes--
	delete(f.st.live, node)
	node.data = nil
}

func (f *FS) Open(ctx context.Context, p string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	access := flags & 0b11
	create := flags&vmfs.Create != 0
	exclusive := flags&vmfs.Exclusive != 0
	truncate := flags&vmfs.Truncate != 0
	append := flags&vmfs.Append != 0
	if exclusive && !create {
		return nil, errno.Invalid
	}
	// O_APPEND|O_RDONLY 在 Linux 允许打开(写入时才报 EBADF)。
	node, _, _, err := f.lookup(p, true)
	created := false
	if err != nil {
		if !create || !errors.Is(err, errno.NotFound) {
			return nil, err
		}

		dir, tail, derr := f.checkWritable(p)
		if derr != nil {
			return nil, derr
		}
		now := f.now()
		node, err = f.newNode(kindFile, applyCreateMode(mode), now)
		if err != nil {
			return nil, err
		}
		node.name = tail
		node.parent = dir
		dir.children[tail] = node
		dir.mtime = now
		created = true
	} else if create && exclusive {
		return nil, errno.Exist
	}
	if !created {
		switch node.kind {
		case kindDir:
			// Linux 语义:O_RDONLY 允许打开目录(getdents64 入口),
			// 写访问与截断/追加报 EISDIR。
			if access != 0 || truncate || append {
				return nil, errno.IsDirectory
			}
			if !f.canRead(node) {
				return nil, errno.Permission
			}
			node.opened++
			return &handle{fs: f, node: node}, nil
		case kindSymlink:
			return nil, errno.NotFound
		case kindFifo:

			return nil, errno.Wrap(errno.NotSupported, "客体没有命名管道的读写语义")
		}
		if access == 0 && !f.canRead(node) || access != 0 && !f.canWrite(node) {
			return nil, errno.Permission
		}
	}
	if truncate {
		if len(node.data) > f.maxBytes {
			return nil, errno.NoSpace
		}
		node.data = node.data[:0]
		node.mtime = f.now()
	}
	node.opened++
	return &handle{fs: f, node: node, appendMode: append, writable: access != 0, accessMode: access}, nil
}

func (f *FS) Stat(ctx context.Context, p string) (stdfs.FileInfo, error) {
	return f.stat(ctx, p, true)
}

func (f *FS) Lstat(ctx context.Context, p string) (stdfs.FileInfo, error) {
	return f.stat(ctx, p, false)
}

func (f *FS) stat(ctx context.Context, p string, follow bool) (stdfs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.st.mu.RLock()
	defer f.st.mu.RUnlock()
	node, _, _, err := f.lookup(p, follow)
	if err != nil {
		return nil, err
	}
	return fileInfo{node: node}, nil
}

func (f *FS) ReadDir(ctx context.Context, p string) ([]stdfs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.st.mu.RLock()
	defer f.st.mu.RUnlock()
	node, _, _, err := f.lookup(p, true)
	if err != nil {
		return nil, err
	}
	if node.kind != kindDir {
		return nil, errno.NotDirectory
	}
	if !f.canRead(node) {
		return nil, errno.Permission
	}
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]stdfs.DirEntry, 0, len(names))
	for _, name := range names {
		out = append(out, dirEntry{info: fileInfo{node: node.children[name]}})
	}
	return out, nil
}

func (f *FS) Mkdir(ctx context.Context, p string, mode stdfs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()

	if node, _, _, err := f.lookup(p, false); err == nil {
		if node == f.st.root {
			return errno.Exist
		}
	}
	dir, tail, err := f.checkWritable(p)
	if err != nil {
		return err
	}
	if _, exists := dir.children[tail]; exists {
		return errno.Exist
	}
	node, err := f.newNode(kindDir, stdfs.ModeDir|applyCreateMode(mode), f.now())
	if err != nil {
		return err
	}
	node.children = map[string]*inode{}
	node.name = tail
	node.parent = dir
	dir.children[tail] = node
	dir.nlink++
	dir.mtime = node.atime
	return nil
}

func (f *FS) Remove(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	node, parent, name, err := f.lookup(p, false)
	if err != nil {
		return err
	}
	if node == f.st.root {
		return errno.Permission
	}
	if !f.canWrite(parent) || !f.mayModifyIn(parent, node) {
		return errno.Permission
	}
	switch node.kind {
	case kindDir:
		if len(node.children) > 0 {
			return errno.NotEmpty
		}
		parent.nlink--
	case kindSymlink:

	default:

		node.nlink--
		if node.nlink == 0 && node.opened == 0 {
			f.release(node)
		}
	}
	delete(parent.children, name)
	parent.mtime = f.now()
	return nil
}

func (f *FS) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	src, srcParent, srcName, err := f.lookup(oldPath, false)
	if err != nil {
		return err
	}
	if src == f.st.root {
		return errno.Permission
	}
	dstParent, dstName, err := f.checkWritable(newPath)
	if err != nil {
		return err
	}
	if !f.canWrite(srcParent) || !f.canWrite(dstParent) || !f.mayModifyIn(srcParent, src) {
		return errno.Permission
	}
	if existing, ok := dstParent.children[dstName]; ok && !f.mayModifyIn(dstParent, existing) {
		return errno.Permission
	}

	if srcParent == dstParent && srcName == dstName {
		return errno.Invalid
	}
	for dir := dstParent; dir != f.st.root; dir = dir.parent {
		if dir == src {
			return errno.Invalid
		}
	}
	if dstParent == src {
		return errno.Invalid
	}
	if existing, ok := dstParent.children[dstName]; ok {
		switch {
		case src.kind == kindDir && existing.kind == kindDir:
			if len(existing.children) > 0 {
				return errno.NotEmpty
			}
			dstParent.nlink--
		case src.kind == kindDir:
			// 目录覆盖非目录:Linux rename(2) 报 ENOTDIR。
			return errno.NotDirectory
		case existing.kind == kindDir:
			return errno.IsDirectory
		default:
			existing.nlink--
			if existing.nlink == 0 && existing.opened == 0 {
				f.release(existing)
			}
		}
	}
	delete(srcParent.children, srcName)
	dstParent.children[dstName] = src
	src.parent = dstParent
	src.name = dstName
	now := f.now()
	srcParent.mtime = now
	dstParent.mtime = now
	return nil
}

// Link 实现 link(2)/linkat(2) 默认语义:oldpath 为符号链接时
// 不解引用,硬链接指向链接本身(I11)。
func (f *FS) Link(ctx context.Context, oldPath, newPath string) error {
	return f.link(ctx, oldPath, newPath, false)
}

// LinkFollowing 实现 linkat(2) 的 AT_SYMLINK_FOLLOW:解引用
// 符号链接,对目标文件建立硬链接。
func (f *FS) LinkFollowing(ctx context.Context, oldPath, newPath string) error {
	return f.link(ctx, oldPath, newPath, true)
}

func (f *FS) link(ctx context.Context, oldPath, newPath string, follow bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	src, _, _, err := f.lookup(oldPath, follow)
	if err != nil {
		return err
	}
	// 不跟随的硬链接可以指向符号链接本身;目录始终拒绝。
	if src.kind != kindFile && src.kind != kindSymlink {
		return errno.Invalid
	}
	dstParent, dstName, err := f.checkWritable(newPath)
	if err != nil {
		return err
	}
	if dstParent.mode.Perm()&0o200 == 0 {
		return errno.Permission
	}
	if _, exists := dstParent.children[dstName]; exists {
		return errno.Exist
	}
	src.nlink++
	dstParent.children[dstName] = src
	dstParent.mtime = f.now()
	return nil
}

func (f *FS) Symlink(ctx context.Context, target, linkPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	if target == "" {
		return errno.Invalid
	}
	if len(target) > maxPathLength {
		return errno.NameTooLong
	}
	dstParent, dstName, err := f.checkWritable(linkPath)
	if err != nil {
		return err
	}
	if dstParent.mode.Perm()&0o200 == 0 {
		return errno.Permission
	}
	if _, exists := dstParent.children[dstName]; exists {
		return errno.Exist
	}
	node, err := f.newNode(kindSymlink, stdfs.ModeSymlink|0o777, f.now())
	if err != nil {
		return err
	}
	node.target = target
	node.name = dstName
	node.parent = dstParent
	dstParent.children[dstName] = node
	dstParent.mtime = node.atime
	return nil
}

func (f *FS) Readlink(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.st.mu.RLock()
	defer f.st.mu.RUnlock()
	node, _, _, err := f.lookup(p, false)
	if err != nil {
		return "", err
	}
	if node.kind != kindSymlink {
		return "", errno.Invalid
	}
	return node.target, nil
}

func (f *FS) SetTimes(ctx context.Context, p string, atime, mtime int64) error {
	return f.SetTimesFollow(ctx, p, atime, mtime, true)
}

func (f *FS) SetTimesFollow(ctx context.Context, p string, atime, mtime int64, follow bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	node, _, _, err := f.lookup(p, follow)
	if err != nil {
		return err
	}
	node.atime = atime
	node.mtime = mtime
	return nil
}

func (f *FS) AccessTime(ctx context.Context, p string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	node, _, _, err := f.lookup(p, true)
	if err != nil {
		return 0, err
	}
	return node.atime, nil
}

func (f *FS) Chown(ctx context.Context, p string, uid, gid int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	node, _, _, err := f.lookup(p, true)
	if err != nil {
		return err
	}
	if uid != vmfs.ChownUnchanged {
		if uid < 0 || uid > 0xFFFF_FFFF {
			return errno.Wrap(errno.Invalid, "属主越界")
		}
		node.uid = uint32(uid)
	}
	if gid != vmfs.ChownUnchanged {
		if gid < 0 || gid > 0xFFFF_FFFF {
			return errno.Wrap(errno.Invalid, "属组越界")
		}
		node.gid = uint32(gid)
	}
	return nil
}

func (f *FS) Chmod(ctx context.Context, p string, mode stdfs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	node, _, _, err := f.lookup(p, true)
	if err != nil {
		return err
	}
	node.mode = node.mode.Type() | vmfs.ModeBits(mode)
	node.mtime = f.now()
	return nil
}
