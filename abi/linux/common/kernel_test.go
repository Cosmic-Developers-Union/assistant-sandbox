package common

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func TestProcessFilesAgainstRealFileSystem(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(ctx, "/data", 0o755); err != nil {
		t.Fatal(err)
	}
	create, err := system.Open(ctx, "/data/note.txt", vmfs.Create|1, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if err := create.Close(); err != nil {
		t.Fatal(err)
	}
	const pid = 9
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/data"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}

	id, err := files.Open(ctx, OpenRequest{Path: "note.txt", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	if id < 3 {
		t.Fatalf("描述符编号=%d：不应占用标准流编号", id)
	}
	buffer := make([]byte, 8)
	if loaded, err := descriptors.Read(ctx, id, buffer); err != nil || loaded != 8 {
		t.Fatalf("读=%d err=%v", loaded, err)
	}
	if string(buffer) != "12345678" {
		t.Fatalf("内容=%q", buffer)
	}
	if err := descriptors.Close(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Read(ctx, id, buffer); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后读取=%v want EBADF", err)
	}

	if _, err := files.Open(ctx, OpenRequest{Path: "/data/note.txt", Flags: OpenFlags{Mode: OpenRead}}); err != nil {
		t.Fatal(err)
	}

	if _, err := files.Open(ctx, OpenRequest{Path: "missing", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失路径=%v want ENOENT", err)
	}

	created, err := files.Open(ctx, OpenRequest{Path: "new.txt", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o640})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Write(ctx, created, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := descriptors.Close(ctx, created); err != nil {
		t.Fatal(err)
	}
	reopened, err := files.Open(ctx, OpenRequest{Path: "new.txt", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := descriptors.Read(ctx, reopened, buffer[:2])
	if err != nil || loaded != 2 || string(buffer[:2]) != "hi" {
		t.Fatalf("重开读=%d %q err=%v", loaded, buffer[:2], err)
	}

	if _, err := files.Open(ctx, OpenRequest{Path: "new.txt", Flags: OpenFlags{Mode: OpenWrite, Create: true, Exclusive: true}}); !errors.Is(err, errno.Exist) {
		t.Fatalf("O_EXCL 已存在=%v want EEXIST", err)
	}

	flagged, err := files.Open(ctx, OpenRequest{Path: "note.txt", Flags: OpenFlags{Mode: OpenRead, CloseOnExec: true}})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := table.CloseCloexec(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || uint64(closed[0]) != flagged {
		t.Fatalf("cloexec 回收=%v flagged=%d", closed, flagged)
	}
}

func TestProcessFilesRejectsUnwired(t *testing.T) {
	ctx := context.Background()
	if _, err := (ProcessFiles{}).Open(ctx, OpenRequest{Path: "/x", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("err=%v want ENOSYS", err)
	}
	if _, err := (ProcessFiles{Store: fd.New()}).Open(ctx, OpenRequest{Path: "/x", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("err=%v want ENOSYS", err)
	}

	table := fd.New()
	files := ProcessFiles{System: &openingSystem{file: &recordingFile{}}, Store: table, PID: 404}
	if _, err := files.Open(ctx, OpenRequest{Path: "/x", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("err=%v want ESRCH", err)
	}
}

func TestProcessFilesClosesFileWhenInstallFails(t *testing.T) {
	file := &recordingFile{}
	files := ProcessFiles{System: &openingSystem{file: file}, Store: failingStore{err: errno.Limit}, PID: 1}
	if _, err := files.Open(context.Background(), OpenRequest{Path: "/x", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.Limit) {
		t.Fatalf("err=%v want EAGAIN", err)
	}
	if !file.closed {
		t.Fatal("安装失败后文件没有被关闭")
	}
}

func TestProcessFilesClosesFileWhenCloexecFails(t *testing.T) {
	file := &recordingFile{}
	store := &cloexecFailStore{err: errno.Range}
	files := ProcessFiles{System: &openingSystem{file: file}, Store: store, PID: 1}
	_, err := files.Open(context.Background(), OpenRequest{
		Path: "/x", Flags: OpenFlags{Mode: OpenRead, CloseOnExec: true},
	})
	if !errors.Is(err, errno.Range) {
		t.Fatalf("err=%v want ERANGE", err)
	}

	if !store.closed || store.id != 3 {
		t.Fatalf("回滚=%v id=%d", store.closed, store.id)
	}
}

type openingSystem struct {
	file vmfs.File
	err  error
}

func (s *openingSystem) Open(context.Context, string, uint32, stdfs.FileMode) (vmfs.File, error) {
	return s.file, s.err
}

func (s *openingSystem) Stat(context.Context, string) (stdfs.FileInfo, error) {
	return nil, errno.NotImplemented
}

func (s *openingSystem) Lstat(context.Context, string) (stdfs.FileInfo, error) {
	return nil, errno.NotImplemented
}

func (s *openingSystem) ReadDir(context.Context, string) ([]stdfs.DirEntry, error) {
	return nil, errno.NotImplemented
}

func (s *openingSystem) Mkdir(context.Context, string, stdfs.FileMode) error {
	return errno.NotImplemented
}

func (s *openingSystem) Remove(context.Context, string) error { return errno.NotImplemented }

func (s *openingSystem) Rename(context.Context, string, string) error { return errno.NotImplemented }

func (s *openingSystem) Link(context.Context, string, string) error { return errno.NotImplemented }

func (s *openingSystem) Symlink(context.Context, string, string) error { return errno.NotImplemented }

func (s *openingSystem) Readlink(context.Context, string) (string, error) {
	return "", errno.NotImplemented
}

type recordingFile struct {
	buffer bytes.Buffer
	closed bool
}

func (f *recordingFile) Read(buffer []byte) (int, error)  { return f.buffer.Read(buffer) }
func (f *recordingFile) Write(buffer []byte) (int, error) { return f.buffer.Write(buffer) }
func (f *recordingFile) Seek(int64, int) (int64, error)   { return 0, errno.Invalid }
func (f *recordingFile) Stat() (stdfs.FileInfo, error)    { return nil, errno.Invalid }
func (f *recordingFile) Close() error {
	f.closed = true
	return nil
}

type failingStore struct{ err error }

func (s failingStore) InstallNext(uint64, fd.Stream) (uint32, error) { return 0, s.err }
func (s failingStore) SetCloexec(uint64, uint32, bool) error         { return nil }
func (s failingStore) Close(context.Context, uint64, uint32) error   { return nil }

type cloexecFailStore struct {
	err    error
	closed bool
	id     uint32
}

func (s *cloexecFailStore) InstallNext(uint64, fd.Stream) (uint32, error) { return 3, nil }

func (s *cloexecFailStore) SetCloexec(uint64, uint32, bool) error { return s.err }

func (s *cloexecFailStore) Close(_ context.Context, _ uint64, id uint32) error {
	s.closed = true
	s.id = id
	return nil
}

func TestProcessDescriptorsSeek(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := system.Open(ctx, "/seek.txt", vmfs.Create|1, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	const pid = 5
	table := fd.New()
	table.Attach(pid, fd.Streams{Input: &bufferStream{input: []byte("stdin")}})
	files := ProcessFiles{System: system, Store: table, PID: pid}
	id, err := files.Open(ctx, OpenRequest{Path: "/seek.txt", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	if offset, err := descriptors.Seek(ctx, id, 2, SeekSet); err != nil || offset != 2 {
		t.Fatalf("SEEK_SET: offset=%d err=%v", offset, err)
	}
	buffer := make([]byte, 2)
	loaded, err := descriptors.Read(ctx, id, buffer)
	if err != nil || loaded != 2 || string(buffer) != "cd" {
		t.Fatalf("定位后读=%d %q err=%v", loaded, buffer, err)
	}
	if offset, err := descriptors.Seek(ctx, id, -1, SeekEnd); err != nil || offset != 5 {
		t.Fatalf("SEEK_END: offset=%d err=%v", offset, err)
	}

	if _, err := descriptors.Seek(ctx, 0, 0, SeekSet); !errors.Is(err, errno.Seek) {
		t.Fatalf("标准流定位=%v want ESPIPE", err)
	}
}

func TestProcessDescriptorsRejectsOversizedAndUnknown(t *testing.T) {
	ctx := context.Background()
	const pid = 7
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	if err := descriptors.Close(ctx, math.MaxUint32+1); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭越界编号=%v want EBADF", err)
	}
	if err := descriptors.Close(ctx, 99); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭未打开的描述符=%v want EBADF", err)
	}
	if err := descriptors.Close(ctx, 0); err != nil {
		t.Fatalf("关闭标准流=%v", err)
	}
	if _, err := descriptors.Seek(ctx, 99, 0, SeekSet); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("定位未打开的描述符=%v want EBADF", err)
	}

	other := ProcessDescriptors{Table: table, PID: pid + 1}
	if _, err := other.Seek(ctx, 0, 0, SeekSet); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未挂载的进程=%v want ESRCH", err)
	}
	if err := (ProcessDescriptors{PID: pid}).Close(ctx, 0); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("空表关闭=%v want ENOSYS", err)
	}
}

func TestGetdents64WalksDirectoryHandleWithCursor(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(ctx, "/walk", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		file, err := system.Open(ctx, "/walk/"+name, vmfs.Create|1, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := system.Mkdir(ctx, "/walk/nested", 0o755); err != nil {
		t.Fatal(err)
	}

	const pid = 21
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors, Files: files}

	dirfd, err := files.Open(ctx, OpenRequest{Path: "/walk", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatalf("打开目录: %v", err)
	}

	// 小缓冲区只装一条记录,游标随调用推进且不重复。
	seen := map[string]bool{}
	type record struct {
		ino, off uint64
		dt       byte
		name     string
	}
	var records []record
	for round := 0; round < 3; round++ {
		reply := syscalls.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{dirfd, 0x2000, 32}})
		if reply.Errno != "" {
			t.Fatalf("第 %d 轮: %+v", round, reply.Errno)
		}
		if reply.Value == 0 {
			break
		}
		raw := make([]byte, reply.Value)
		if err := syscalls.Memory.Read(ctx, 0x2000, raw); err != nil {
			t.Fatal(err)
		}
		for cursor := 0; cursor < len(raw); {
			reclen := int(binary.LittleEndian.Uint16(raw[cursor+16:]))
			records = append(records, record{
				ino:  binary.LittleEndian.Uint64(raw[cursor:]),
				off:  binary.LittleEndian.Uint64(raw[cursor+8:]),
				dt:   raw[cursor+18],
				name: cstring(raw[cursor+19 : cursor+reclen]),
			})
			cursor += reclen
		}
	}
	if len(records) != 3 {
		t.Fatalf("应读到 3 条目录项: %+v", records)
	}
	for _, want := range []record{{dt: 4, name: "nested"}, {dt: 8, name: "one.txt"}, {dt: 8, name: "two.txt"}} {
		found := false
		for _, got := range records {
			if got.name == want.name {
				found = true
				if got.dt != want.dt {
					t.Fatalf("%s d_type=%d want %d", want.name, got.dt, want.dt)
				}
				if got.ino == 0 || got.off != got.ino {
					t.Fatalf("%s ino/off=%d/%d", want.name, got.ino, got.off)
				}
				if seen[want.name] {
					t.Fatalf("%s 重复出现", want.name)
				}
				seen[want.name] = true
			}
		}
		if !found {
			t.Fatalf("缺少目录项 %s: %+v", want.name, records)
		}
	}

	// lseek 到 0 回卷目录游标,再次读出全部条目。
	if _, err := descriptors.Seek(ctx, dirfd, 0, SeekSet); err != nil {
		t.Fatalf("回卷: %v", err)
	}
	reply := syscalls.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{dirfd, 0x2000, 256}})
	if reply.Errno != "" || reply.Value == 0 {
		t.Fatalf("回卷后应重新读到条目: %+v", reply)
	}

	// 普通文件句柄不是目录读取入口(Linux 语义 ENOTDIR)。
	filefd, err := files.Open(ctx, OpenRequest{Path: "/walk/one.txt", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{filefd, 0x2000, 256}}); reply.Errno != errno.NotDirectory {
		t.Fatalf("非目录句柄: %+v want ENOTDIR", reply)
	}
}

func TestUtimensatAndFtruncateAgainstRealFileSystem(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const pid = 31
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	clock := &fakeClock{wall: 7_500_000_000}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors, Files: files, Clock: clock}

	filefd, err := files.Open(ctx, OpenRequest{Path: "/note", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Write(ctx, filefd, []byte("hello world")); err != nil {
		t.Fatal(err)
	}

	// ftruncate 扩张与收缩。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFtruncate, Args: [6]uint64{filefd, 20}}); reply.Errno != "" {
		t.Fatalf("扩张: %+v", reply)
	}
	info, err := descriptors.Get(ctx, filefd)
	if err != nil {
		t.Fatal(err)
	}
	if stat, err := info.(interface {
		Stat() (stdfs.FileInfo, error)
	}).Stat(); err != nil || stat.Size() != 20 {
		t.Fatalf("扩张后尺寸=%d err=%v", stat.Size(), err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFtruncate, Args: [6]uint64{filefd, 5}}); reply.Errno != "" {
		t.Fatalf("收缩: %+v", reply)
	}
	if _, err := descriptors.Seek(ctx, filefd, 0, SeekSet); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	loaded, err := descriptors.Read(ctx, filefd, buffer)
	if err != nil || loaded != 5 || string(buffer[:5]) != "hello" {
		t.Fatalf("读=%d err=%v", loaded, err)
	}

	// fsync/fdatasync 经句柄能力成功。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFsync, Args: [6]uint64{filefd}}); reply.Errno != "" {
		t.Fatalf("fsync: %+v", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFdatasync, Args: [6]uint64{filefd}}); reply.Errno != "" {
		t.Fatalf("fdatasync: %+v", reply)
	}

	// utimensat 路径形态:UTIME_NOW + UTIME_OMIT。
	var spec [32]byte
	binary.LittleEndian.PutUint64(spec[8:], utimeNow)
	binary.LittleEndian.PutUint64(spec[16:], 0)  // mtime 秒
	binary.LittleEndian.PutUint64(spec[24:], 88) // mtime 纳秒
	if err := syscalls.Memory.Write(ctx, 0x2000, spec[:]); err != nil {
		t.Fatal(err)
	}
	writeString(syscalls.Memory.(*fakeMemory), 0x1000, "/note")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0x2000}}); reply.Errno != "" {
		t.Fatalf("utimensat: %+v", reply)
	}
	if atime, err := system.AccessTime(ctx, "/note"); err != nil || atime != 7_500_000_000 {
		t.Fatalf("atime=%d err=%v", atime, err)
	}
	if stat, err := system.Stat(ctx, "/note"); err != nil || stat.ModTime().UnixNano() != 88 {
		t.Fatalf("mtime=%v err=%v", stat.ModTime().UnixNano(), err)
	}

	// futimens 形态:NULL 路径作用于描述符。
	var fdSpec [32]byte
	binary.LittleEndian.PutUint64(fdSpec[0:], 5)  // atime 秒
	binary.LittleEndian.PutUint64(fdSpec[24:], 7) // mtime 纳秒
	if err := syscalls.Memory.Write(ctx, 0x2100, fdSpec[:]); err != nil {
		t.Fatal(err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{filefd, 0, 0x2100}}); reply.Errno != "" {
		t.Fatalf("futimens: %+v", reply)
	}
	if atime, err := system.AccessTime(ctx, "/note"); err != nil || atime != 5*int64(time.Second) {
		t.Fatalf("fd atime=%d err=%v", atime, err)
	}
}

func TestStatfsAgainstRealQuota(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{TotalBytes: 8192, MaxNodes: 64})
	if err != nil {
		t.Fatal(err)
	}
	const pid = 41
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors, Files: files}

	// 路径形态:statfs("/")。
	writeString(syscalls.Memory.(*fakeMemory), 0x1000, "/")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysStatfs, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0x3000}}); reply.Errno != "" {
		t.Fatalf("statfs: %+v", reply)
	}
	memory := syscalls.Memory.(*fakeMemory)
	if blocks := binary.LittleEndian.Uint64([]byte{readByte(memory, 0x3000+8), readByte(memory, 0x3000+9), readByte(memory, 0x3000+10), readByte(memory, 0x3000+11), readByte(memory, 0x3000+12), readByte(memory, 0x3000+13), readByte(memory, 0x3000+14), readByte(memory, 0x3000+15)}); blocks != 2 {
		t.Fatalf("blocks=%d want 2", blocks)
	}

	// 句柄形态:fstatfs(fd) 与 statfs 一致。
	filefd, err := files.Open(ctx, OpenRequest{Path: "/f", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}})
	if err != nil {
		t.Fatal(err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFstatfs, Args: [6]uint64{filefd, 0x3000}}); reply.Errno != "" {
		t.Fatalf("fstatfs: %+v", reply)
	}
	if blocks := binary.LittleEndian.Uint64([]byte{readByte(memory, 0x3000+8), readByte(memory, 0x3000+9), readByte(memory, 0x3000+10), readByte(memory, 0x3000+11), readByte(memory, 0x3000+12), readByte(memory, 0x3000+13), readByte(memory, 0x3000+14), readByte(memory, 0x3000+15)}); blocks != 2 {
		t.Fatalf("fstatfs blocks=%d want 2", blocks)
	}
}

