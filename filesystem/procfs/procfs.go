package procfs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"sort"
	"strings"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type Entry struct {
	ID         proc.PID
	Parent     proc.PID
	Generation uint64
	Program    string
	Args       []string
	Dir        string
	Env        map[string]string
	State      string
	ExitCode   uint8
	ExitSignal uint8
	PGID       proc.PID
	SID        proc.PID

	OpenFDs []uint32
}

type Provider interface {
	Processes(context.Context) ([]Entry, error)

	Self(context.Context) (proc.PID, error)
}

type FS struct {
	provider Provider
}

func New(provider Provider) (*FS, error) {
	if provider == nil {
		return nil, errno.Wrap(errno.Invalid, "procfs 需要进程视图来源")
	}
	return &FS{provider: provider}, nil
}

const (
	root        = "/"
	selfLink    = "self"
	fdDir       = "fd"
	statusName  = "status"
	cmdlineName = "cmdline"
)

func isProcPath(path string) bool { return true }

func splitPath(path string) ([]string, error) {
	if path == "" || path[0] != '/' {
		return nil, errno.Wrap(errno.Invalid, "路径必须是客体绝对路径: "+path)
	}
	if path == "/" {
		return nil, nil
	}
	trimmed := strings.TrimPrefix(path, "/")
	segments := strings.Split(trimmed, "/")
	for _, segment := range segments {
		if segment == "" {
			return nil, errno.Wrap(errno.Invalid, "路径含空段: "+path)
		}
	}
	return segments, nil
}

func parsePID(segment string) (proc.PID, bool) {
	if segment == "" {
		return 0, false
	}
	var value uint64
	for index := 0; index < len(segment); index++ {
		digit := segment[index]
		if digit < '0' || digit > '9' {
			return 0, false
		}
		value = value*10 + uint64(digit-'0')
		if value > 1<<40 {
			return 0, false
		}
	}
	if value == 0 {
		return 0, false
	}
	return proc.PID(value), true
}

func (f *FS) lookup(ctx context.Context, path string) (Entry, []string, error) {
	segments, err := splitPath(path)
	if err != nil {
		return Entry{}, nil, err
	}
	if len(segments) == 0 {

		return Entry{}, nil, errno.Wrap(errno.Invalid, "路径指向 /proc 根")
	}

	target := segments[0]
	if target == selfLink {
		self, err := f.provider.Self(ctx)
		if err != nil {
			return Entry{}, nil, err
		}
		target = itoa(uint64(self))
	}
	pid, ok := parsePID(target)
	if !ok {
		return Entry{}, nil, errno.Wrap(errno.NotFound, "没有该进程条目: "+segments[0])
	}
	entry, err := f.entryOf(ctx, pid)
	if err != nil {
		return Entry{}, nil, err
	}
	return entry, segments[1:], nil
}

func (f *FS) entryOf(ctx context.Context, pid proc.PID) (Entry, error) {
	entries, err := f.provider.Processes(ctx)
	if err != nil {
		return Entry{}, err
	}
	for _, entry := range entries {
		if entry.ID == pid {
			return entry, nil
		}
	}
	return Entry{}, errno.Wrap(errno.NotFound, "进程不存在: "+itoa(uint64(pid)))
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
	segments, err := splitPath(path)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return info{name: "proc", mode: stdfs.ModeDir | 0o555, size: 0}, nil
	}

	if segments[0] == selfLink {
		if !follow {

			return info{name: selfLink, mode: stdfs.ModeSymlink | 0o777}, nil
		}

		self, err := f.provider.Self(ctx)
		if err != nil {
			return nil, err
		}
		segments = append([]string{itoa(uint64(self))}, segments[1:]...)
	}
	entry, rest, err := f.lookup(ctx, "/"+strings.Join(segments, "/"))
	if err != nil {
		return nil, err
	}
	if len(rest) == 0 {
		return info{name: segments[0], mode: stdfs.ModeDir | 0o555}, nil
	}
	name := rest[0]
	switch {
	case name == statusName && len(rest) == 1:
		content := statusOf(entry)
		return info{name: statusName, mode: 0o444, size: int64(len(content))}, nil
	case name == cmdlineName && len(rest) == 1:
		content := cmdlineOf(entry)
		return info{name: cmdlineName, mode: 0o444, size: int64(len(content))}, nil
	case name == fdDir && len(rest) == 1:
		return info{name: fdDir, mode: stdfs.ModeDir | 0o555}, nil
	case name == fdDir && len(rest) == 2:
		if !containsFD(entry, rest[1]) {
			return nil, errno.Wrap(errno.NotFound, "描述符不存在: "+rest[1])
		}
		return info{name: rest[1], mode: stdfs.ModeSymlink | 0o777}, nil
	default:
		return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
	}
}

