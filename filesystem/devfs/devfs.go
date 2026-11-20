package devfs

import (
	"context"
	"io"
	stdfs "io/fs"
	"sort"
	"strings"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const (
	nullName    = "null"
	zeroName    = "zero"
	randomName  = "random"
	urandomName = "urandom"
	ttyName     = "tty"

	fdDir = "fd"

	stdinName  = "stdin"
	stdoutName = "stdout"
	stderrName = "stderr"
)

type Terminal interface {
	io.Reader
	io.Writer
}

type Provider interface {
	Descriptor(ctx context.Context, id uint32) (io.ReadWriteCloser, error)
}

type Options struct {
	Entropy entropy.Source

	Terminal Terminal

	Provider Provider

	Owner uint32

	Mode map[string]stdfs.FileMode

	// Subdirs 暴露可读写的子目录(如 N09 的 /dev/shm),按名称委托给
	// 目标文件系统;其余设备节点仍只读。
	Subdirs map[string]vmfs.System
}

func defaultMode(name string) stdfs.FileMode {
	if name == ttyName {
		return 0o600
	}
	return 0o666
}

type FS struct {
	entropy  entropy.Source
	terminal Terminal
	provider Provider
	owner    uint32
	modes    map[string]stdfs.FileMode
	subdirs  map[string]vmfs.System
}

func New(options Options) (*FS, error) {
	if options.Entropy == nil {

		return nil, errno.Wrap(errno.Invalid, "devfs 需要注入熵源")
	}
	modes := make(map[string]stdfs.FileMode, len(options.Mode))
	for name, mode := range options.Mode {
		modes[name] = mode
	}
	subdirs := make(map[string]vmfs.System, len(options.Subdirs))
	for name, system := range options.Subdirs {
		if system != nil {
			subdirs[name] = system
		}
	}
	return &FS{
		entropy:  options.Entropy,
		terminal: options.Terminal,
		provider: options.Provider,
		owner:    options.Owner,
		modes:    modes,
		subdirs:  subdirs,
	}, nil
}

var deviceNames = []string{fdDir, nullName, randomName, stderrName, stdinName, stdoutName, ttyName, urandomName, zeroName}

var directoryNames = map[string]bool{fdDir: true}

func isDevice(name string) bool {
	for _, known := range deviceNames {
		if known == name {
			return true
		}
	}
	return false
}

func isDirectoryName(name string) bool { return directoryNames[name] }

func stdAlias(name string) (uint32, bool) {
	switch name {
	case stdinName:
		return 0, true
	case stdoutName:
		return 1, true
	case stderrName:
		return 2, true
	default:
		return 0, false
	}
}

func (f *FS) modeOf(name string) stdfs.FileMode {
	if mode, ok := f.modes[name]; ok {
		return mode.Perm()
	}
	return defaultMode(name)
}

func (f *FS) deviceMode(name string) stdfs.FileMode {
	return stdfs.ModeDevice | stdfs.ModeCharDevice | f.modeOf(name)
}

func splitPath(path string) ([]string, error) {
	if path == "" || path[0] != '/' {
		return nil, errno.Wrap(errno.Invalid, "路径必须是客体绝对路径: "+path)
	}
	if path == "/" {
		return nil, nil
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, segment := range segments {
		if segment == "" {
			return nil, errno.Wrap(errno.Invalid, "路径含空段: "+path)
		}
	}
	return segments, nil
}

func (f *FS) Stat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	return f.stat(ctx, path)
}

func (f *FS) Lstat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	return f.stat(ctx, path)
}

func (f *FS) stat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	segments, err := splitPath(path)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return info{name: "dev", mode: stdfs.ModeDir | 0o555}, nil
	}
	if system, ok := f.subdirs[segments[0]]; ok {
		if len(segments) == 1 {
			return info{name: segments[0], mode: stdfs.ModeDir | 0o755}, nil
		}
		return system.Stat(ctx, "/"+strings.Join(segments[1:], "/"))
	}
	name := segments[0]
	if !isDevice(name) {
		return nil, errno.Wrap(errno.NotFound, "没有该设备节点: "+path)
	}

	if name == fdDir {
		if len(segments) == 1 {
			return info{name: fdDir, mode: stdfs.ModeDir | 0o555}, nil
		}
		if len(segments) == 2 {

			if _, err := f.descriptorOf(ctx, segments[1]); err != nil {
				return nil, err
			}
			return info{name: segments[1], mode: 0o666}, nil
		}
		return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
	}
	if len(segments) != 1 {
		return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
	}

	if _, isAlias := stdAlias(name); isAlias {
		if _, err := f.descriptorOf(ctx, name); err != nil {
			return nil, err
		}
		return info{name: name, mode: 0o666}, nil
	}
	return info{name: name, mode: f.deviceMode(name)}, nil
}