func readByte(memory *fakeMemory, address uint64) byte {
	return memory.data[address]
}

func TestDupAndFcntlFlagsAgainstRealFileSystem(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const pid = 51
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors, Files: files}

	origin, err := files.Open(ctx, OpenRequest{Path: "/f", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}})
	if err != nil {
		t.Fatal(err)
	}

	// dup:最低空闲编号且共享偏移。
	dupReply := syscalls.Handle(ctx, call(SysDup, origin, 0, 0))
	if dupReply.Errno != "" {
		t.Fatalf("dup: %+v", dupReply)
	}
	duped := dupReply.Value
	if duped <= origin {
		t.Fatalf("dup 编号=%d 应大于 %d", duped, origin)
	}
	if _, err := descriptors.Write(ctx, origin, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	// 共享偏移:原描述符写到位置 5,副本从同一位置读 → 立即 EOF。
	buffer := make([]byte, 5)
	if loaded, err := descriptors.Read(ctx, duped, buffer); !errors.Is(err, io.EOF) || loaded != 0 {
		t.Fatalf("dup 应共享偏移(读到 EOF): 读=%d err=%v", loaded, err)
	}

	// F_GETFL:读写模式。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFcntl, Args: [6]uint64{origin, fGetFL}}); reply.Errno != "" || reply.Value != 2 {
		t.Fatalf("F_GETFL: %+v want O_RDWR", reply)
	}

	// F_SETFL:O_NONBLOCK 对常规文件无效果地成功,O_APPEND 生效。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFcntl, Args: [6]uint64{origin, fSetFL, oNonBlock}}); reply.Errno != "" {
		t.Fatalf("F_SETFL NONBLOCK: %+v", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFcntl, Args: [6]uint64{origin, fSetFL, oAppend}}); reply.Errno != "" {
		t.Fatalf("F_SETFL APPEND: %+v", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFcntl, Args: [6]uint64{origin, fGetFL}}); reply.Errno != "" || reply.Value != 2|oAppend {
		t.Fatalf("F_GETFL 应含 O_APPEND: %+v", reply)
	}
	// 追加写落在末尾。
	if _, err := descriptors.Write(ctx, origin, []byte("!")); err != nil {
		t.Fatal(err)
	}
	if stat, err := system.Stat(ctx, "/f"); err != nil || stat.Size() != 6 {
		t.Fatalf("追加后尺寸=%d err=%v", stat.Size(), err)
	}

	// F_DUPFD_CLOEXEC:起始编号与 CLOEXEC 标志。
	cloexecReply := syscalls.Handle(ctx, Call{Syscall: SysFcntl, Args: [6]uint64{origin, fDupFDCloexec, 200}})
	if cloexecReply.Errno != "" || cloexecReply.Value < 200 {
		t.Fatalf("F_DUPFD_CLOEXEC: %+v", cloexecReply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysFcntl, Args: [6]uint64{cloexecReply.Value, fGetFD}}); reply.Errno != "" || reply.Value != 1 {
		t.Fatalf("CLOEXEC 标志: %+v", reply)
	}

	// 关闭副本不影响原描述符。
	if reply := syscalls.Handle(ctx, call(SysClose, duped, 0, 0)); reply.Errno != "" {
		t.Fatalf("close dup: %+v", reply)
	}
	if _, err := descriptors.Get(ctx, origin); err != nil {
		t.Fatalf("原描述符失效: %v", err)
	}
}

