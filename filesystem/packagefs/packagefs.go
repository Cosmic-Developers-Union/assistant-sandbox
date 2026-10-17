package packagefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	pathpkg "path"
	"slices"
	"strings"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/digest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const Version = 1

const maxSymlinkDepth = 8

type Kind uint8

const (
	KindDirectory Kind = iota + 1

	KindFile

	KindSymlink
)

func (k Kind) String() string {
	switch k {
	case KindDirectory:
		return "directory"
	case KindFile:
		return "file"
	case KindSymlink:
		return "symlink"
	default:
		return fmt.Sprintf("kind(%d)", uint8(k))
	}
}

type Entry struct {
	Path string

	Kind Kind

	Mode stdfs.FileMode

	Size int64

	Digest string

	Target string
}

type Manifest struct {
	Version int
	Entries []Entry
}

type Source interface {
	Open(ctx context.Context, identity string) (io.ReadCloser, error)
}

type node struct {
	kind     Kind
	mode     stdfs.FileMode
	size     int64
	digest   string
	target   string
	children map[string]*node

	implicit bool
}

type FS struct {
	root   *node
	source Source
}

func New(manifest Manifest, source Source) (*FS, error) {
	if source == nil {
		return nil, errno.Wrap(errno.Invalid, "packagefs 需要内容来源")
	}
	if manifest.Version != Version {
		return nil, errno.Wrap(errno.Version,
			fmt.Sprintf("清单版本 %d 不是 %d", manifest.Version, Version))
	}
	fsys := &FS{
		root:   &node{kind: KindDirectory, mode: 0o755, children: map[string]*node{}},
		source: source,
	}
	for index, entry := range manifest.Entries {
		if err := fsys.insert(entry); err != nil {
			return nil, fmt.Errorf("清单第 %d 条 %q: %w", index, entry.Path, err)
		}
	}
	return fsys, nil
}

func (f *FS) insert(entry Entry) error {
	components, err := splitPath(entry.Path)
	if err != nil {
		return err
	}
	if len(components) == 0 {
		return errno.Wrap(errno.Invalid, "清单里不能有根目录条目")
	}
	switch entry.Kind {
	case KindFile:
		if entry.Digest == "" {
			return errno.Wrap(errno.Invalid, "文件条目缺少内容身份")
		}
		if entry.Size < 0 {
			return errno.Wrap(errno.Invalid, "文件条目大小不能为负")
		}
		if entry.Target != "" {
			return errno.Wrap(errno.Invalid, "文件条目不应有链接目标")
		}
	case KindSymlink:
		if entry.Target == "" {
			return errno.Wrap(errno.Invalid, "符号链接条目缺少目标")
		}
		if entry.Digest != "" {
			return errno.Wrap(errno.Invalid, "符号链接条目不应有内容身份")
		}
	case KindDirectory:
		if entry.Digest != "" || entry.Target != "" {
			return errno.Wrap(errno.Invalid, "目录条目不应有内容身份或链接目标")
		}
	default:
		return errno.Wrap(errno.Invalid, "未知条目种类")
	}
	directory := f.root
	for _, name := range components[:len(components)-1] {
		child, ok := directory.children[name]
		if !ok {

			child = &node{kind: KindDirectory, mode: 0o755, children: map[string]*node{}, implicit: true}
			directory.children[name] = child
		}
		if child.kind != KindDirectory {
			return errno.Wrap(errno.NotDirectory, "路径前缀不是目录: "+name)
		}
		directory = child
	}
	name := components[len(components)-1]
	existing, ok := directory.children[name]
	if ok {

		if !existing.implicit || entry.Kind != KindDirectory {
			return errno.Wrap(errno.Exist, "路径重复: "+entry.Path)
		}
		existing.implicit = false
		existing.mode = entry.Mode
		return nil
	}
	mode := entry.Mode
	if entry.Kind == KindDirectory && mode == 0 {
		mode = 0o755
	}
	directory.children[name] = &node{
		kind:     entry.Kind,
		mode:     mode,
		size:     entry.Size,
		digest:   entry.Digest,
		target:   entry.Target,
		children: map[string]*node{},
	}
	return nil
}

func splitPath(path string) ([]string, error) {
	if path == "" || path[0] != '/' {
		return nil, errno.Wrap(errno.Invalid, "路径必须是客体绝对路径: "+path)
	}
	components := make([]string, 0, 4)
	for _, part := range strings.Split(path, "/") {
		switch part {
		case "", ".":
		case "..":
			return nil, errno.Wrap(errno.Invalid, "路径不接受 .. 组件: "+path)
		default:
			components = append(components, part)
		}
	}
	return components, nil
}

func (f *FS) walk(path string, followLast bool, depth int) (*node, error) {
	if depth > maxSymlinkDepth {
		return nil, errno.Wrap(errno.LinkLoop, "符号链接解析过深: "+path)
	}
	components, err := splitPath(path)
	if err != nil {
		return nil, err
	}
	current := f.root
	resolved := ""
	for index, name := range components {
		child, ok := current.children[name]
		if !ok {
			return nil, errno.Wrap(errno.NotFound, "路径不存在: "+path)
		}
		last := index == len(components)-1
		if child.kind == KindSymlink && (!last || followLast) {

			combined := pathpkg.Clean(resolved + "/" + child.target)
			if rest := components[index+1:]; len(rest) > 0 {
				combined = pathpkg.Clean(combined + "/" + strings.Join(rest, "/"))
			}
			return f.walk(combined, followLast, depth+1)
		}
		resolved += "/" + name
		current = child
	}
	return current, nil
}