func parseFD(segment string) (uint32, bool) {
	if segment == "" || len(segment) > 10 {
		return 0, false
	}
	var value uint32
	for index := 0; index < len(segment); index++ {
		digit := segment[index]
		if digit < '0' || digit > '9' {
			return 0, false
		}
		next := value*10 + uint32(digit-'0')
		if next < value {
			return 0, false
		}
		value = next
	}
	return value, true
}

func (f *FS) descriptorOf(ctx context.Context, segment string) (uint32, error) {
	id, err := f.resolveDescriptorID(segment)
	if err != nil {
		return 0, err
	}
	if f.provider == nil {
		return 0, errno.Wrap(errno.NoDevice, "没有描述符来源")
	}
	if _, err := f.provider.Descriptor(ctx, id); err != nil {
		return 0, err
	}
	return id, nil
}

func (f *FS) resolveDescriptorID(segment string) (uint32, error) {
	if id, ok := stdAlias(segment); ok {
		return id, nil
	}
	id, ok := parseFD(segment)
	if !ok {
		return 0, errno.Wrap(errno.NotFound, "非法描述符: "+segment)
	}
	return id, nil
}

func (f *FS) ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	segments, err := splitPath(path)
	if err != nil {
		return nil, err
	}
	if len(segments) == 1 && segments[0] == fdDir {
		return f.readFDDir(ctx)
	}
	if len(segments) != 0 {
		if system, ok := f.subdirs[segments[0]]; ok {
			return system.ReadDir(ctx, "/"+strings.Join(segments[1:], "/"))
		}
		return nil, errno.Wrap(errno.NotDirectory, "不是目录: "+path)
	}
	out := make([]stdfs.DirEntry, 0, len(deviceNames)+len(f.subdirs))
	for _, name := range deviceNames {
		if isDirectoryName(name) {
			out = append(out, dirEntry{name: name, mode: stdfs.ModeDir | 0o555})
			continue
		}
		out = append(out, dirEntry{name: name, mode: f.deviceMode(name)})
	}
	for name := range f.subdirs {
		out = append(out, dirEntry{name: name, mode: stdfs.ModeDir | 0o755})
	}
	sortEntries(out)
	return out, nil
}

func (f *FS) readFDDir(ctx context.Context) ([]stdfs.DirEntry, error) {
	lister, ok := f.provider.(interface {
		OpenDescriptors(context.Context) ([]uint32, error)
	})
	if f.provider == nil || !ok {
		return nil, errno.Wrap(errno.NotSupported, "描述符来源不支持列出打开的描述符")
	}
	ids, err := lister.OpenDescriptors(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]stdfs.DirEntry, 0, len(ids))
	for _, id := range ids {
		out = append(out, dirEntry{name: itoa(id), mode: stdfs.ModeSymlink | 0o777})
	}
	sortEntries(out)
	return out, nil
}

func splitMust(path string) []string {
	segments, _ := splitPath(path)
	return segments
}

func (f *FS) subdirPath(segments []string) (vmfs.System, string, bool) {
	if len(segments) == 0 {
		return nil, "", false
	}
	system, ok := f.subdirs[segments[0]]
	if !ok {
		return nil, "", false
	}
	return system, "/" + strings.Join(segments[1:], "/"), true
}

func itoa(value uint32) string {
	var buffer [10]byte
	index := len(buffer)
	if value == 0 {
		return "0"
	}
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}

func (f *FS) Open(ctx context.Context, path string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	segments, err := splitPath(path)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return nil, errno.Wrap(errno.IsDirectory, "是目录: "+path)
	}
	if system, ok := f.subdirs[segments[0]]; ok {
		return system.Open(ctx, "/"+strings.Join(segments[1:], "/"), flags, mode)
	}
	if !isDevice(segments[0]) {
		return nil, errno.Wrap(errno.NotFound, "没有该设备节点: "+path)
	}
	name := segments[0]

	if isDirectoryName(name) && len(segments) == 1 {
		return nil, errno.Wrap(errno.IsDirectory, "是目录: "+path)
	}

	if name == fdDir || stdAliasMatches(name) {
		var id uint32
		if name == fdDir {
			if len(segments) != 2 {
				return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
			}
			parsed, ok := parseFD(segments[1])
			if !ok {
				return nil, errno.Wrap(errno.NotFound, "非法描述符: "+segments[1])
			}
			id = parsed
		} else {
			if len(segments) != 1 {
				return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
			}
			id, _ = stdAlias(name)
		}
		return f.openDescriptor(ctx, id, flags)
	}
	if len(segments) != 1 {
		return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
	}
	if err := f.checkAccess(name, flags); err != nil {
		return nil, err
	}

	if name == ttyName && f.terminal == nil {
		return nil, errno.Wrap(errno.NoDevice, "没有控制终端")
	}
	return &file{fs: f, name: name, flags: flags}, nil
}