// TestDirectoryOperationsAgainstRealFileSystem 验证 I10:mkdirat/
// unlinkat/renameat 在真实 memfs 上的目录、文件、覆盖与跨挂载
// 失败语义(Linux errno 矩阵)。
func TestDirectoryOperationsAgainstRealFileSystem(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const pid = 41
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"}
	syscalls := Syscalls{Memory: newFakeMemory(), Files: files}

	writePath := func(address uint64, path string) {
		t.Helper()
		if err := syscalls.Memory.Write(ctx, address, append([]byte(path), 0)); err != nil {
			t.Fatal(err)
		}
	}
	// mkdirat 成功与 EEXIST(目录与普通文件同名冲突)。
	writePath(0x1000, "/dir")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysMkdirat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0o755}}); reply.Errno != "" {
		t.Fatalf("mkdirat: %+v", reply)
	}
	writePath(0x1010, "/dir")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysMkdirat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, 0o755}}); reply.Errno != errno.Exist {
		t.Fatalf("重复 mkdirat: %+v want EEXIST", reply)
	}
	writePath(0x1000, "/note")
	if _, err := files.Open(ctx, OpenRequest{Path: "/note", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644}); err != nil {
		t.Fatal(err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysMkdirat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, 0o755}}); reply.Errno != errno.Exist {
		t.Fatalf("mkdirat 覆盖普通文件: %+v want EEXIST", reply)
	}
	// 缺失父目录 ENOENT。
	writePath(0x1000, "/missing/child")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysMkdirat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0o755}}); reply.Errno != errno.NotFound {
		t.Fatalf("缺失父目录: %+v want ENOENT", reply)
	}

	// unlinkat:未知 flags EINVAL;非空目录 ENOTEMPTY;空目录成功。
	writePath(0x1020, "/dir")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1020, 1 << 20}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知 flags: %+v want EINVAL", reply)
	}
	writePath(0x1000, "/dir/child")
	if _, err := files.Open(ctx, OpenRequest{Path: "/dir/child", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644}); err != nil {
		t.Fatal(err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1020, atRemovedir}}); reply.Errno != errno.NotEmpty {
		t.Fatalf("非空目录: %+v want ENOTEMPTY", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0}}); reply.Errno != "" {
		t.Fatalf("unlink 文件: %+v", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1020, atRemovedir}}); reply.Errno != "" {
		t.Fatalf("目录内文件已删后删除目录: %+v want 成功", reply)
	}

	// renameat 覆盖矩阵:file→file 原子替换、file→dir EISDIR、
	// dir→file ENOTDIR、缺失源 ENOENT、目录替换空目录成功。
	writePath(0x1000, "/note")
	if _, err := files.Open(ctx, OpenRequest{Path: "/note", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644}); err != nil {
		t.Fatal(err)
	}
	writePath(0x1010, "/note2")
	if _, err := files.Open(ctx, OpenRequest{Path: "/note2", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644}); err != nil {
		t.Fatal(err)
	}
	writePath(0x1020, "/note3")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysRenameat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, atCurrentWorkingDirectory, 0x1020}}); reply.Errno != "" {
		t.Fatalf("file→file 覆盖: %+v", reply)
	}
	if _, err := files.Stat(ctx, "/note3"); err != nil {
		t.Fatalf("覆盖后目标缺失: %v", err)
	}
	if _, err := files.Stat(ctx, "/note"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("覆盖后源应消失: %v", err)
	}
	writePath(0x1000, "/note4")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysMkdirat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0o755}}); reply.Errno != "" {
		t.Fatalf("mkdirat /note4: %+v", reply)
	}
	writePath(0x1010, "/note5")
	if _, err := files.Open(ctx, OpenRequest{Path: "/note5", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644}); err != nil {
		t.Fatal(err)
	}
	writePath(0x1020, "/note4")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysRenameat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, atCurrentWorkingDirectory, 0x1020}}); reply.Errno != errno.IsDirectory {
		t.Fatalf("file→dir: %+v want EISDIR", reply)
	}
	writePath(0x1010, "/note5")
	writePath(0x1020, "/note4")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysRenameat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1020, atCurrentWorkingDirectory, 0x1010}}); reply.Errno != errno.NotDirectory {
		t.Fatalf("dir→file: %+v want ENOTDIR", reply)
	}
	writePath(0x1000, "/gone")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysRenameat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, atCurrentWorkingDirectory, 0x1010}}); reply.Errno != errno.NotFound {
		t.Fatalf("缺失源: %+v want ENOENT", reply)
	}
	// renameat2:flags=0 等价 renameat,RENAME_NOREPLACE 明确 EINVAL。
	writePath(0x1010, "/note5")
	if _, err := files.Open(ctx, OpenRequest{Path: "/note5", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644}); err != nil {
		t.Fatal(err)
	}
	writePath(0x1020, "/note2")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysRenameat2, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, atCurrentWorkingDirectory, 0x1020, 0}}); reply.Errno != "" {
		t.Fatalf("renameat2 flags=0: %+v", reply)
	}
	writePath(0x1010, "/note5")
	if _, err := files.Open(ctx, OpenRequest{Path: "/note5", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644}); err != nil {
		t.Fatal(err)
	}
	writePath(0x1020, "/note2")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysRenameat2, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, atCurrentWorkingDirectory, 0x1020, 1}}); reply.Errno != errno.Invalid {
		t.Fatalf("RENAME_NOREPLACE: %+v want EINVAL", reply)
	}
}

