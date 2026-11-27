package instance

import (
	"context"
	"fmt"
	"io"
	stdfs "io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// cgroupFS 是 cgroup v2 的只读视图:根 cgroup 与进程所在叶子
// /assistant-sandbox 的文件在每次打开时从内核进程表与资源计量生成。
// 它不实现控制组创建、迁移或写控制文件。
type cgroupFS struct {
	machine *VM
}

var cgroupFiles = []string{
	"cgroup.controllers",
	"cgroup.events",
	"cgroup.freeze",
	"cgroup.procs",
	"cgroup.subtree_control",
	"cgroup.type",
	"cpu.max",
	"cpu.stat",
	"memory.current",
	"memory.max",
	"pids.current",
	"pids.max",
}

func newCgroupFS(machine *VM) vmfs.System {
	return &cgroupFS{machine: machine}
}

func (c *cgroupFS) resolve(name string) (leaf bool, file string, err error) {
	if !strings.HasPrefix(name, "/") {
		return false, "", errno.Wrap(errno.Invalid, "cgroupfs 路径必须是绝对路径")
	}
	clean := path.Clean(name)
	switch {
	case clean == "/":
		return false, "", nil
	case strings.Count(clean, "/") == 1 && slices.Contains(cgroupFiles, strings.TrimPrefix(clean, "/")):
		return false, strings.TrimPrefix(clean, "/"), nil
	case clean == cgroupPath:
		return true, "", nil
	case strings.HasPrefix(clean, cgroupPath+"/"):
		file = strings.TrimPrefix(clean, cgroupPath+"/")
		if !strings.Contains(file, "/") && slices.Contains(cgroupFiles, file) {
			return true, file, nil
		}
	}
	return false, "", errno.Wrap(errno.NotFound, "没有该 cgroup 条目: "+name)
}

func (c *cgroupFS) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return c.stat(ctx, name)
}

func (c *cgroupFS) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return c.stat(ctx, name)
}

func (c *cgroupFS) stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	leaf, file, err := c.resolve(name)
	if err != nil {
		return nil, err
	}
	if file == "" {
		return cgroupInfo{name: "cgroup", mode: stdfs.ModeDir | 0o555}, nil
	}
	data, err := c.content(ctx, leaf, file)
	if err != nil {
		return nil, err
	}
	return cgroupInfo{name: file, mode: 0o444, size: int64(len(data))}, nil
}

func (c *cgroupFS) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	leaf, file, err := c.resolve(name)
	if err != nil {
		return nil, err
	}
	if file != "" || (leaf && path.Clean(name) != cgroupPath) {
		return nil, errno.Wrap(errno.NotDirectory, "不是目录: "+name)
	}
	entries := make([]stdfs.DirEntry, 0, len(cgroupFiles)+1)
	if !leaf {
		entries = append(entries, cgroupDirEntry{name: strings.TrimPrefix(cgroupPath, "/"), mode: stdfs.ModeDir | 0o555})
	}
	for _, item := range cgroupFiles {
		entries = append(entries, cgroupDirEntry{name: item, mode: 0o444})
	}
	slices.SortFunc(entries, func(a, b stdfs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, nil
}

func (c *cgroupFS) Open(ctx context.Context, name string, flags uint32, _ stdfs.FileMode) (vmfs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if flags&(vmfs.WriteOnly|vmfs.ReadWrite|vmfs.Create|vmfs.Truncate|vmfs.Append) != 0 {
		return nil, errno.Wrap(errno.ReadOnlyFS, "cgroupfs 是只读视图")
	}
	leaf, file, err := c.resolve(name)
	if err != nil {
		return nil, err
	}
	if file == "" {
		entries, err := c.ReadDir(ctx, name)
		if err != nil {
			return nil, err
		}
		return &cgroupDirHandle{name: "cgroup", entries: entries}, nil
	}
	data, err := c.content(ctx, leaf, file)
	if err != nil {
		return nil, err
	}
	return &cgroupFile{name: file, reader: strings.NewReader(string(data))}, nil
}

func (c *cgroupFS) content(ctx context.Context, leaf bool, name string) ([]byte, error) {
	root := !leaf
	switch name {
	case "cgroup.controllers":
		return []byte("cpu memory pids\n"), nil
	case "cgroup.events":
		return []byte("populated 1\nfrozen 0\n"), nil
	case "cgroup.freeze":
		return []byte("0\n"), nil
	case "cgroup.procs":
		if root {
			return nil, nil
		}
		processes := c.machine.kernel.Processes()
		pids := make([]uint64, 0, len(processes))
		for _, process := range processes {
			pids = append(pids, uint64(process.ID))
		}
		slices.Sort(pids)
		var builder strings.Builder
		for _, pid := range pids {
			fmt.Fprintf(&builder, "%d\n", pid)
		}
		return []byte(builder.String()), nil
	case "cgroup.subtree_control":
		if root {
			return []byte("cpu memory pids\n"), nil
		}
		return nil, nil
	case "cgroup.type":
		return []byte("domain\n"), nil
	case "cpu.max":
		return []byte("max 100000\n"), nil
	case "cpu.stat":
		return []byte("usage_usec 0\nuser_usec 0\nsystem_usec 0\n"), nil
	case "memory.current":
		if root || c.machine.meters == nil {
			return []byte("0\n"), nil
		}
		view, err := c.machine.meters.SessionView(ctx)
		if err != nil {
			return nil, err
		}
		return append(strconv.AppendUint(nil, view.MemoryUsed, 10), '\n'), nil
	case "memory.max":
		if root {
			return []byte("max\n"), nil
		}
		return append(strconv.AppendUint(nil, c.machine.memoryBytes, 10), '\n'), nil
	case "pids.current":
		if root {
			return []byte("0\n"), nil
		}
		processes := c.machine.kernel.Processes()
		return append(strconv.AppendInt(nil, int64(len(processes)), 10), '\n'), nil
	case "pids.max":
		return []byte("max\n"), nil
	default:
		return nil, errno.Wrap(errno.NotFound, "没有该 cgroup 文件: "+name)
	}
}

func (c *cgroupFS) Mkdir(context.Context, string, stdfs.FileMode) error {
	return errno.Wrap(errno.ReadOnlyFS, "cgroupfs 是只读视图")
}

func (c *cgroupFS) Remove(context.Context, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "cgroupfs 是只读视图")
}