func stdAliasMatches(name string) bool {
	_, ok := stdAlias(name)
	return ok
}

func (f *FS) openDescriptor(ctx context.Context, id uint32, flags uint32) (vmfs.File, error) {
	if f.provider == nil {
		return nil, errno.Wrap(errno.NoDevice, "没有描述符来源")
	}
	stream, err := f.provider.Descriptor(ctx, id)
	if err != nil {
		return nil, err
	}
	return &descriptorFile{stream: stream, id: id, flags: flags}, nil
}

func (f *FS) checkAccess(name string, flags uint32) error {
	permissions := f.modeOf(name)
	writable := flags&(vmfs.WriteOnly|vmfs.ReadWrite|vmfs.Truncate|vmfs.Append) != 0
	if writable && permissions&0o222 == 0 {
		return errno.Wrap(errno.Permission, "设备节点不可写: "+name)
	}
	if !writable && permissions&0o444 == 0 {
		return errno.Wrap(errno.Permission, "设备节点不可读: "+name)
	}
	return nil
}

func (f *FS) Mkdir(ctx context.Context, path string, mode stdfs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	segments, err := splitPath(path)
	if err != nil {
		return err
	}
	if system, relative, ok := f.subdirPath(segments); ok {
		return system.Mkdir(ctx, relative, mode)
	}
	return errno.Wrap(errno.ReadOnlyFS, "devfs 是只读设备视图")
}

func (f *FS) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	segments, err := splitPath(path)
	if err != nil {
		return err
	}
	if system, relative, ok := f.subdirPath(segments); ok {
		return system.Remove(ctx, relative)
	}
	return errno.Wrap(errno.ReadOnlyFS, "devfs 是只读设备视图")
}

func (f *FS) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, fromRelative, ok := f.subdirPath(splitMust(from))
	target, toRelative, targetOK := f.subdirPath(splitMust(to))
	if ok && targetOK && source == target {
		return source.Rename(ctx, fromRelative, toRelative)
	}
	return errno.Wrap(errno.ReadOnlyFS, "devfs 是只读设备视图")
}

func (f *FS) Link(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, fromRelative, ok := f.subdirPath(splitMust(from))
	target, toRelative, targetOK := f.subdirPath(splitMust(to))
	if ok && targetOK && source == target {
		return source.Link(ctx, fromRelative, toRelative)
	}
	return errno.Wrap(errno.ReadOnlyFS, "devfs 是只读设备视图")
}

func (f *FS) Symlink(ctx context.Context, target, link string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	segments, err := splitPath(link)
	if err != nil {
		return err
	}
	if system, relative, ok := f.subdirPath(segments); ok {
		return system.Symlink(ctx, target, relative)
	}
	return errno.Wrap(errno.ReadOnlyFS, "devfs 是只读设备视图")
}

func (f *FS) Readlink(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	segments, err := splitPath(path)
	if err != nil {
		return "", err
	}
	if system, relative, ok := f.subdirPath(segments); ok {
		return system.Readlink(ctx, relative)
	}
	return "", errno.Wrap(errno.Invalid, "设备视图没有符号链接: "+path)
}

type info struct {
	name string
	mode stdfs.FileMode
}

func (i info) Name() string         { return i.name }
func (i info) Size() int64          { return 0 }
func (i info) Mode() stdfs.FileMode { return i.mode }
func (i info) ModTime() time.Time   { return time.Time{} }
func (i info) IsDir() bool          { return i.mode.IsDir() }
func (i info) Sys() any             { return nil }

type dirEntry struct {
	name string
	mode stdfs.FileMode
}

func (d dirEntry) Name() string         { return d.name }
func (d dirEntry) IsDir() bool          { return d.mode.IsDir() }
func (d dirEntry) Type() stdfs.FileMode { return d.mode.Type() }
func (d dirEntry) Info() (stdfs.FileInfo, error) {
	return info(d), nil
}

func sortEntries(entries []stdfs.DirEntry) {
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})
}

var _ vmfs.System = (*FS)(nil)