// TestReadWriteShortSemanticsAgainstRealFileSystem 验证 I06:write(2)
// 按配额短写返回已完成量,后续写报 ENOSPC;read(2) 在 EOF 返回 0;
// 零长度读写恒成功。
func TestReadWriteShortSemanticsAgainstRealFileSystem(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{TotalBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	const pid = 51
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors, Files: files}

	fd, err := files.Open(ctx, OpenRequest{Path: "/log", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644})
	if err != nil {
		t.Fatal(err)
	}
	// 写入 8 字节后写 4 字节:短写 2,write(2) 返回 2 且无错误。
	if err := syscalls.Memory.Write(ctx, 0x2000, []byte("abcdefgh")); err != nil {
		t.Fatal(err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWrite, Args: [6]uint64{fd, 0x2000, 8}}); reply.Value != 8 || reply.Errno != "" {
		t.Fatalf("首次写: %+v", reply)
	}
	if err := syscalls.Memory.Write(ctx, 0x2000, []byte("WXYZ")); err != nil {
		t.Fatal(err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWrite, Args: [6]uint64{fd, 0x2000, 4}}); reply.Value != 2 || reply.Errno != "" {
		t.Fatalf("短写应返回已完成量 2: %+v", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWrite, Args: [6]uint64{fd, 0x2000, 1}}); reply.Errno != errno.NoSpace {
		t.Fatalf("满盘写: %+v want ENOSPC", reply)
	}
	// 零长度写在满盘时成功。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWrite, Args: [6]uint64{fd, 0x2000, 0}}); reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("零长度写: %+v", reply)
	}
	// EOF 读返回 0。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysRead, Args: [6]uint64{fd, 0x2000, 4}}); reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("EOF 读: %+v want 0", reply)
	}
}