func (c *cgroupFS) Rename(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "cgroupfs 是只读视图")
}

func (c *cgroupFS) Link(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "cgroupfs 是只读视图")
}

func (c *cgroupFS) Symlink(context.Context, string, string) error {
	return errno.Wrap(errno.ReadOnlyFS, "cgroupfs 是只读视图")
}

func (c *cgroupFS) Readlink(context.Context, string) (string, error) {
	return "", errno.Wrap(errno.Invalid, "cgroupfs 没有符号链接")
}

type cgroupInfo struct {
	name string
	mode stdfs.FileMode
	size int64
}

func (i cgroupInfo) Name() string         { return i.name }
func (i cgroupInfo) Size() int64          { return i.size }
func (i cgroupInfo) Mode() stdfs.FileMode { return i.mode }
func (i cgroupInfo) ModTime() time.Time   { return time.Time{} }
func (i cgroupInfo) IsDir() bool          { return i.mode.IsDir() }
func (i cgroupInfo) Sys() any             { return nil }

type cgroupDirEntry struct {
	name string
	mode stdfs.FileMode
}

func (e cgroupDirEntry) Name() string         { return e.name }
func (e cgroupDirEntry) IsDir() bool          { return e.mode.IsDir() }
func (e cgroupDirEntry) Type() stdfs.FileMode { return e.mode.Type() }
func (e cgroupDirEntry) Info() (stdfs.FileInfo, error) {
	return cgroupInfo{name: e.name, mode: e.mode}, nil
}

type cgroupFile struct {
	name   string
	reader *strings.Reader
}

func (f *cgroupFile) Read(buffer []byte) (int, error) { return f.reader.Read(buffer) }
func (f *cgroupFile) Write([]byte) (int, error) {
	return 0, errno.Wrap(errno.ReadOnlyFS, "cgroupfs 是只读视图")
}
func (f *cgroupFile) Seek(offset int64, whence int) (int64, error) {
	return f.reader.Seek(offset, whence)
}
func (f *cgroupFile) Close() error { return nil }
func (f *cgroupFile) Stat() (stdfs.FileInfo, error) {
	return cgroupInfo{name: f.name, mode: 0o444, size: f.reader.Size()}, nil
}

type cgroupDirHandle struct {
	name    string
	entries []stdfs.DirEntry
	offset  int
}

func (d *cgroupDirHandle) Readdir(limit int) ([]stdfs.DirEntry, error) {
	if limit <= 0 || d.offset >= len(d.entries) {
		return nil, nil
	}
	end := min(d.offset+limit, len(d.entries))
	batch := append([]stdfs.DirEntry(nil), d.entries[d.offset:end]...)
	d.offset = end
	return batch, nil
}

func (d *cgroupDirHandle) Read([]byte) (int, error) {
	return 0, errno.Wrap(errno.IsDirectory, "是目录: "+d.name)
}

func (d *cgroupDirHandle) Write([]byte) (int, error) {
	return 0, errno.Wrap(errno.ReadOnlyFS, "cgroupfs 是只读视图")
}

func (d *cgroupDirHandle) Seek(offset int64, whence int) (int64, error) {
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

func (d *cgroupDirHandle) Close() error { return nil }
func (d *cgroupDirHandle) Stat() (stdfs.FileInfo, error) {
	return cgroupInfo{name: d.name, mode: stdfs.ModeDir | 0o555}, nil
}
