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

type FS struct {
	mu     sync.RWMutex
	root   *inode
	nodes  uint64
	nextID uint64
	clock  clock.Source

	live          map[*inode]struct{}
	maxNodes      int
	maxBytes      int
	maxTotalBytes int64
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
		maxTotalBytes: opts.TotalBytes, nextID: 1, live: map[*inode]struct{}{}}
	now, err := clk.Wall(context.Background())
	if err != nil {
		return nil, err
	}
	f.root = &inode{
		id:       f.nextID,
		kind:     kindDir,
		mode:     stdfs.ModeDir | 0o755,
		atime:    now,
		mtime:    now,
		nlink:    1,
		children: map[string]*inode{},
		name:     "/",
	}
	f.nodes = 1
	f.live[f.root] = struct{}{}
	f.root.parent = f.root
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
	if dir.mode.Perm()&0o100 == 0 {
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
			return f.root, nil, nil
		}
		return f.root, strings.Split(trimmed, "/"), nil
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
		return f.root, f.root, "/", nil
	}

	head, tail := comps[:len(comps)-1], comps[len(comps)-1]
	dir, err := f.resolve(f.root, head, 1, true)
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
	dir, err := f.resolve(f.root, head, 1, true)
	if err != nil {
		return nil, "", err
	}
	if err := f.traversable(dir); err != nil {
		return nil, "", err
	}
	if dir.mode.Perm()&0o200 == 0 {
		return nil, "", errno.Permission
	}
	return dir, tail, nil
}

func (f *FS) newNode(kind inodeKind, mode stdfs.FileMode, now int64) (*inode, error) {
	if f.nodes >= uint64(f.maxNodes) {
		return nil, errno.NoSpace
	}
	f.nextID++
	f.nodes++
	node := &inode{id: f.nextID, kind: kind, mode: mode, atime: now, mtime: now, nlink: 1}
	f.live[node] = struct{}{}
	return node, nil
}

func (f *FS) release(node *inode) {
	f.nodes--
	delete(f.live, node)
	node.data = nil
}

func (f *FS) Open(ctx context.Context, p string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	access := flags & 0b11
	create := flags&vmfs.Create != 0
	exclusive := flags&vmfs.Exclusive != 0
	truncate := flags&vmfs.Truncate != 0
	append := flags&vmfs.Append != 0
	if exclusive && !create {
		return nil, errno.Invalid
	}
	if append && access == 0 {
		return nil, errno.Invalid
	}
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
		node, err = f.newNode(kindFile, mode.Perm(), now)
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
			if node.mode.Perm()&0o400 == 0 {
				return nil, errno.Permission
			}
			node.opened++
			return &handle{fs: f, node: node}, nil
		case kindSymlink:
			return nil, errno.NotFound
		case kindFifo:

			return nil, errno.Wrap(errno.NotSupported, "客体没有命名管道的读写语义")
		}
		if access == 0 && node.mode.Perm()&0o400 == 0 || access != 0 && node.mode.Perm()&0o200 == 0 {
			return nil, errno.Permission
		}
	}
	if truncate && access != 0 {
		if len(node.data) > f.maxBytes {
			return nil, errno.NoSpace
		}
		node.data = node.data[:0]
		node.mtime = f.now()
	}
	node.opened++
	return &handle{fs: f, node: node, appendMode: append}, nil
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
	f.mu.RLock()
	defer f.mu.RUnlock()
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
	f.mu.RLock()
	defer f.mu.RUnlock()
	node, _, _, err := f.lookup(p, true)
	if err != nil {
		return nil, err
	}
	if node.kind != kindDir {
		return nil, errno.NotDirectory
	}
	if node.mode.Perm()&0o400 == 0 {
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
	f.mu.Lock()
	defer f.mu.Unlock()

	if node, _, _, err := f.lookup(p, false); err == nil {
		if node == f.root {
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
	node, err := f.newNode(kindDir, stdfs.ModeDir|mode.Perm(), f.now())
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
	f.mu.Lock()
	defer f.mu.Unlock()
	node, parent, name, err := f.lookup(p, false)
	if err != nil {
		return err
	}
	if node == f.root {
		return errno.Permission
	}
	if parent.mode.Perm()&0o200 == 0 {
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
	f.mu.Lock()
	defer f.mu.Unlock()
	src, srcParent, srcName, err := f.lookup(oldPath, false)
	if err != nil {
		return err
	}
	if src == f.root {
		return errno.Permission
	}
	dstParent, dstName, err := f.checkWritable(newPath)
	if err != nil {
		return err
	}
	if srcParent.mode.Perm()&0o200 == 0 || dstParent.mode.Perm()&0o200 == 0 {
		return errno.Permission
	}

	if srcParent == dstParent && srcName == dstName {
		return errno.Invalid
	}
	for dir := dstParent; dir != f.root; dir = dir.parent {
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
			return errno.IsDirectory
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

func (f *FS) Link(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	src, _, _, err := f.lookup(oldPath, true)
	if err != nil {
		return err
	}
	if src.kind != kindFile {
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.RLock()
	defer f.mu.RUnlock()
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
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	node, _, _, err := f.lookup(p, true)
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
	node, _, _, err := f.lookup(p, true)
	if err != nil {
		return err
	}
	node.mode = node.mode.Type() | vmfs.ModeBits(mode)
	node.mtime = f.now()
	return nil
}