// TestLinkSymlinkLifecycleAgainstRealFileSystem 验证 I11:linkat 默认
// 不解引用符号链接(链接指向链接本身,nlink 可观察),AT_SYMLINK_FOLLOW
// 对目标文件建链;readlinkat 截断返回放入缓冲区的字节数;删除目标后
// 符号链接悬空但 readlinkat 仍可读,硬链接内容经其余链接存续。
func TestLinkSymlinkLifecycleAgainstRealFileSystem(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const pid = 61
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors, Files: files}

	writePath := func(address uint64, path string) {
		t.Helper()
		if err := syscalls.Memory.Write(ctx, address, append([]byte(path), 0)); err != nil {
			t.Fatal(err)
		}
	}
	note, err := files.Open(ctx, OpenRequest{Path: "/note", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Write(ctx, note, []byte("payload")); err != nil {
		t.Fatal(err)
	}

	// symlinkat + readlinkat:完整读取与截断(计数为放入缓冲区的字节数)。
	writePath(0x1000, "note")
	writePath(0x1010, "/link")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysSymlinkat, Args: [6]uint64{0x1000, atCurrentWorkingDirectory, 0x1010}}); reply.Errno != "" {
		t.Fatalf("symlinkat: %+v", reply)
	}
	writePath(0x1010, "/link")
	if err := syscalls.Memory.Write(ctx, 0x2000, make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysReadlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, 0x2000, 16}}); reply.Value != 4 {
		t.Fatalf("readlinkat 完整: %+v want 4", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysReadlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, 0x2000, 2}}); reply.Value != 2 {
		t.Fatalf("readlinkat 截断: %+v want 2", reply)
	}
	var text [2]byte
	if err := syscalls.Memory.Read(ctx, 0x2000, text[:]); err != nil || string(text[:]) != "no" {
		t.Fatalf("截断内容=%q err=%v", text, err)
	}

	// linkat 默认:链接指向符号链接本身——/hard 也是指向 note 的链接,
	// 两者 lstat 的 nlink 均为 2;write 内容经 /note 可见。
	writePath(0x1010, "/link")
	writePath(0x1020, "/hard")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysLinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, atCurrentWorkingDirectory, 0x1020, 0}}); reply.Errno != "" {
		t.Fatalf("linkat 默认: %+v", reply)
	}
	assertNlink := func(path string, want uint64) {
		t.Helper()
		writePath(0x1030, path)
		if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1030, 0x2000, atSymlinkNoFollow, 0}}); reply.Errno != "" {
			t.Fatalf("fstatat %s: %+v", path, reply)
		}
		var raw [144]byte
		if err := syscalls.Memory.Read(ctx, 0x2000, raw[:]); err != nil {
			t.Fatal(err)
		}
		// asm-generic struct stat:nlink 是偏移 20 的 u32。
		if nlink := uint64(binary.LittleEndian.Uint32(raw[20:])); nlink != want {
			t.Fatalf("%s nlink=%d want %d", path, nlink, want)
		}
	}
	assertNlink("/link", 2)
	assertNlink("/hard", 2)

	// linkat AT_SYMLINK_FOLLOW:对解引用后的目标文件建链;
	// 目标路径已存在时报 EEXIST,新路径成功且 note nlink=2。
	writePath(0x1020, "/hard")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysLinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, atCurrentWorkingDirectory, 0x1020, atSymlinkFollow}}); reply.Errno != errno.Exist {
		t.Fatalf("linkat FOLLOW 覆盖既有路径: %+v want EEXIST", reply)
	}
	writePath(0x1020, "/hard2")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysLinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, atCurrentWorkingDirectory, 0x1020, atSymlinkFollow}}); reply.Errno != "" {
		t.Fatalf("linkat FOLLOW: %+v", reply)
	}
	assertNlink("/note", 2)
	if _, err := descriptors.Seek(ctx, note, 0, SeekSet); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	if loaded, err := descriptors.Read(ctx, note, buffer); err != nil || string(buffer[:loaded]) != "payload" {
		t.Fatalf("经 /hard2 建链后 /note 内容=%q err=%v", buffer[:loaded], err)
	}

	// linkat 未知 flags EINVAL。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysLinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, atCurrentWorkingDirectory, 0x1020, 1 << 20}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知 flags: %+v want EINVAL", reply)
	}

	// 删除生命周期:删除 /note(硬链接之一)后内容经 /hard3 存续;
	// 符号链接悬空但 readlinkat 仍返回目标;打开悬空链接 ENOENT。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0}}); reply.Errno != "" {
		t.Fatalf("unlink /note: %+v", reply)
	}
	if _, err := files.Open(ctx, OpenRequest{Path: "/note", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NotFound) {
		t.Fatalf("已删目标: %v want ENOENT", err)
	}
	dangling, err := files.Open(ctx, OpenRequest{Path: "/hard2", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := descriptors.Read(ctx, dangling, buffer)
	if err != nil || string(buffer[:loaded]) != "payload" {
		t.Fatalf("硬链接内容存续: %q err=%v", buffer[:loaded], err)
	}
	writePath(0x1010, "/link")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysReadlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1010, 0x2000, 16}}); reply.Value != 4 {
		t.Fatalf("悬空链接 readlinkat: %+v want 4", reply)
	}
	if _, err := files.Open(ctx, OpenRequest{Path: "/link", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NotFound) {
		t.Fatalf("悬空链接打开: %v want ENOENT", err)
	}
}