func (f *FS) Open(ctx context.Context, path string, flags uint32, _ stdfs.FileMode) (opened vmfs.File, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if flags&(vmfs.WriteOnly|vmfs.ReadWrite|vmfs.Create|vmfs.Truncate|vmfs.Append) != 0 {
		return nil, errno.Wrap(errno.ReadOnlyFS, "packagefs 是只读镜像")
	}
	target, err := f.walk(path, true, 0)
	if err != nil {
		return nil, err
	}
	switch target.kind {
	case KindDirectory:
		return nil, errno.Wrap(errno.IsDirectory, "路径是目录: "+path)
	case KindFile:
	default:
		return nil, errno.Wrap(errno.Invalid, "不可打开的对象: "+path)
	}
	if target.mode.Perm()&0o400 == 0 {
		return nil, errno.Wrap(errno.Permission, "文件不可读: "+path)
	}
	reader, err := f.source.Open(ctx, target.digest)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, reader.Close())
	}()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	if int64(len(data)) != target.size {
		return nil, errno.Wrap(errno.Version,
			fmt.Sprintf("内容长度与清单不符: %s 声明 %d 字节，实际 %d", path, target.size, len(data)))
	}
	if identity := digest.Content(data); identity != target.digest {
		return nil, errno.Wrap(errno.Version, "内容身份与清单不符: "+path)
	}
	return &file{info: fileInfo{name: pathpkg.Base(path), node: target}, reader: bytes.NewReader(data)}, nil
}

func (f *FS) Stat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	return f.stat(ctx, path, true)
}

func (f *FS) Lstat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	return f.stat(ctx, path, false)
}

func (f *FS) stat(ctx context.Context, path string, follow bool) (stdfs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := f.walk(path, follow, 0)
	if err != nil {
		return nil, err
	}
	return fileInfo{name: pathpkg.Base(path), node: target}, nil
}

func (f *FS) ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := f.walk(path, true, 0)
	if err != nil {
		return nil, err
	}
	if target.kind != KindDirectory {
		return nil, errno.Wrap(errno.NotDirectory, "路径不是目录: "+path)
	}
	names := make([]string, 0, len(target.children))
	for name := range target.children {
		names = append(names, name)
	}
	slices.Sort(names)
	entries := make([]stdfs.DirEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, dirEntry{name: name, node: target.children[name]})
	}
	return entries, nil
}

func (f *FS) Readlink(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target, err := f.walk(path, false, 0)
	if err != nil {
		return "", err
	}
	if target.kind != KindSymlink {
		return "", errno.Wrap(errno.Invalid, "路径不是符号链接: "+path)
	}
	return target.target, nil
}

func (f *FS) Mkdir(context.Context, string, stdfs.FileMode) error {
	return errno.Wrap(errno.ReadOnlyFS, "packagefs 是只读镜像")
}

func (f *FS) Remove(context.Context, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "packagefs 是只读镜像")
}

func (f *FS) Rename(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "packagefs 是只读镜像")
}

func (f *FS) Link(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "packagefs 是只读镜像")
}

func (f *FS) Symlink(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "packagefs 是只读镜像")
}

type file struct {
	info   fileInfo
	reader *bytes.Reader
	closed bool
}

func (f *file) Read(buffer []byte) (int, error) {
	if f.closed {
		return 0, errno.Wrap(errno.BadHandle, "文件已关闭")
	}
	return f.reader.Read(buffer)
}

func (f *file) Write([]byte) (int, error) {
	return 0, errno.Wrap(errno.ReadOnlyFS, "packagefs 是只读镜像")
}

func (f *file) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, errno.Wrap(errno.BadHandle, "文件已关闭")
	}
	return f.reader.Seek(offset, whence)
}

func (f *file) Close() error {
	f.closed = true
	return nil
}

func (f *file) Stat() (stdfs.FileInfo, error) { return f.info, nil }

type fileInfo struct {
	name string
	node *node
}

func (i fileInfo) Name() string { return i.name }

func (i fileInfo) Size() int64 {
	if i.node.kind != KindFile {
		return 0
	}
	return i.node.size
}

func (i fileInfo) Mode() stdfs.FileMode {
	switch i.node.kind {
	case KindDirectory:
		return stdfs.ModeDir | i.node.mode.Perm()
	case KindSymlink:
		return stdfs.ModeSymlink | i.node.mode.Perm()
	default:
		return i.node.mode.Perm()
	}
}

func (i fileInfo) ModTime() time.Time { return time.Time{} }

func (i fileInfo) IsDir() bool { return i.node.kind == KindDirectory }

func (i fileInfo) Sys() any { return nil }

type dirEntry struct {
	name string
	node *node
}

func (e dirEntry) Name() string { return e.name }

func (e dirEntry) IsDir() bool { return e.node.kind == KindDirectory }

func (e dirEntry) Type() stdfs.FileMode {
	switch e.node.kind {
	case KindDirectory:
		return stdfs.ModeDir
	case KindSymlink:
		return stdfs.ModeSymlink
	default:
		return 0
	}
}

func (e dirEntry) Info() (stdfs.FileInfo, error) {
	return fileInfo(e), nil
}

var _ vmfs.System = (*FS)(nil)