func (f *FS) ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	segments, err := splitPath(path)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		entries, err := f.provider.Processes(ctx)
		if err != nil {
			return nil, err
		}
		out := []stdfs.DirEntry{dirEntry{name: selfLink, mode: stdfs.ModeSymlink | 0o777}}
		for _, entry := range entries {
			out = append(out, dirEntry{name: itoa(uint64(entry.ID)), mode: stdfs.ModeDir | 0o555})
		}
		sortEntries(out)
		return out, nil
	}
	entry, rest, err := f.lookup(ctx, path)
	if err != nil {
		return nil, err
	}
	switch {
	case len(rest) == 0:
		out := []stdfs.DirEntry{
			dirEntry{name: fdDir, mode: stdfs.ModeDir | 0o555},
			dirEntry{name: cmdlineName, mode: 0o444},
			dirEntry{name: statusName, mode: 0o444},
		}
		sortEntries(out)
		return out, nil
	case len(rest) == 1 && rest[0] == fdDir:
		out := make([]stdfs.DirEntry, 0, len(entry.OpenFDs))
		for _, descriptor := range entry.OpenFDs {
			out = append(out, dirEntry{name: itoa(uint64(descriptor)), mode: stdfs.ModeSymlink | 0o777})
		}
		sortEntries(out)
		return out, nil
	default:
		return nil, errno.Wrap(errno.NotDirectory, "不是目录: "+path)
	}
}

func (f *FS) Readlink(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	segments, err := splitPath(path)
	if err != nil {
		return "", err
	}
	if len(segments) == 1 && segments[0] == selfLink {

		return f.selfTarget(ctx)
	}
	entry, rest, err := f.lookup(ctx, path)
	if err != nil {
		return "", err
	}
	if len(rest) == 2 && rest[0] == fdDir {
		if !containsFD(entry, rest[1]) {
			return "", errno.Wrap(errno.NotFound, "描述符不存在: "+rest[1])
		}

		return "fd:" + rest[1], nil
	}
	return "", errno.Wrap(errno.Invalid, "不是符号链接: "+path)
}

func (f *FS) selfTarget(ctx context.Context) (string, error) {
	self, err := f.provider.Self(ctx)
	if err != nil {
		return "", err
	}
	return itoa(uint64(self)), nil
}

func (f *FS) Open(ctx context.Context, path string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if flags&(vmfs.WriteOnly|vmfs.ReadWrite|vmfs.Create|vmfs.Truncate|vmfs.Append) != 0 {
		return nil, errno.Wrap(errno.Permission, "procfs 是只读视图: "+path)
	}
	segments, err := splitPath(path)
	if err != nil {
		return nil, err
	}

	if len(segments) == 0 {
		return nil, errno.Wrap(errno.IsDirectory, "是目录: "+path)
	}
	if len(segments) == 1 && segments[0] == selfLink {

		return nil, errno.Wrap(errno.IsDirectory, "self 是符号链接: "+path)
	}
	entry, rest, err := f.lookup(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(rest) == 0 {
		return nil, errno.Wrap(errno.IsDirectory, "是目录: "+path)
	}
	switch rest[0] {
	case statusName:
		if len(rest) != 1 {
			return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
		}
		return newFile(statusName, []byte(statusOf(entry))), nil
	case cmdlineName:
		if len(rest) != 1 {
			return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
		}
		return newFile(cmdlineName, []byte(cmdlineOf(entry))), nil
	default:
		return nil, errno.Wrap(errno.NotFound, "没有该条目: "+path)
	}
}

func (f *FS) Mkdir(ctx context.Context, path string, mode stdfs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errno.Wrap(errno.ReadOnlyFS, "procfs 是只读视图")
}

func (f *FS) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errno.Wrap(errno.ReadOnlyFS, "procfs 是只读视图")
}

func (f *FS) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errno.Wrap(errno.ReadOnlyFS, "procfs 是只读视图")
}

func (f *FS) Link(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errno.Wrap(errno.ReadOnlyFS, "procfs 是只读视图")
}

func (f *FS) Symlink(ctx context.Context, target, link string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errno.Wrap(errno.ReadOnlyFS, "procfs 是只读视图")
}

func statusOf(entry Entry) string {
	var builder strings.Builder
	builder.WriteString("Name:\t" + entry.Program + "\n")
	builder.WriteString("Pid:\t" + itoa(uint64(entry.ID)) + "\n")
	builder.WriteString("PPid:\t" + itoa(uint64(entry.Parent)) + "\n")
	builder.WriteString("Pgid:\t" + itoa(uint64(entry.PGID)) + "\n")
	builder.WriteString("Sid:\t" + itoa(uint64(entry.SID)) + "\n")
	builder.WriteString("State:\t" + entry.State + "\n")
	builder.WriteString("Generation:\t" + itoa(entry.Generation) + "\n")
	if entry.ExitCode != 0 || entry.ExitSignal != 0 {
		builder.WriteString("ExitCode:\t" + itoa(uint64(entry.ExitCode)) + "\n")
		builder.WriteString("ExitSignal:\t" + itoa(uint64(entry.ExitSignal)) + "\n")
	}
	return builder.String()
}

func cmdlineOf(entry Entry) string {
	parts := make([]string, 0, len(entry.Args)+1)
	parts = append(parts, entry.Program)
	parts = append(parts, entry.Args...)
	return strings.Join(parts, "\x00")
}

func containsFD(entry Entry, encoded string) bool {
	value, ok := parsePID(encoded)
	if !ok {
		return false
	}
	for _, descriptor := range entry.OpenFDs {
		if uint64(descriptor) == uint64(value) {
			return true
		}
	}
	return false
}

type info struct {
	name string
	mode stdfs.FileMode
	size int64
}

func (i info) Name() string         { return i.name }
func (i info) Size() int64          { return i.size }
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
	return info{name: d.name, mode: d.mode}, nil
}

func sortEntries(entries []stdfs.DirEntry) {
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})
}

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}

var (
	_ vmfs.System = (*FS)(nil)
	_             = errors.Is
	_             = isProcPath
)