// TestDupAndSeparateOpensOffsetMatrix 验证 I04:dup 共享打开文件
// 描述(任一描述符的读写推进共享偏移),分别 open 拥有独立偏移。
func TestDupAndSeparateOpensOffsetMatrix(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const pid = 71
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors, Files: files}

	note, err := files.Open(ctx, OpenRequest{Path: "/f", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o644})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Write(ctx, note, []byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Seek(ctx, note, 0, SeekSet); err != nil {
		t.Fatal(err)
	}
	// dup:新描述符共享同一打开文件描述。
	duped, err := descriptors.DupFrom(ctx, note)
	if err != nil {
		t.Fatal(err)
	}
	readThree := func(descriptor uint64) string {
		t.Helper()
		buffer := make([]byte, 3)
		n, err := descriptors.Read(ctx, descriptor, buffer)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("读 fd=%d: %v", descriptor, err)
		}
		return string(buffer[:n])
	}
	if got := readThree(note); got != "abc" {
		t.Fatalf("原描述符读=%q", got)
	}
	// dup 出的描述符续接共享偏移(应为 def 而非 abc)。
	if got := readThree(duped); got != "def" {
		t.Fatalf("dup 读=%q want def(共享偏移)", got)
	}
	// 回到原描述符:偏移已在文件尾(read 返回 0,即 EOF)。
	if got := readThree(note); got != "" {
		t.Fatalf("共享偏移读=%q want 空(EOF)", got)
	}
	// 分别 open:独立偏移,从头读取。
	independent, err := files.Open(ctx, OpenRequest{Path: "/f", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readThree(independent); got != "abc" {
		t.Fatalf("独立打开读=%q want abc", got)
	}
	// 独立描述符的推进不影响 dup 链:dup 链仍在 EOF。
	if got := readThree(duped); got != "" {
		t.Fatalf("独立偏移不应影响共享链: %q", got)
	}
	_ = syscalls
}
