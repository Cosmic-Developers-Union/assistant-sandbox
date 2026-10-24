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

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type fakeMemory struct {
	data     map[uint64]byte
	readErr  error
	writeErr error
	reads    int
	writes   int
}

func newFakeMemory() *fakeMemory { return &fakeMemory{data: map[uint64]byte{}} }

func (m *fakeMemory) Read(_ context.Context, address uint64, buffer []byte) error {
	m.reads++
	if m.readErr != nil {
		return m.readErr
	}
	for index := range buffer {
		buffer[index] = m.data[address+uint64(index)]
	}
	return nil
}

func (m *fakeMemory) Write(_ context.Context, address uint64, buffer []byte) error {
	m.writes++
	if m.writeErr != nil {
		return m.writeErr
	}
	for index, value := range buffer {
		m.data[address+uint64(index)] = value
	}
	return nil
}

type fakeDescriptors struct {
	written    []byte
	input      []byte
	writeErr   error
	readErr    error
	closeErr   error
	closed     bool
	offset     int64
	seeks      int
	seekErr    error
	eventfd    [][2]uint64
	eventfdErr error
	get        any
}

func (d *fakeDescriptors) Write(_ context.Context, _ uint64, buffer []byte) (int, error) {
	if d.writeErr != nil {
		return 0, d.writeErr
	}
	d.written = append(d.written, buffer...)
	return len(buffer), nil
}

func (d *fakeDescriptors) Dup2(_ context.Context, oldfd, newfd uint64, cloexec bool) error {
	return d.seekErr
}

func (d *fakeDescriptors) EventFD(ctx context.Context, initialValue, flags uint64) (uint64, error) {
	d.eventfd = append(d.eventfd, [2]uint64{initialValue, flags})
	return 33, d.eventfdErr
}

func (d *fakeDescriptors) Pipe(_ context.Context) (uint64, uint64, error) {
	return 3, 4, d.seekErr
}

func (d *fakeDescriptors) Fcntl(_ context.Context, descriptor uint64, cmd uint64, arg uint64) (uint64, error) {
	return 0, d.seekErr
}

func (d *fakeDescriptors) Get(_ context.Context, descriptor uint64) (any, error) {
	if d.seekErr != nil {
		return nil, d.seekErr
	}
	return d.get, nil
}

func (d *fakeDescriptors) Seek(_ context.Context, _ uint64, offset int64, whence SeekWhence) (int64, error) {
	if d.seekErr != nil {
		return 0, d.seekErr
	}
	d.seeks++
	switch whence {
	case SeekSet:
		d.offset = offset
	case SeekCurrent:
		d.offset += offset
	}
	return d.offset, nil
}

func (d *fakeDescriptors) Close(_ context.Context, _ uint64) error {
	if d.closeErr != nil {
		return d.closeErr
	}
	d.closed = true
	return nil
}

func (d *fakeDescriptors) Read(_ context.Context, _ uint64, buffer []byte) (int, error) {
	if d.readErr != nil {
		return 0, d.readErr
	}
	loaded := copy(buffer, d.input)
	d.input = d.input[loaded:]
	return loaded, nil
}

func call(syscall Syscall, descriptor, address, count uint64) Call {
	return Call{Syscall: syscall, Args: [6]uint64{descriptor, address, count}}
}

func TestWriteCopiesFromGuestMemory(t *testing.T) {
	space := newFakeMemory()
	for index, value := range []byte("hello") {
		space.data[0x1000+uint64(index)] = value
	}
	descriptors := &fakeDescriptors{}
	syscalls := Syscalls{Memory: space, Descriptors: descriptors}
	reply := syscalls.Handle(context.Background(), call(SysWrite, 1, 0x1000, 5))
	if reply.Errno != "" || reply.Exit || reply.Value != 5 {
		t.Fatalf("reply=%+v", reply)
	}
	if string(descriptors.written) != "hello" {
		t.Fatalf("写入=%q", descriptors.written)
	}
}

func TestReadWritesBackToGuestMemory(t *testing.T) {
	space := newFakeMemory()
	descriptors := &fakeDescriptors{input: []byte("abc")}
	syscalls := Syscalls{Memory: space, Descriptors: descriptors}
	reply := syscalls.Handle(context.Background(), call(SysRead, 0, 0x2000, 8))
	if reply.Errno != "" || reply.Value != 3 {
		t.Fatalf("reply=%+v", reply)
	}
	for index, want := range []byte("abc") {
		if got := space.data[0x2000+uint64(index)]; got != want {
			t.Fatalf("客体地址 %#x=%q want %q", 0x2000+uint64(index), got, want)
		}
	}
}

func TestReadEOFIsNotAnError(t *testing.T) {
	space := newFakeMemory()
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}}
	reply := syscalls.Handle(context.Background(), call(SysRead, 0, 0x2000, 4))
	if reply.Errno != "" || reply.Exit || reply.Value != 0 {
		t.Fatalf("reply=%+v", reply)
	}

	eof := Syscalls{Memory: space, Descriptors: &fakeDescriptors{readErr: io.EOF}}
	reply = eof.Handle(context.Background(), call(SysRead, 0, 0x2000, 4))
	if reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("EOF reply=%+v", reply)
	}
}

func TestZeroLengthAccessIsNoop(t *testing.T) {
	space := newFakeMemory()
	descriptors := &fakeDescriptors{}
	syscalls := Syscalls{Memory: space, Descriptors: descriptors}
	for _, syscall := range []Syscall{SysRead, SysWrite} {
		reply := syscalls.Handle(context.Background(), call(syscall, 7, 0xDEAD, 0))
		if reply.Errno != "" || reply.Value != 0 {
			t.Fatalf("%v reply=%+v", syscall, reply)
		}
	}
	if space.reads != 0 || space.writes != 0 {
		t.Fatalf("零长度访问碰到了内存: 读 %d 写 %d", space.reads, space.writes)
	}
}

func TestBufferLimitRejectsOversizedCount(t *testing.T) {
	space := newFakeMemory()
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, BufferLimit: 8}
	reply := syscalls.Handle(context.Background(), call(SysWrite, 1, 0x1000, 9))
	if reply.Errno != errno.Invalid {
		t.Fatalf("reply=%+v want EINVAL", reply)
	}

	reply = syscalls.Handle(context.Background(), call(SysWrite, 1, 0x1000, 8))
	if reply.Errno != "" || reply.Value != 8 {
		t.Fatalf("reply=%+v", reply)
	}

	if DefaultBufferLimit == 0 || DefaultBufferLimit > 1<<24 {
		t.Fatalf("默认上限=%d", DefaultBufferLimit)
	}
}

func TestMissingPortsReportNotImplemented(t *testing.T) {
	for _, syscalls := range []Syscalls{
		{Descriptors: &fakeDescriptors{}},
		{Memory: newFakeMemory()},
		{},
	} {
		for _, syscall := range []Syscall{SysRead, SysWrite} {
			reply := syscalls.Handle(context.Background(), call(syscall, 1, 0x1000, 4))
			if reply.Errno != errno.NotImplemented || reply.Value != 0 {
				t.Fatalf("%v reply=%+v", syscall, reply)
			}
		}
	}
}

func TestGuestAddressFaults(t *testing.T) {
	ctx := context.Background()
	broken := newFakeMemory()
	broken.readErr = errno.Range
	syscalls := Syscalls{Memory: broken, Descriptors: &fakeDescriptors{}}
	if reply := syscalls.Handle(ctx, call(SysWrite, 1, 0x1000, 4)); reply.Errno != errno.Fault {
		t.Fatalf("写源缓冲非法: reply=%+v want EFAULT", reply)
	}
	broken.writeErr = errno.Range
	syscalls = Syscalls{Memory: broken, Descriptors: &fakeDescriptors{input: []byte("abc")}}
	if reply := syscalls.Handle(ctx, call(SysRead, 0, 0x1000, 4)); reply.Errno != errno.Fault {
		t.Fatalf("读目标缓冲非法: reply=%+v want EFAULT", reply)
	}
}

func TestDescriptorErrorsKeepIdentity(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label string
		err   error
		want  errno.Code
	}{
		{"权限", errno.Permission, errno.Permission},
		{"断开的管道", errno.Wrap(errno.BrokenPipe, "管道对端已关闭"), errno.BrokenPipe},
		{"没有身份的错误", errors.New("磁盘冒烟"), errno.IO},
	} {
		syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{writeErr: tc.err}}
		if reply := syscalls.Handle(ctx, call(SysWrite, 1, 0x1000, 4)); reply.Errno != tc.want {
			t.Fatalf("%s: reply=%+v want %v", tc.label, reply, tc.want)
		}
		syscalls = Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{readErr: tc.err}}
		if reply := syscalls.Handle(ctx, call(SysRead, 0, 0x1000, 4)); reply.Errno != tc.want {
			t.Fatalf("%s（读）: reply=%+v want %v", tc.label, reply, tc.want)
		}
	}
}

func TestUnknownSyscallsReportENOSYS(t *testing.T) {
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{}}
	for _, syscall := range []Syscall{SysUnknown, Syscall(1234)} {
		if reply := syscalls.Handle(context.Background(), Call{Syscall: syscall}); reply.Errno != errno.NotImplemented {
			t.Fatalf("%v: reply=%+v", syscall, reply)
		}
	}
}

func TestExitThroughSyscalls(t *testing.T) {
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{}}
	for _, syscall := range []Syscall{SysExit, SysExitGroup} {
		reply := syscalls.Handle(context.Background(), Call{Syscall: syscall, Args: [6]uint64{9}})
		if !reply.Exit || reply.Value != 9 {
			t.Fatalf("%v: reply=%+v", syscall, reply)
		}
	}
}

func TestErrnoOfFindsIdentity(t *testing.T) {
	if got := errnoOf(errno.Wrap(errno.Permission, "打开客体文件")); got != errno.Permission {
		t.Fatalf("got %v", got)
	}
	if got := errnoOf(errors.New("没有身份")); got != errno.IO {
		t.Fatalf("got %v want EIO", got)
	}
}

type bufferStream struct {
	input  []byte
	output []byte
}

func (s *bufferStream) Read(buffer []byte) (int, error) {
	if len(s.input) == 0 {
		return 0, io.EOF
	}
	loaded := copy(buffer, s.input)
	s.input = s.input[loaded:]
	return loaded, nil
}

func (s *bufferStream) Write(buffer []byte) (int, error) {
	s.output = append(s.output, buffer...)
	return len(buffer), nil
}

func (s *bufferStream) Close() error { return nil }

func TestProcessDescriptorsAgainstRealTable(t *testing.T) {
	ctx := context.Background()
	const pid = 42
	table := fd.New()

	terminal := &bufferStream{input: []byte("in")}
	table.Attach(pid, fd.Streams{Input: terminal, Output: terminal, Error: terminal})
	adapter := ProcessDescriptors{Table: table, PID: pid}
	buffer := make([]byte, 2)
	if loaded, err := adapter.Read(ctx, 0, buffer); err != nil || loaded != 2 {
		t.Fatalf("读=%d err=%v", loaded, err)
	}
	if string(buffer) != "in" {
		t.Fatalf("读入=%q", buffer)
	}
	if written, err := adapter.Write(ctx, 1, []byte("out")); err != nil || written != 3 {
		t.Fatalf("写=%d err=%v", written, err)
	}
	if string(terminal.output) != "out" {
		t.Fatalf("写出=%q", terminal.output)
	}

	other := ProcessDescriptors{Table: table, PID: pid + 1}
	if _, err := other.Read(ctx, 0, buffer); err == nil {
		t.Fatal("另一个 pid 不应拿到别人的描述符")
	}

	if _, err := (ProcessDescriptors{PID: pid}).Write(ctx, 1, nil); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("空表: %v", err)
	}
	if _, err := adapter.Write(ctx, math.MaxUint32+1, nil); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("越界编号: %v", err)
	}
}

func TestSyscallsOverProcessDescriptors(t *testing.T) {
	ctx := context.Background()
	const pid = 7
	table := fd.New()
	stream := &bufferStream{}
	table.Attach(pid, fd.Streams{Output: stream})
	space := newFakeMemory()
	for index, value := range []byte("hello") {
		space.data[0x4000+uint64(index)] = value
	}
	syscalls := Syscalls{Memory: space, Descriptors: ProcessDescriptors{Table: table, PID: pid}}
	reply := syscalls.Handle(ctx, call(SysWrite, 1, 0x4000, 5))
	if reply.Errno != "" || reply.Value != 5 {
		t.Fatalf("reply=%+v", reply)
	}
	if !bytes.Equal(stream.output, []byte("hello")) {
		t.Fatalf("写出=%q", stream.output)
	}
}

type fakeFiles struct {
	requests     []OpenRequest
	next         uint64
	err          error
	entries      []stdfs.DirEntry
	removeCalls  []string
	statInfo     stdfs.FileInfo
	statPaths    []string
	lstatInfo    stdfs.FileInfo
	lstatErr     error
	setTimeErr   error
	setTimeCalls []setTimeCall
	statfsInfo   StatfsInfo
	statfsErr    error
}

type setTimeCall struct {
	path         string
	atime, mtime int64
	follow       bool
}

type fakeFileInfo struct{ dir bool }

func (i fakeFileInfo) Name() string { return "stub" }
func (i fakeFileInfo) Size() int64  { return 0 }
func (i fakeFileInfo) Mode() stdfs.FileMode {
	if i.dir {
		return stdfs.ModeDir
	}
	return 0
}
func (i fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (i fakeFileInfo) IsDir() bool        { return i.dir }
func (i fakeFileInfo) Sys() any           { return nil }

func (f *fakeFiles) Mkdir(_ context.Context, path string, mode uint32) error { return f.err }
func (f *fakeFiles) Remove(_ context.Context, path string) error {
	f.removeCalls = append(f.removeCalls, path)
	return f.err
}
func (f *fakeFiles) Symlink(_ context.Context, target, linkpath string) error { return f.err }
func (f *fakeFiles) Link(_ context.Context, oldpath, newpath string) error    { return f.err }
func (f *fakeFiles) Rename(_ context.Context, oldpath, newpath string) error  { return f.err }
func (f *fakeFiles) ReadDir(_ context.Context, path string) ([]stdfs.DirEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

func (f *fakeFiles) Readlink(_ context.Context, path string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "", nil
}

func (f *fakeFiles) Stat(_ context.Context, path string) (stdfs.FileInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.statPaths = append(f.statPaths, path)
	return f.statInfo, nil
}

func (f *fakeFiles) Statfs(_ context.Context, path string) (StatfsInfo, error) {
	if f.statfsErr != nil {
		return StatfsInfo{}, f.statfsErr
	}
	return f.statfsInfo, nil
}

func (f *fakeFiles) SetTimes(_ context.Context, path string, atime, mtime int64, follow bool) error {
	if f.setTimeErr != nil {
		return f.setTimeErr
	}
	f.setTimeCalls = append(f.setTimeCalls, setTimeCall{path: path, atime: atime, mtime: mtime, follow: follow})
	return nil
}

func (f *fakeFiles) Lstat(_ context.Context, path string) (stdfs.FileInfo, error) {
	if f.lstatErr != nil {
		return nil, f.lstatErr
	}
	return f.lstatInfo, nil
}

func (f *fakeFiles) Open(_ context.Context, request OpenRequest) (uint64, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return 0, f.err
	}
	if f.next == 0 {
		f.next = 3
	}
	return f.next, nil
}

func writeString(memory *fakeMemory, address uint64, value string) {
	for index, b := range []byte(value) {
		memory.data[address+uint64(index)] = b
	}
}

func TestOpenAtInstallsDescriptor(t *testing.T) {
	space := newFakeMemory()
	writeString(space, 0x1000, "/etc/motd")
	files := &fakeFiles{next: 5}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: files}
	flags := OpenFlags{Mode: OpenRead}
	reply := syscalls.Handle(context.Background(), Call{
		Syscall:   SysOpenAt,
		Args:      [6]uint64{atCurrentWorkingDirectory, 0x1000, 0, 0o644},
		OpenFlags: &flags,
	})
	if reply.Errno != "" || reply.Value != 5 {
		t.Fatalf("reply=%+v", reply)
	}
	if len(files.requests) != 1 {
		t.Fatalf("请求=%+v", files.requests)
	}
	request := files.requests[0]
	if request.Path != "/etc/motd" || request.Perm != 0o644 || request.Flags.Mode != OpenRead {
		t.Fatalf("请求=%+v", request)
	}
}

func TestOpenAtRejections(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	writeString(space, 0x1000, "/etc/motd")
	base := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: &fakeFiles{}}
	flags := OpenFlags{Mode: OpenRead}
	for _, tc := range []struct {
		label string
		call  Call
		want  errno.Code
	}{
		{
			label: "目录描述符相对解析未实现",
			call:  Call{Syscall: SysOpenAt, Args: [6]uint64{3, 0x1000, 0, 0}, OpenFlags: &flags},
			want:  errno.NotSupported,
		},
		{
			label: "架构层不认识标志位",
			call:  Call{Syscall: SysOpenAt, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0, 0}},
			want:  errno.NotSupported,
		},
		{
			label: "O_EXCL 没有 O_CREAT",
			call: Call{Syscall: SysOpenAt, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0, 0},
				OpenFlags: &OpenFlags{Mode: OpenRead, Exclusive: true}},
			want: errno.Invalid,
		},
		{
			label: "文件端口未装配",
			call: Call{Syscall: SysOpenAt, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0, 0},
				OpenFlags: &flags},
			want: errno.NotImplemented,
		},
	} {
		syscalls := base
		if tc.label == "文件端口未装配" {
			syscalls.Files = nil
		}
		if reply := syscalls.Handle(ctx, tc.call); reply.Errno != tc.want {
			t.Fatalf("%s: reply=%+v want %v", tc.label, reply, tc.want)
		}
	}
}

func TestOpenAtPathErrors(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: &fakeFiles{}}
	flags := OpenFlags{Mode: OpenRead}
	open := func(address uint64) Reply {
		return syscalls.Handle(ctx, Call{
			Syscall:   SysOpenAt,
			Args:      [6]uint64{atCurrentWorkingDirectory, address, 0, 0},
			OpenFlags: &flags,
		})
	}

	if reply := open(0x1000); reply.Errno != errno.NotFound {
		t.Fatalf("空路径: reply=%+v want ENOENT", reply)
	}

	for offset := uint64(0); offset < PathLimit; offset++ {
		space.data[0x2000+offset] = 'a'
	}
	if reply := open(0x2000); reply.Errno != errno.NameTooLong {
		t.Fatalf("超长路径: reply=%+v want ENAMETOOLONG", reply)
	}

	broken := newFakeMemory()
	broken.readErr = errno.Range
	syscalls = Syscalls{Memory: broken, Descriptors: &fakeDescriptors{}, Files: &fakeFiles{}}
	if reply := open(0x1000); reply.Errno != errno.Fault {
		t.Fatalf("地址故障: reply=%+v want EFAULT", reply)
	}
}

func TestCloseDescriptor(t *testing.T) {
	ctx := context.Background()
	descriptors := &fakeDescriptors{}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysClose, Args: [6]uint64{3}}); reply.Errno != "" {
		t.Fatalf("reply=%+v", reply)
	}
	if !descriptors.closed {
		t.Fatal("关闭没有走到描述符端口")
	}
	failing := &fakeDescriptors{closeErr: errno.BadHandle}
	syscalls = Syscalls{Memory: newFakeMemory(), Descriptors: failing}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysClose, Args: [6]uint64{99}}); reply.Errno != errno.BadHandle {
		t.Fatalf("reply=%+v want EBADF", reply)
	}

	syscalls = Syscalls{Memory: newFakeMemory()}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysClose, Args: [6]uint64{3}}); reply.Errno != errno.NotImplemented {
		t.Fatalf("reply=%+v want ENOSYS", reply)
	}
}

func TestOpenFlagsValidate(t *testing.T) {
	for _, tc := range []struct {
		label string
		flags OpenFlags
		valid bool
	}{
		{"只读", OpenFlags{Mode: OpenRead}, true},
		{"读写加创建", OpenFlags{Mode: OpenReadWrite, Create: true}, true},
		{"追加写", OpenFlags{Mode: OpenWrite, Append: true}, true},
		{"未知模式", OpenFlags{Mode: OpenMode(3)}, false},
		{"O_EXCL 没有 O_CREAT", OpenFlags{Mode: OpenRead, Exclusive: true}, false},
		{"只读加追加", OpenFlags{Mode: OpenRead, Append: true}, false},
	} {
		err := tc.flags.Validate()
		if tc.valid && err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if !tc.valid && !errors.Is(err, errno.Invalid) {
			t.Fatalf("%s: %v want EINVAL", tc.label, err)
		}
	}
}

type fakeClock struct {
	wall      int64
	monotonic uint64
	err       error
}

func (c *fakeClock) Wall(context.Context) (int64, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.wall, nil
}

func (c *fakeClock) Monotonic(context.Context) (uint64, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.monotonic, nil
}

func TestIdentitySyscalls(t *testing.T) {
	ctx := context.Background()
	identity := &Identity{PID: 42, PPID: 1, UID: 1000, GID: 1001}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{}, Identity: identity}
	for syscall, want := range map[Syscall]uint64{
		SysGetPID: 42, SysGetTID: 42, SysGetPPID: 1,
		SysGetUID: 1000, SysGetEUID: 1000, SysGetGID: 1001, SysGetEGID: 1001,
	} {
		reply := syscalls.Handle(ctx, Call{Syscall: syscall})
		if reply.Errno != "" || reply.Value != want {
			t.Fatalf("%v: reply=%+v want %d", syscall, reply, want)
		}
	}

	bare := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{}}
	if reply := bare.Handle(ctx, Call{Syscall: SysGetPID}); reply.Errno != errno.NotImplemented {
		t.Fatalf("reply=%+v want ENOSYS", reply)
	}
}

func TestClockGettime(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	source := &fakeClock{wall: 1_500_000_000, monotonic: 2_250_000_000}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Clock: source}
	read := func(reply Reply) (int64, int64) {
		t.Helper()
		if reply.Errno != "" {
			t.Fatalf("reply=%+v", reply)
		}
		var encoded [16]byte
		for index := range encoded {
			encoded[index] = space.data[0x3000+uint64(index)]
		}
		return int64(binary.LittleEndian.Uint64(encoded[0:])),
			int64(binary.LittleEndian.Uint64(encoded[8:]))
	}

	seconds, nanos := read(syscalls.Handle(ctx, Call{Syscall: SysClockGettime, Args: [6]uint64{clockRealtime, 0x3000}}))
	if seconds != 1 || nanos != 500_000_000 {
		t.Fatalf("CLOCK_REALTIME=%d.%09d", seconds, nanos)
	}
	seconds, nanos = read(syscalls.Handle(ctx, Call{Syscall: SysClockGettime, Args: [6]uint64{clockMonotonic, 0x3000}}))
	if seconds != 2 || nanos != 250_000_000 {
		t.Fatalf("CLOCK_MONOTONIC=%d.%09d", seconds, nanos)
	}

	source.wall = -1
	seconds, nanos = read(syscalls.Handle(ctx, Call{Syscall: SysClockGettime, Args: [6]uint64{clockRealtime, 0x3000}}))
	if seconds != -1 || nanos != 999_999_999 {
		t.Fatalf("负时间=%d.%09d", seconds, nanos)
	}

	if reply := syscalls.Handle(ctx, Call{Syscall: SysClockGettime, Args: [6]uint64{2, 0x3000}}); reply.Errno != errno.NotSupported {
		t.Fatalf("未知时钟: reply=%+v want ENOTSUP", reply)
	}

	missing := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}}
	if reply := missing.Handle(ctx, Call{Syscall: SysClockGettime, Args: [6]uint64{clockMonotonic, 0x3000}}); reply.Errno != errno.NotImplemented {
		t.Fatalf("缺时钟: reply=%+v want ENOSYS", reply)
	}
	broken := newFakeMemory()
	broken.writeErr = errno.Range
	failing := Syscalls{Memory: broken, Descriptors: &fakeDescriptors{}, Clock: source}
	if reply := failing.Handle(ctx, Call{Syscall: SysClockGettime, Args: [6]uint64{clockMonotonic, 0x3000}}); reply.Errno != errno.Fault {
		t.Fatalf("目标地址非法: reply=%+v want EFAULT", reply)
	}

	source.err = errno.NotSupported
	if reply := syscalls.Handle(ctx, Call{Syscall: SysClockGettime, Args: [6]uint64{clockMonotonic, 0x3000}}); reply.Errno != errno.NotSupported {
		t.Fatalf("时钟错误: reply=%+v", reply)
	}
}

func TestLseekSemantics(t *testing.T) {
	ctx := context.Background()
	descriptors := &fakeDescriptors{offset: 10}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: descriptors}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysLseek, Args: [6]uint64{3, 4, uint64(SeekSet)}}); reply.Value != 4 {
		t.Fatalf("SEEK_SET: reply=%+v", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysLseek, Args: [6]uint64{3, 6, uint64(SeekCurrent)}}); reply.Value != 10 {
		t.Fatalf("SEEK_CUR: reply=%+v", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysLseek, Args: [6]uint64{3, 0, 7}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知基准: reply=%+v want EINVAL", reply)
	}
	blocked := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{seekErr: errno.Seek}}
	if reply := blocked.Handle(ctx, Call{Syscall: SysLseek, Args: [6]uint64{3, 0, uint64(SeekSet)}}); reply.Errno != errno.Seek {
		t.Fatalf("管道: reply=%+v want ESPIPE", reply)
	}
	bare := Syscalls{Memory: newFakeMemory()}
	if reply := bare.Handle(ctx, Call{Syscall: SysLseek, Args: [6]uint64{3, 0, uint64(SeekSet)}}); reply.Errno != errno.NotImplemented {
		t.Fatalf("缺端口: reply=%+v want ENOSYS", reply)
	}
}

func TestOpenAtKeepsFileErrorIdentity(t *testing.T) {
	space := newFakeMemory()
	writeString(space, 0x1000, "/secret")
	files := &fakeFiles{err: errno.Wrap(errno.Permission, "打开客体文件")}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: files}
	flags := OpenFlags{Mode: OpenRead}
	reply := syscalls.Handle(context.Background(), Call{
		Syscall:   SysOpenAt,
		Args:      [6]uint64{atCurrentWorkingDirectory, 0x1000, 0, 0},
		OpenFlags: &flags,
	})
	if reply.Errno != errno.Permission {
		t.Fatalf("reply=%+v want EACCES", reply)
	}
}

func TestIdentityRejectsNonIdentityCall(t *testing.T) {
	syscalls := Syscalls{Identity: &Identity{PID: 1}}
	if reply := syscalls.identity(Call{Syscall: SysRead}); reply.Errno != errno.NotImplemented {
		t.Fatalf("reply=%+v want ENOSYS", reply)
	}
}

func TestClockGettimePropagatesRealtimeErrors(t *testing.T) {
	source := &fakeClock{err: errno.Range}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{}, Clock: source}
	reply := syscalls.Handle(context.Background(), Call{Syscall: SysClockGettime, Args: [6]uint64{clockRealtime, 0x3000}})
	if reply.Errno != errno.Range {
		t.Fatalf("墙钟失败: reply=%+v want ERANGE", reply)
	}
}

func TestRawOpenFlagsTranslation(t *testing.T) {
	for _, tc := range []struct {
		label string
		flags OpenFlags
		want  uint32
	}{
		{"只读", OpenFlags{Mode: OpenRead}, 0},
		{"只写", OpenFlags{Mode: OpenWrite}, 1},
		{"读写加创建加截断", OpenFlags{Mode: OpenReadWrite, Create: true, Truncate: true}, 2 | 64 | 512},
		{"追加加独占加创建", OpenFlags{Mode: OpenWrite, Create: true, Exclusive: true, Append: true}, 1 | 64 | 128 | 1024},
	} {
		if got := rawOpenFlags(tc.flags); got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.label, got, tc.want)
		}
	}
}

type fakeHeap struct {
	current uint64
	limit   uint64
	err     error
	grown   []uint64
}

func (h *fakeHeap) Break(context.Context) (uint64, error) { return h.current, h.err }

func (h *fakeHeap) Grow(_ context.Context, target uint64) (uint64, error) {
	if h.err != nil {
		return 0, h.err
	}
	if target == 0 {
		return h.current, nil
	}
	h.grown = append(h.grown, target)
	if target > h.limit {
		return h.current, nil
	}
	h.current = target
	return h.current, nil
}

func TestBrkQueryGrowAndFailure(t *testing.T) {
	heap := &fakeHeap{current: 0x2000, limit: 0x3000}
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{}, Heap: heap}
	if reply := syscalls.Handle(context.Background(), Call{Syscall: SysBrk}); reply.Value != 0x2000 || reply.Errno != "" {
		t.Fatalf("查询: reply=%+v want 0x2000", reply)
	}
	if reply := syscalls.Handle(context.Background(), Call{Syscall: SysBrk, Args: [6]uint64{0x3000}}); reply.Value != 0x3000 || reply.Errno != "" {
		t.Fatalf("增长: reply=%+v want 0x3000", reply)
	}

	reply := syscalls.Handle(context.Background(), Call{Syscall: SysBrk, Args: [6]uint64{0x9000}})
	if reply.Errno != "" || reply.Value != 0x3000 {
		t.Fatalf("越过上限: reply=%+v want 当前 break", reply)
	}

	heap.err = errno.Cancelled
	if reply := syscalls.Handle(context.Background(), Call{Syscall: SysBrk, Args: [6]uint64{0x3000}}); reply.Errno != errno.Cancelled {
		t.Fatalf("内部错误: reply=%+v want ECANCELED", reply)
	}

	bare := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{}}
	if reply := bare.Handle(context.Background(), Call{Syscall: SysBrk, Args: [6]uint64{0x1000}}); reply.Errno != errno.NotImplemented {
		t.Fatalf("缺端口: reply=%+v want ENOSYS", reply)
	}
}

type fakeMapper struct {
	mapped    []MapRequest
	unmapped  [][2]uint64
	protected [][3]uint64
	remapped  [][5]uint64
	advised   [][3]uint64
	reply     uint64
	err       error
}

func (m *fakeMapper) Anonymous(_ context.Context, request MapRequest) (uint64, error) {
	m.mapped = append(m.mapped, request)
	if m.err != nil {
		return 0, m.err
	}
	return m.reply, nil
}

func (m *fakeMapper) Unmap(_ context.Context, start, end uint64) error {
	m.unmapped = append(m.unmapped, [2]uint64{start, end})
	return m.err
}

func (m *fakeMapper) Protect(_ context.Context, start, end uint64, prot uint64) error {
	m.protected = append(m.protected, [3]uint64{start, end, prot})
	return m.err
}

func (m *fakeMapper) Mremap(_ context.Context, oldStart, oldEnd, newEnd uint64, mayMove, fixed bool, fixedStart uint64) (uint64, error) {
	m.remapped = append(m.remapped, [5]uint64{oldStart, oldEnd, newEnd, boolToU64(mayMove), boolToU64(fixed)})
	if m.err != nil {
		return 0, m.err
	}
	return fixedStart, nil
}

func (m *fakeMapper) Madvise(_ context.Context, start, end uint64, advice uint64) error {
	m.advised = append(m.advised, [3]uint64{start, end, advice})
	return m.err
}

func boolToU64(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}

func TestMmapValidatesAndDelegates(t *testing.T) {
	ctx := context.Background()
	anon := uint64(mapPrivate | mapAnonymous)
	for _, tc := range []struct {
		label string
		args  [6]uint64
		want  errno.Code
	}{
		{"零长度", [6]uint64{0, 0, ProtRead, anon, ^uint64(0), 0}, errno.Invalid},
		{"未知标志", [6]uint64{0, 4096, ProtRead, anon | 0x4000, ^uint64(0), 0}, errno.Invalid},
		{"共享映射", [6]uint64{0, 4096, ProtRead, mapAnonymous, ^uint64(0), 0}, errno.NotSupported},
		{"文件映射", [6]uint64{0, 4096, ProtRead, mapPrivate, 3, 0}, errno.NotSupported},
		{"非零偏移", [6]uint64{0, 4096, ProtRead, anon, ^uint64(0), 4096}, errno.Invalid},
		{"提示未对齐", [6]uint64{4097, 4096, ProtRead, anon, ^uint64(0), 0}, errno.Invalid},
	} {
		mapper := &fakeMapper{}
		handler := Syscalls{Mapper: mapper}
		if reply := handler.Handle(ctx, Call{Syscall: SysMmap, Args: tc.args}); reply.Errno != tc.want {
			t.Errorf("%s: err=%v want %v", tc.label, reply.Errno, tc.want)
		}
		if len(mapper.mapped) != 0 {
			t.Errorf("%s: 被拒绝的调用不得触达地址空间", tc.label)
		}
	}

	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysMmap}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配 Mapper 应报 ENOSYS: %v", reply.Errno)
	}

	mapper := &fakeMapper{reply: 0x1000}
	handler := Syscalls{Mapper: mapper}
	reply := handler.Handle(ctx, Call{Syscall: SysMmap, Args: [6]uint64{0, 4097, ProtRead | ProtWrite, anon, ^uint64(0), 0}})
	if reply.Errno != "" || reply.Value != 0x1000 {
		t.Fatalf("正常映射: %+v", reply)
	}
	if len(mapper.mapped) != 1 || mapper.mapped[0].Length != 8192 {
		t.Fatalf("长度应向上取整到页: %+v", mapper.mapped)
	}

	if reply := handler.Handle(ctx, Call{Syscall: SysMmap, Args: [6]uint64{0x5000, 4096, ProtRead, anon | mapFixed | mapFixedNoReplace, ^uint64(0), 0}}); reply.Errno != "" {
		t.Fatalf("固定映射: %v", reply.Errno)
	}
	request := mapper.mapped[1]
	if !request.Fixed || !request.NoReplace || request.Hint != 0x5000 {
		t.Fatalf("FIXED/NOREPLACE 标志应传递: %+v", request)
	}
}

func TestMunmapValidatesAndDelegates(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label string
		args  [6]uint64
		want  errno.Code
	}{
		{"起始未对齐", [6]uint64{4097, 4096}, errno.Invalid},
		{"零长度", [6]uint64{4096, 0}, errno.Invalid},
		{"范围溢出", [6]uint64{^uint64(0) - 4095, 1 << 63}, errno.Overflow},
	} {
		mapper := &fakeMapper{}
		handler := Syscalls{Mapper: mapper}
		if reply := handler.Handle(ctx, Call{Syscall: SysMunmap, Args: tc.args}); reply.Errno != tc.want {
			t.Errorf("%s: err=%v want %v", tc.label, reply.Errno, tc.want)
		}
		if len(mapper.unmapped) != 0 {
			t.Errorf("%s: 被拒绝的调用不得触达地址空间", tc.label)
		}
	}

	mapper := &fakeMapper{}
	handler := Syscalls{Mapper: mapper}
	if reply := handler.Handle(ctx, Call{Syscall: SysMunmap, Args: [6]uint64{4096, 5000}}); reply.Errno != "" {
		t.Fatalf("正常解除: %v", reply.Errno)
	}
	if len(mapper.unmapped) != 1 || mapper.unmapped[0] != [2]uint64{4096, 12288} {
		t.Fatalf("解除范围应页对齐: %+v", mapper.unmapped)
	}
}

func TestMprotectValidatesAndDelegates(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label string
		args  [6]uint64
		want  errno.Code
	}{
		{"起始未对齐", [6]uint64{4097, 4096, ProtRead}, errno.Invalid},
		{"零长度", [6]uint64{4096, 0, ProtRead}, errno.Invalid},
		{"未知保护位", [6]uint64{4096, 4096, 0x8}, errno.Invalid},
		{"范围溢出", [6]uint64{^uint64(0) - 4095, 1 << 63, ProtRead}, errno.Overflow},
	} {
		mapper := &fakeMapper{}
		handler := Syscalls{Mapper: mapper}
		if reply := handler.Handle(ctx, Call{Syscall: SysMprotect, Args: tc.args}); reply.Errno != tc.want {
			t.Errorf("%s: err=%v want %v", tc.label, reply.Errno, tc.want)
		}
		if len(mapper.protected) != 0 {
			t.Errorf("%s: 被拒绝的调用不得触达地址空间", tc.label)
		}
	}

	mapper := &fakeMapper{}
	handler := Syscalls{Mapper: mapper}
	if reply := handler.Handle(ctx, Call{Syscall: SysMprotect, Args: [6]uint64{4096, 4097, ProtRead | ProtExec}}); reply.Errno != "" {
		t.Fatalf("正常改权限: %v", reply.Errno)
	}
	if len(mapper.protected) != 1 || mapper.protected[0] != [3]uint64{4096, 12288, ProtRead | ProtExec} {
		t.Fatalf("范围应向上取整到页且保护位透传: %+v", mapper.protected)
	}

	mapper.err = errno.Range
	if reply := handler.Handle(ctx, Call{Syscall: SysMprotect, Args: [6]uint64{4096, 4096, ProtRead}}); reply.Errno != errno.Range {
		t.Fatalf("实现层错误应透传: %v", reply.Errno)
	}
}

type fakeSleeper struct {
	durations []uint64
	err       error
	onSleep   func(nanoseconds uint64)
}

func (s *fakeSleeper) Sleep(_ context.Context, nanoseconds uint64) error {
	s.durations = append(s.durations, nanoseconds)
	if s.onSleep != nil {
		s.onSleep(nanoseconds)
	}
	return s.err
}

func TestClockNanosleepRelative(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{wall: 1_000, monotonic: 5_000}
	sleeper := &fakeSleeper{}
	handler := Syscalls{Memory: newFakeMemory(), Clock: clock, Sleeper: sleeper}

	request := make([]byte, 16)
	binary.LittleEndian.PutUint64(request[0:], 2)
	binary.LittleEndian.PutUint64(request[8:], 500)
	if err := handler.Memory.Write(ctx, 0x2000, request); err != nil {
		t.Fatal(err)
	}
	reply := handler.Handle(ctx, Call{Syscall: SysClockNanosleep, Args: [6]uint64{clockMonotonic, 0, 0x2000}})
	if reply.Errno != "" {
		t.Fatalf("相对睡眠应成功: %v", reply.Errno)
	}
	if len(sleeper.durations) != 1 || sleeper.durations[0] != 2*nanosPerSecond+500 {
		t.Fatalf("应等待请求的时长: %+v", sleeper.durations)
	}

	// 零时长直接成功。
	binary.LittleEndian.PutUint64(request[0:], 0)
	binary.LittleEndian.PutUint64(request[8:], 0)
	if reply := handler.Handle(ctx, Call{Syscall: SysClockNanosleep, Args: [6]uint64{clockRealtime, 0, 0x2000}}); reply.Errno != "" {
		t.Fatalf("零时长应成功: %v", reply.Errno)
	}
}

func TestClockNanosleepValidates(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{}
	handler := Syscalls{Memory: newFakeMemory(), Clock: clock, Sleeper: &fakeSleeper{}}

	request := make([]byte, 16)
	binary.LittleEndian.PutUint64(request[8:], nanosPerSecond)
	if err := handler.Memory.Write(ctx, 0x2000, request); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label string
		args  [6]uint64
		want  errno.Code
	}{
		{"未知时钟", [6]uint64{99, 0, 0x2000}, errno.Invalid},
		{"纳秒越界", [6]uint64{clockMonotonic, 0, 0x2000}, errno.Invalid},
		{"未知标志", [6]uint64{clockMonotonic, 2, 0x2000}, errno.Invalid},
	} {
		if reply := handler.Handle(ctx, Call{Syscall: SysClockNanosleep, Args: tc.args}); reply.Errno != tc.want {
			t.Errorf("%s: err=%v want %v", tc.label, reply.Errno, tc.want)
		}
	}

	faulty := Syscalls{Memory: &fakeMemory{readErr: errno.Fault}, Clock: clock, Sleeper: &fakeSleeper{}}
	if reply := faulty.Handle(ctx, Call{Syscall: SysClockNanosleep, Args: [6]uint64{clockMonotonic, 0, 0x2000}}); reply.Errno != errno.Fault {
		t.Errorf("请求指针坏: err=%v want EFAULT", reply.Errno)
	}

	if reply := (Syscalls{Memory: newFakeMemory(), Clock: clock}).Handle(ctx, Call{Syscall: SysClockNanosleep}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配 Sleeper 应报 ENOSYS: %v", reply.Errno)
	}
}

func TestClockNanosleepInterruptReportsRemaining(t *testing.T) {
	ctx := context.Background()
	// 相对睡眠被中断:单调钟在等待期间前进 1 秒,剩余应为 1 秒。
	clock := &fakeClock{monotonic: 10 * nanosPerSecond}
	sleeper := &fakeSleeper{err: errno.Interrupted, onSleep: func(uint64) {
		clock.monotonic += nanosPerSecond
	}}
	handler := Syscalls{Memory: newFakeMemory(), Clock: clock, Sleeper: sleeper}

	request := make([]byte, 16)
	binary.LittleEndian.PutUint64(request[0:], 2)
	if err := handler.Memory.Write(ctx, 0x2000, request); err != nil {
		t.Fatal(err)
	}
	remain := make([]byte, 16)
	if err := handler.Memory.Write(ctx, 0x3000, remain); err != nil {
		t.Fatal(err)
	}
	reply := handler.Handle(ctx, Call{Syscall: SysClockNanosleep, Args: [6]uint64{clockMonotonic, 0, 0x2000, 0x3000}})
	if reply.Errno != errno.Interrupted {
		t.Fatalf("中断应返回 EINTR: %v", reply.Errno)
	}
	if err := handler.Memory.Read(ctx, 0x3000, remain); err != nil {
		t.Fatal(err)
	}
	gotSeconds := binary.LittleEndian.Uint64(remain[0:])
	gotNanos := binary.LittleEndian.Uint64(remain[8:])
	if gotSeconds != 1 || gotNanos != 0 {
		t.Fatalf("剩余时间=%ds+%dns want 1s+0ns", gotSeconds, gotNanos)
	}

	// 绝对期限被中断时不回填剩余时间。
	absoluteClock := &fakeClock{monotonic: nanosPerSecond}
	abst := &fakeSleeper{err: errno.Interrupted, onSleep: func(uint64) {
		absoluteClock.monotonic += nanosPerSecond / 2
	}}
	abshandler := Syscalls{Memory: newFakeMemory(), Clock: absoluteClock, Sleeper: abst}
	request[0], request[8] = 3, 0
	if err := abshandler.Memory.Write(ctx, 0x2000, request); err != nil {
		t.Fatal(err)
	}
	reply = abshandler.Handle(ctx, Call{Syscall: SysClockNanosleep, Args: [6]uint64{clockMonotonic, timerAbstime, 0x2000, 0x3000}})
	if reply.Errno != errno.Interrupted {
		t.Fatalf("绝对期限中断应返回 EINTR: %v", reply.Errno)
	}
}

func TestClockNanosleepAbsoluteDeadline(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{monotonic: 2 * nanosPerSecond}
	handler := Syscalls{Memory: newFakeMemory(), Clock: clock, Sleeper: &fakeSleeper{}}

	request := make([]byte, 16)
	binary.LittleEndian.PutUint64(request[0:], 1)
	if err := handler.Memory.Write(ctx, 0x2000, request); err != nil {
		t.Fatal(err)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysClockNanosleep, Args: [6]uint64{clockMonotonic, timerAbstime, 0x2000}}); reply.Errno != "" {
		t.Fatalf("期限已过应直接成功: %v", reply.Errno)
	}

	binary.LittleEndian.PutUint64(request[0:], 3)
	if err := handler.Memory.Write(ctx, 0x2000, request); err != nil {
		t.Fatal(err)
	}
	// 等待一次后时钟推进到期限,循环应在下一次检查时退出。
	sleeper := handler.Sleeper.(*fakeSleeper)
	sleeper.onSleep = func(nanoseconds uint64) {
		if nanoseconds != nanosPerSecond {
			t.Errorf("应等待到期限还差的 1 秒: %d", nanoseconds)
		}
		clock.monotonic += nanoseconds
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysClockNanosleep, Args: [6]uint64{clockMonotonic, timerAbstime, 0x2000}}); reply.Errno != "" {
		t.Fatalf("未到期应等待: %v", reply.Errno)
	}
	if len(sleeper.durations) != 1 {
		t.Fatalf("到期后不得继续等待: %+v", sleeper.durations)
	}
}

func TestEventfd2ValidatesAndDelegates(t *testing.T) {
	ctx := context.Background()
	descriptors := &fakeDescriptors{}
	handler := Syscalls{Descriptors: descriptors}

	for _, tc := range []struct {
		label string
		args  [6]uint64
		want  errno.Code
	}{
		{"未知标志", [6]uint64{5, efdFlagMask + 1}, errno.Invalid},
	} {
		if reply := handler.Handle(ctx, Call{Syscall: SysEventfd2, Args: tc.args}); reply.Errno != tc.want {
			t.Errorf("%s: err=%v want %v", tc.label, reply.Errno, tc.want)
		}
		if len(descriptors.eventfd) != 0 {
			t.Errorf("%s: 被拒绝的调用不得触达描述符表", tc.label)
		}
	}

	reply := handler.Handle(ctx, Call{Syscall: SysEventfd2, Args: [6]uint64{5, efdSemaphore | efdNonBlock | efdCloexec}})
	if reply.Errno != "" || reply.Value != 33 {
		t.Fatalf("正常创建: %+v", reply)
	}
	if len(descriptors.eventfd) != 1 || descriptors.eventfd[0] != [2]uint64{5, efdSemaphore | efdNonBlock | efdCloexec} {
		t.Fatalf("初值与标志应透传: %+v", descriptors.eventfd)
	}

	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysEventfd2}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配描述符应报 ENOSYS: %v", reply.Errno)
	}

	descriptors.eventfdErr = errno.Limit
	if reply := handler.Handle(ctx, Call{Syscall: SysEventfd2, Args: [6]uint64{5}}); reply.Errno != errno.Limit {
		t.Fatalf("实现层错误应透传: %v", reply.Errno)
	}
}

type fakeLimiter struct {
	limit  uint64
	ok     bool
	set    uint64
	setErr error
}

func (l *fakeLimiter) DescriptorLimit(context.Context) (uint64, bool) { return l.limit, l.ok }

func (l *fakeLimiter) SetDescriptorLimit(_ context.Context, value uint64) error {
	if l.setErr != nil {
		return l.setErr
	}
	l.limit, l.ok, l.set = value, true, value
	return nil
}

func TestGetrlimitReportsConfiguredLimits(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	handler := Syscalls{Memory: memory, Limiter: &fakeLimiter{limit: 64, ok: true}}

	if reply := handler.Handle(ctx, Call{Syscall: SysGetrlimit, Args: [6]uint64{rlimitNoFile, 0x2000}}); reply.Errno != "" {
		t.Fatalf("NOFILE 查询: %v", reply.Errno)
	}
	var encoded [16]byte
	if err := memory.Read(ctx, 0x2000, encoded[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint64(encoded[0:]); got != 64 {
		t.Fatalf("NOFILE 应报告真实限额 64, got %d", got)
	}

	if reply := handler.Handle(ctx, Call{Syscall: SysGetrlimit, Args: [6]uint64{9, 0x2100}}); reply.Errno != "" {
		t.Fatalf("未配置资源查询: %v", reply.Errno)
	}
	if err := memory.Read(ctx, 0x2100, encoded[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint64(encoded[0:]); got != rlimInfinity {
		t.Fatalf("未配置资源应报告 RLIM_INFINITY, got %d", got)
	}

	if reply := handler.Handle(ctx, Call{Syscall: SysGetrlimit, Args: [6]uint64{rlimitNoFile, 0}}); reply.Errno != errno.Invalid {
		t.Fatalf("空指针应报 EINVAL: %v", reply.Errno)
	}
}

func TestSetrlimitCannotExceedRealBudget(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	limiter := &fakeLimiter{limit: 64, ok: true, setErr: errno.Permission}
	handler := Syscalls{Memory: memory, Limiter: limiter}

	lower := make([]byte, 16)
	binary.LittleEndian.PutUint64(lower[0:], 16)
	binary.LittleEndian.PutUint64(lower[8:], 16)
	if err := memory.Write(ctx, 0x2000, lower); err != nil {
		t.Fatal(err)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysSetrlimit, Args: [6]uint64{rlimitNoFile, 0x2000}}); reply.Errno != errno.Permission {
		t.Fatalf("限额设置被真实预算拒绝时应返回 EPERM: %v", reply.Errno)
	}

	limiter.setErr = nil
	if reply := handler.Handle(ctx, Call{Syscall: SysSetrlimit, Args: [6]uint64{rlimitNoFile, 0x2000}}); reply.Errno != "" {
		t.Fatalf("预算内下调应成功: %v", reply.Errno)
	}
	if limiter.set != 16 || limiter.limit != 16 {
		t.Fatalf("下调应写入限额器: %d", limiter.set)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysGetrlimit, Args: [6]uint64{rlimitNoFile, 0x2100}}); reply.Errno != "" {
		t.Fatal(reply.Errno)
	}
	var encoded [16]byte
	if err := memory.Read(ctx, 0x2100, encoded[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint64(encoded[0:]); got != 16 {
		t.Fatalf("查询应反映下调后的限额: %d", got)
	}

	above := make([]byte, 16)
	binary.LittleEndian.PutUint64(above[0:], 32)
	binary.LittleEndian.PutUint64(above[8:], 16)
	if err := memory.Write(ctx, 0x2200, above); err != nil {
		t.Fatal(err)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysSetrlimit, Args: [6]uint64{rlimitNoFile, 0x2200}}); reply.Errno != errno.Invalid {
		t.Fatalf("cur 超过 max 应报 EINVAL: %v", reply.Errno)
	}

	if reply := handler.Handle(ctx, Call{Syscall: SysSetrlimit, Args: [6]uint64{9, 0x2000}}); reply.Errno != errno.NotSupported {
		t.Fatalf("未配置资源的设置应明确拒绝: %v", reply.Errno)
	}
}

func TestPrlimit64WritesOldAndAppliesNew(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	limiter := &fakeLimiter{limit: 64, ok: true}
	handler := Syscalls{Memory: memory, Limiter: limiter}

	request := make([]byte, 16)
	binary.LittleEndian.PutUint64(request[0:], 8)
	binary.LittleEndian.PutUint64(request[8:], 8)
	if err := memory.Write(ctx, 0x2000, request); err != nil {
		t.Fatal(err)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysPrlimit64, Args: [6]uint64{0, rlimitNoFile, 0x2000, 0x3000}}); reply.Errno != "" {
		t.Fatalf("prlimit 设置: %v", reply.Errno)
	}
	if limiter.set != 8 {
		t.Fatalf("新限额应生效: %d", limiter.set)
	}
	var encoded [16]byte
	if err := memory.Read(ctx, 0x3000, encoded[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint64(encoded[0:]); got != 8 {
		t.Fatalf("旧值指针应收到设置后的限额: %d", got)
	}
}

func TestUnameAndSysinfoUseConfiguredIdentity(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	handler := Syscalls{Memory: memory, Sysname: "Linux", Release: "6.8.0-vm", Machine: "riscv64"}

	if reply := handler.Handle(ctx, Call{Syscall: SysUname, Args: [6]uint64{0x2000}}); reply.Errno != "" {
		t.Fatalf("uname: %v", reply.Errno)
	}
	var encoded [390]byte
	if err := memory.Read(ctx, 0x2000, encoded[:]); err != nil {
		t.Fatal(err)
	}
	if got := cstring(encoded[0*65 : 1*65]); got != "Linux" {
		t.Fatalf("sysname=%q want Linux", got)
	}
	if got := cstring(encoded[2*65 : 3*65]); got != "6.8.0-vm" {
		t.Fatalf("release=%q want 6.8.0-vm", got)
	}
	if got := cstring(encoded[4*65 : 5*65]); got != "riscv64" {
		t.Fatalf("machine=%q want riscv64", got)
	}
	if got := cstring(encoded[1*65 : 2*65]); got != "assistant-sandbox" {
		t.Fatalf("nodename 应来自客体配置而非宿主: %q", got)
	}

	if reply := handler.Handle(ctx, Call{Syscall: SysSysinfo, Args: [6]uint64{0x3000}}); reply.Errno != "" {
		t.Fatalf("sysinfo: %v", reply.Errno)
	}
	var info [128]byte
	if err := memory.Read(ctx, 0x3000, info[:]); err != nil {
		t.Fatal(err)
	}
	// 内存/负载/交换空间字段必须为零填充,不泄露宿主数值;仅 procs=1。
	for index, b := range info {
		if index == 80 {
			continue
		}
		if b != 0 {
			t.Fatalf("sysinfo[%d]=%d 应为零填充", index, b)
		}
	}
	if info[80] != 1 {
		t.Fatalf("procs 应为 1: %d", info[80])
	}
}

func cstring(raw []byte) string {
	if index := bytes.IndexByte(raw, 0); index >= 0 {
		return string(raw[:index])
	}
	return string(raw)
}

type namedHandle struct{ name string }

func (n namedHandle) Name() string { return n.name }

// dirEntryInfo 是携带扩展元数据的目录项替身。
type dirEntryInfo struct {
	name     string
	mode     stdfs.FileMode
	metadata *vmfs.Metadata
}

func (e dirEntryInfo) Name() string { return e.name }
func (e dirEntryInfo) IsDir() bool  { return e.mode&stdfs.ModeDir != 0 }
func (e dirEntryInfo) Type() stdfs.FileMode {
	return e.mode.Type()
}
func (e dirEntryInfo) Info() (stdfs.FileInfo, error) {
	return statFileInfo{name: e.name, mode: e.mode, sys: e.metadata}, nil
}

// dirHandle 是实现 Readdir 的目录句柄替身:每次调用按序返回一批条目,
// 模拟 memfs 游标推进。
type dirHandle struct {
	batches [][]stdfs.DirEntry
	calls   []int
}

func (h *dirHandle) Readdir(limit int) ([]stdfs.DirEntry, error) {
	if limit <= 0 {
		return nil, errno.Invalid
	}
	h.calls = append(h.calls, limit)
	if len(h.batches) == 0 {
		return nil, nil
	}
	batch := h.batches[0]
	h.batches = h.batches[1:]
	return batch, nil
}

func TestGetdents64EncodesRecordsWithHandleCursor(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	handle := &dirHandle{batches: [][]stdfs.DirEntry{{
		dirEntryInfo{name: "a.txt", metadata: &vmfs.Metadata{Inode: 11}},
		dirEntryInfo{name: "sub", metadata: &vmfs.Metadata{Inode: 22}, mode: stdfs.ModeDir | 0o755},
		dirEntryInfo{name: "lnk", metadata: &vmfs.Metadata{Inode: 33}, mode: stdfs.ModeSymlink | 0o777},
	}}}
	descriptors := &fakeDescriptors{get: handle}
	handler := Syscalls{Memory: memory, Descriptors: descriptors}

	// 全部三条记录一次写入:d_ino/d_off 取句柄声明的 inode 号,
	// d_type 按模式区分,记录按 8 字节对齐且填充为零。
	if reply := handler.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{3, 0x2000, 256}}); reply.Errno != "" || reply.Value != 80 {
		t.Fatalf("getdents64: %+v want 80 字节", reply)
	}
	var raw [80]byte
	if err := memory.Read(ctx, 0x2000, raw[:]); err != nil {
		t.Fatal(err)
	}
	expected := []struct {
		ino, off, reclen uint64
		dt               byte
		name             string
	}{
		{11, 11, 32, 8, "a.txt"},
		{22, 22, 24, 4, "sub"},
		{33, 33, 24, 10, "lnk"},
	}
	for index, cursor := 0, 0; index < len(expected); index++ {
		reclen := binary.LittleEndian.Uint16(raw[cursor+16:])
		record := raw[cursor : cursor+int(reclen)]
		cursor += int(reclen)
		want := expected[index]
		if ino := binary.LittleEndian.Uint64(record[0:]); ino != want.ino {
			t.Fatalf("第 %d 项 d_ino=%d want %d", index, ino, want.ino)
		}
		if off := binary.LittleEndian.Uint64(record[8:]); off != want.off {
			t.Fatalf("第 %d 项 d_off=%d want %d", index, off, want.off)
		}
		if reclen != uint16(want.reclen) {
			t.Fatalf("第 %d 项 d_reclen=%d want %d", index, reclen, want.reclen)
		}
		if record[18] != want.dt {
			t.Fatalf("第 %d 项 d_type=%d want %d", index, record[18], want.dt)
		}
		if got := cstring(record[19:]); got != want.name {
			t.Fatalf("第 %d 项名字=%q want %q", index, got, want.name)
		}
	}
	// 游标已读完:下一次调用返回 0。
	if reply := handler.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{3, 0x2000, 256}}); reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("目录读完应返回 0: %+v", reply)
	}
}

func TestGetdents64RejectsTinyBuffersAndBadHandles(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	handle := &dirHandle{batches: [][]stdfs.DirEntry{{
		dirEntryInfo{name: "a.txt", metadata: &vmfs.Metadata{Inode: 11}},
	}}}
	handler := Syscalls{Memory: memory, Descriptors: &fakeDescriptors{get: handle}}

	// 缓冲区容不下一条记录报 EINVAL。
	if reply := handler.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{3, 0x2000, 23}}); reply.Errno != errno.Invalid {
		t.Fatalf("过小缓冲区: %+v want EINVAL", reply)
	}
	// 零长度缓冲区报 EINVAL。
	if reply := handler.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{3, 0x2000, 0}}); reply.Errno != errno.Invalid {
		t.Fatalf("零长度: %+v want EINVAL", reply)
	}
	// 非目录句柄明确 ENOTSUP。
	notDir := Syscalls{Memory: memory, Descriptors: &fakeDescriptors{get: namedHandle{name: "/tmp"}}}
	if reply := notDir.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{3, 0x2000, 256}}); reply.Errno != errno.NotSupported {
		t.Fatalf("非目录句柄: %+v want ENOTSUP", reply)
	}
	// 未装配端口报 ENOSYS。
	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysGetdents64}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配应报 ENOSYS: %v", reply.Errno)
	}
	// 坏描述符透传 EBADF。
	badFD := Syscalls{Memory: memory, Descriptors: &fakeDescriptors{seekErr: errno.BadHandle}}
	if reply := badFD.Handle(ctx, Call{Syscall: SysGetdents64, Args: [6]uint64{9, 0x2000, 256}}); reply.Errno != errno.BadHandle {
		t.Fatalf("描述符错误应透传: %v", reply.Errno)
	}
}

func TestUnlinkatDistinguishesFilesAndDirectories(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	if err := memory.Write(ctx, 0x1000, []byte("/tmp\x00")); err != nil {
		t.Fatal(err)
	}
	files := &fakeFiles{}
	handler := Syscalls{Memory: memory, Files: files}

	files.statInfo = fakeFileInfo{dir: true}
	if reply := handler.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0}}); reply.Errno != errno.IsDirectory {
		t.Fatalf("unlink 目录应报 EISDIR: %v", reply.Errno)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, atRemovedir}}); reply.Errno != "" {
		t.Fatalf("AT_REMOVEDIR 删目录应成功: %v", reply.Errno)
	}

	files.statInfo = fakeFileInfo{dir: false}
	if reply := handler.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, atRemovedir}}); reply.Errno != errno.NotDirectory {
		t.Fatalf("AT_REMOVEDIR 删文件应报 ENOTDIR: %v", reply.Errno)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0}}); reply.Errno != "" {
		t.Fatalf("unlink 文件应成功: %v", reply.Errno)
	}
	if len(files.removeCalls) != 2 || files.removeCalls[0] != "/tmp" {
		t.Fatalf("Remove 应收到解析后的路径: %+v", files.removeCalls)
	}
}

func TestMremapValidatesAndDelegates(t *testing.T) {
	ctx := context.Background()
	mapper := &fakeMapper{}
	handler := Syscalls{Mapper: mapper}

	for _, tc := range []struct {
		label string
		args  [6]uint64
		want  errno.Code
	}{
		{"旧地址未对齐", [6]uint64{4097, 4096, 8192, mremapMayMove}, errno.Invalid},
		{"旧大小为零", [6]uint64{4096, 0, 8192, mremapMayMove}, errno.Invalid},
		{"新大小为零", [6]uint64{4096, 4096, 0, mremapMayMove}, errno.Invalid},
		{"未知标志", [6]uint64{4096, 4096, 8192, 0x8}, errno.Invalid},
		{"FIXED 缺 MAYMOVE", [6]uint64{4096, 4096, 8192, mremapFixed}, errno.Invalid},
		{"FIXED 地址未对齐", [6]uint64{4096, 4096, 8192, mremapMayMove | mremapFixed, 4097}, errno.Invalid},
	} {
		if reply := handler.Handle(ctx, Call{Syscall: SysMremap, Args: tc.args}); reply.Errno != tc.want {
			t.Errorf("%s: err=%v want %v", tc.label, reply.Errno, tc.want)
		}
		if len(mapper.remapped) != 0 {
			t.Errorf("%s: 被拒绝的调用不得触达地址空间", tc.label)
		}
	}

	if reply := handler.Handle(ctx, Call{Syscall: SysMremap, Args: [6]uint64{4096, 4097, 8193, mremapMayMove | mremapFixed, 8192}}); reply.Errno != "" {
		t.Fatalf("正常重映射: %v", reply.Errno)
	}
	call := mapper.remapped[0]
	if call[0] != 4096 || call[1] != 12288 || call[2] != 16384 || call[3] != 1 || call[4] != 1 {
		t.Fatalf("范围与标志应对齐到页并透传: %+v", call)
	}

	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysMremap}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配 Mapper 应报 ENOSYS: %v", reply.Errno)
	}
}

func TestMadviseValidatesAndDelegates(t *testing.T) {
	ctx := context.Background()
	mapper := &fakeMapper{}
	handler := Syscalls{Mapper: mapper}

	if reply := handler.Handle(ctx, Call{Syscall: SysMadvise, Args: [6]uint64{4097, 4096, MadvDontneed}}); reply.Errno != errno.Invalid {
		t.Fatalf("起始未对齐应报 EINVAL: %v", reply.Errno)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysMadvise, Args: [6]uint64{4096, 4096, 0x30}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知 advice 应报 EINVAL: %v", reply.Errno)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysMadvise, Args: [6]uint64{4096, 0, MadvDontneed}}); reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("零长度应直接成功: %+v", reply)
	}
	if len(mapper.advised) != 0 {
		t.Fatalf("零长度不得触达地址空间")
	}

	if reply := handler.Handle(ctx, Call{Syscall: SysMadvise, Args: [6]uint64{4096, 4097, MadvDontneed}}); reply.Errno != "" {
		t.Fatalf("正常 advice: %v", reply.Errno)
	}
	if mapper.advised[0] != [3]uint64{4096, 12288, MadvDontneed} {
		t.Fatalf("范围应向上取整并透传 advice: %+v", mapper.advised[0])
	}

	mapper.err = errno.NoMemory
	if reply := handler.Handle(ctx, Call{Syscall: SysMadvise, Args: [6]uint64{4096, 4096, MadvDontneed}}); reply.Errno != errno.NoMemory {
		t.Fatalf("实现层错误应透传: %v", reply.Errno)
	}
}

func TestReadStringVectorBoundsAndTermination(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	handler := Syscalls{Memory: memory, VectorLimit: 8}

	// 三个字符串指针 + NULL:hello / w / 空串。
	put := func(address uint64, value string) {
		data := append([]byte(value), 0)
		if err := memory.Write(ctx, address, data); err != nil {
			t.Fatal(err)
		}
	}
	put(0x1000, "hello")
	put(0x1100, "w")
	put(0x1200, "")
	writePtr := func(slot uint64, target uint64) {
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], target)
		if err := memory.Write(ctx, slot, raw[:]); err != nil {
			t.Fatal(err)
		}
	}
	writePtr(0x2000, 0x1000)
	writePtr(0x2008, 0x1100)
	writePtr(0x2010, 0x1200)
	writePtr(0x2018, 0)

	got, code := handler.readStringVector(ctx, 0x2000)
	if code != "" {
		t.Fatalf("向量读取: %v", code)
	}
	if len(got) != 3 || got[0] != "hello" || got[1] != "w" || got[2] != "" {
		t.Fatalf("向量内容不符: %q", got)
	}

	// 字符串读取越界:EFAULT(fakeMemory 默认零填充,用 readErr 触发)。
	faulty := Syscalls{Memory: &fakeMemory{readErr: errno.Fault}, VectorLimit: 8}
	if _, code := faulty.readStringVector(ctx, 0x2000); code != errno.Fault {
		t.Fatalf("数组读取越界应报 EFAULT: %v", code)
	}

	// 条目数超上限:E2BIG。
	limit := handler.vectorLimit()
	for slot, target := uint64(0x2000), uint64(0x1000); slot < 0x2000+limit*8; slot, target = slot+8, target+8 {
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], target)
		if err := memory.Write(ctx, slot, raw[:]); err != nil {
			t.Fatal(err)
		}
		if err := memory.Write(ctx, target, []byte{0}); err != nil {
			t.Fatal(err)
		}
	}
	if _, code := handler.readStringVector(ctx, 0x2000); code != errno.TooBig {
		t.Fatalf("超上限应报 E2BIG: %v", code)
	}
}

func TestReadIovecsBounds(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	handler := Syscalls{Memory: memory}
	raw := make([]byte, 2*16)
	binary.LittleEndian.PutUint64(raw[0:], 0x5000)
	binary.LittleEndian.PutUint64(raw[8:], 16)
	binary.LittleEndian.PutUint64(raw[16:], 0x6000)
	binary.LittleEndian.PutUint64(raw[24:], 32)
	if err := memory.Write(ctx, 0x7000, raw); err != nil {
		t.Fatal(err)
	}

	got, code := handler.readIovecs(ctx, 0x7000, 2)
	if code != "" {
		t.Fatalf("iovec 读取: %v", code)
	}
	if len(got) != 2 || got[0] != (Iovec{Base: 0x5000, Length: 16}) || got[1] != (Iovec{Base: 0x6000, Length: 32}) {
		t.Fatalf("iovec 内容不符: %+v", got)
	}

	if _, code := handler.readIovecs(ctx, 0x7000, iovMax+1); code != errno.Invalid {
		t.Fatalf("超 IOV_MAX 应报 EINVAL: %v", code)
	}
	if _, code := handler.readIovecs(ctx, 0x7000, 0); code != errno.Invalid {
		t.Fatalf("零长度应报 EINVAL: %v", code)
	}
	faulty := Syscalls{Memory: &fakeMemory{readErr: errno.Fault}}
	if _, code := faulty.readIovecs(ctx, 0x7000, 2); code != errno.Fault {
		t.Fatalf("越界数组应报 EFAULT: %v", code)
	}
}

func TestEncodeStatMatchesAsmGenericLayout(t *testing.T) {
	// 逐字段断言与 riscv64 asm-generic struct stat 布局一致(128 字节)。
	input := StatInput{
		Dev: 0x102, Ino: 0x1234, Mode: 0o100644, NLink: 3, UID: 1000, GID: 1000,
		RDev: 0, Size: 0xABC, Blksize: 4096, Blocks: 24,
		AtimeSec: 1700000000, AtimeNsec: 111, MtimeSec: 1700000001, MtimeNsec: 222,
		CtimeSec: 1700000002, CtimeNsec: 333,
	}
	encoded := encodeStat(input)
	if len(encoded) != 128 {
		t.Fatalf("struct stat 应为 128 字节: %d", len(encoded))
	}
	assertU64 := func(offset int, want uint64) {
		if got := binary.LittleEndian.Uint64(encoded[offset:]); got != want {
			t.Errorf("offset %d: %#x want %#x", offset, got, want)
		}
	}
	assertU32 := func(offset int, want uint32) {
		if got := binary.LittleEndian.Uint32(encoded[offset:]); got != want {
			t.Errorf("offset %d: %#x want %#x", offset, got, want)
		}
	}
	assertU64(statDev, 0x102)
	assertU64(statIno, 0x1234)
	assertU32(statMode, 0o100644)
	assertU32(statNLink, 3)
	assertU32(statUID, 1000)
	assertU32(statGID, 1000)
	assertU64(statRDev, 0)
	assertU64(statSize, 0xABC)
	assertU32(statBlksize, 4096)
	assertU64(statBlocks, 24)
	assertU64(statAtime, 1700000000)
	assertU64(statAtimeNsec, 111)
	assertU64(statMtime, 1700000001)
	assertU64(statMtimeNsec, 222)
	assertU64(statCtime, 1700000002)
	assertU64(statCtimeNsec, 333)
	// 填充字段必须为零。
	for _, offset := range []int{40, 60, 120, 124} {
		assertU32(offset, 0)
	}

	iov := encodeIovec(0x5000, 16)
	if binary.LittleEndian.Uint64(iov[0:]) != 0x5000 || binary.LittleEndian.Uint64(iov[8:]) != 16 {
		t.Fatalf("iovec 布局不符: % x", iov[:])
	}
}

func TestReadvWritevScatterGather(t *testing.T) {
	ctx := context.Background()
	memory := newFakeMemory()
	descriptors := &fakeDescriptors{input: []byte("hello world!")}
	handler := Syscalls{Memory: memory, Descriptors: descriptors}

	// readv:两个 iovec,第二个超出剩余数据 → 短读结束。
	raw := make([]byte, 2*16)
	binary.LittleEndian.PutUint64(raw[0:], 0x5000)
	binary.LittleEndian.PutUint64(raw[8:], 8)
	binary.LittleEndian.PutUint64(raw[16:], 0x6000)
	binary.LittleEndian.PutUint64(raw[24:], 4)
	if err := memory.Write(ctx, 0x7000, raw); err != nil {
		t.Fatal(err)
	}
	reply := handler.Handle(ctx, Call{Syscall: SysReadv, Args: [6]uint64{3, 0x7000, 2}})
	if reply.Errno != "" || reply.Value != 12 {
		t.Fatalf("readv 应返回累计字节数 12(数据耗尽短读): %+v", reply)
	}
	_ = reply
	var got [8]byte
	if err := memory.Read(ctx, 0x5000, got[:]); err != nil {
		t.Fatal(err)
	}
	if string(got[:]) != "hello wo" {
		t.Fatalf("第一段应写入 hello wo: %q", got)
	}

	// writev:两段拼接写出(第二段 4 字节)。
	if err := memory.Write(ctx, 0x5000, []byte("hello wo")); err != nil {
		t.Fatal(err)
	}
	if err := memory.Write(ctx, 0x6000, []byte("rld!")); err != nil {
		t.Fatal(err)
	}
	reply = handler.Handle(ctx, Call{Syscall: SysWritev, Args: [6]uint64{3, 0x7000, 2}})
	if reply.Errno != "" || reply.Value != 12 {
		t.Fatalf("writev 应返回累计 12: %+v", reply)
	}
	if string(descriptors.written) != "hello world!" {
		t.Fatalf("写出内容: %q", descriptors.written)
	}

	if reply := handler.Handle(ctx, Call{Syscall: SysReadv, Args: [6]uint64{3, 0x7000, iovMax + 1}}); reply.Errno != errno.Invalid {
		t.Fatalf("超 IOV_MAX 应报 EINVAL: %v", reply.Errno)
	}
	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysReadv}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配应报 ENOSYS: %v", reply.Errno)
	}
}

// statHandle 是暴露 Stat 的描述符句柄替身,模拟文件/目录句柄。
type statHandle struct {
	info      stdfs.FileInfo
	statErr   error
	truncate  []int64
	truncErr  error
	syncCalls []bool
	syncErr   error
	times     []setTimeCall
	timesErr  error
	space     vmfs.Space
	spaceErr  error
}

func (h *statHandle) Stat() (stdfs.FileInfo, error) { return h.info, h.statErr }

func (h *statHandle) Truncate(size int64) error {
	if h.truncErr != nil {
		return h.truncErr
	}
	h.truncate = append(h.truncate, size)
	return nil
}

func (h *statHandle) Sync(fdatasync bool) error {
	if h.syncErr != nil {
		return h.syncErr
	}
	h.syncCalls = append(h.syncCalls, fdatasync)
	return nil
}

func (h *statHandle) SetTimes(atime, mtime int64) error {
	if h.timesErr != nil {
		return h.timesErr
	}
	h.times = append(h.times, setTimeCall{atime: atime, mtime: mtime})
	return nil
}

func (h *statHandle) Space(context.Context) (vmfs.Space, error) {
	if h.spaceErr != nil {
		return vmfs.Space{}, h.spaceErr
	}
	return h.space, nil
}

// bareHandle 无任何可选能力,模拟管道/eventfd 等描述符。
type bareHandle struct{}

func (bareHandle) Stat() (stdfs.FileInfo, error) { return nil, errno.NotSupported }

// statFileInfo 是携带扩展元数据的 FileInfo 替身。
type statFileInfo struct {
	name    string
	mode    stdfs.FileMode
	size    int64
	modTime time.Time
	sys     any
}

func (i statFileInfo) Name() string         { return i.name }
func (i statFileInfo) Size() int64          { return i.size }
func (i statFileInfo) Mode() stdfs.FileMode { return i.mode }
func (i statFileInfo) ModTime() time.Time   { return i.modTime }
func (i statFileInfo) IsDir() bool          { return i.mode&stdfs.ModeDir != 0 }
func (i statFileInfo) Sys() any             { return i.sys }

type decodedStat struct {
	dev, ino, rdev        uint64
	mode, nlink, uid, gid uint32
	size, blksize, blocks int64
	atime, mtime, ctime   int64
	atimeNsec, mtimeNsec  int64
	ctimeNsec             int64
}

func decodeStat(memory *fakeMemory, address uint64) decodedStat {
	var out decodedStat
	word := func(offset uint64) uint64 {
		var raw [8]byte
		for index := range raw {
			raw[index] = memory.data[address+offset+uint64(index)]
		}
		return binary.LittleEndian.Uint64(raw[:])
	}
	half := func(offset uint64) uint32 { return uint32(word(offset)) }
	out.dev = word(statDev)
	out.ino = word(statIno)
	out.mode = half(statMode)
	out.nlink = half(statNLink)
	out.uid = half(statUID)
	out.gid = half(statGID)
	out.rdev = word(statRDev)
	out.size = int64(word(statSize))
	out.blksize = int64(half(statBlksize))
	out.blocks = int64(word(statBlocks))
	out.atime = int64(word(statAtime))
	out.atimeNsec = int64(word(statAtimeNsec))
	out.mtime = int64(word(statMtime))
	out.mtimeNsec = int64(word(statMtimeNsec))
	out.ctime = int64(word(statCtime))
	out.ctimeNsec = int64(word(statCtimeNsec))
	return out
}

func TestFstatEncodesDescriptorMetadata(t *testing.T) {
	space := newFakeMemory()
	metadata := &vmfs.Metadata{
		Inode: 4242, NLink: 2, UID: 1000, GID: 100,
		AtimeNanos: 1700000000*int64(time.Second) + 123,
		MtimeNanos: 1600000000*int64(time.Second) + 456,
		CtimeNanos: 1600000000*int64(time.Second) + 789,
	}
	info := statFileInfo{
		name: "data.bin", mode: 0o644, size: 1536,
		sys: metadata,
	}
	descriptors := &fakeDescriptors{get: &statHandle{info: info}}
	syscalls := Syscalls{Memory: space, Descriptors: descriptors}
	reply := syscalls.Handle(context.Background(), call(SysFstat, 5, 0x3000, 0))
	if reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("reply=%+v", reply)
	}
	stat := decodeStat(space, 0x3000)
	if stat.ino != 4242 || stat.nlink != 2 || stat.uid != 1000 || stat.gid != 100 {
		t.Fatalf("身份字段=%+v", stat)
	}
	if stat.mode != 0o100644 {
		t.Fatalf("mode=%o want 0100644", stat.mode)
	}
	if stat.size != 1536 || stat.blocks != 3 || stat.blksize != 4096 {
		t.Fatalf("尺寸字段=%+v", stat)
	}
	if stat.dev != 0 || stat.rdev != 0 {
		t.Fatalf("设备号字段=%+v", stat)
	}
	if stat.atime != 1700000000 || stat.atimeNsec != 123 {
		t.Fatalf("atime=%d.%d", stat.atime, stat.atimeNsec)
	}
	if stat.mtime != 1600000000 || stat.mtimeNsec != 456 {
		t.Fatalf("mtime=%d.%d", stat.mtime, stat.mtimeNsec)
	}
	if stat.ctime != 1600000000 || stat.ctimeNsec != 789 {
		t.Fatalf("ctime=%d.%d", stat.ctime, stat.ctimeNsec)
	}
}

func TestFstatRejectsBadHandlesAndFaults(t *testing.T) {
	ctx := context.Background()
	// 无 Stat 能力的句柄明确 ENOTSUP。
	syscalls := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{get: 42}}
	if reply := syscalls.Handle(ctx, call(SysFstat, 3, 0x3000, 0)); reply.Errno != errno.NotSupported {
		t.Fatalf("非文件句柄: reply=%+v want ENOTSUP", reply)
	}
	// 坏描述符报 EFAULT 之前的原始 errno(EBADF)。
	badFD := Syscalls{Memory: newFakeMemory(), Descriptors: &fakeDescriptors{seekErr: errno.BadHandle}}
	if reply := badFD.Handle(ctx, call(SysFstat, 99, 0x3000, 0)); reply.Errno != errno.BadHandle {
		t.Fatalf("坏描述符: reply=%+v want EBADF", reply)
	}
	// 句柄 Stat 失败透传错误。
	statter := &fakeDescriptors{get: &statHandle{statErr: errno.Permission}}
	perm := Syscalls{Memory: newFakeMemory(), Descriptors: statter}
	if reply := perm.Handle(ctx, call(SysFstat, 3, 0x3000, 0)); reply.Errno != errno.Permission {
		t.Fatalf("句柄故障: reply=%+v want EACCES", reply)
	}
	// 客体缓冲区非法报 EFAULT。
	broken := newFakeMemory()
	broken.writeErr = errno.Range
	faulted := Syscalls{Memory: broken, Descriptors: &fakeDescriptors{get: &statHandle{info: statFileInfo{name: "f"}}}}
	if reply := faulted.Handle(ctx, call(SysFstat, 3, 0x3000, 0)); reply.Errno != errno.Fault {
		t.Fatalf("缓冲区非法: reply=%+v want EFAULT", reply)
	}
	// 未装配端口报 ENOSYS。
	if reply := (Syscalls{}).Handle(ctx, call(SysFstat, 3, 0x3000, 0)); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配: reply=%+v want ENOSYS", reply)
	}
}

func TestNewfstatatFollowsAndNoFollow(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	files := &fakeFiles{
		statInfo:  statFileInfo{name: "target", mode: 0o600, size: 7, modTime: time.Unix(1600000000, 0)},
		lstatInfo: statFileInfo{name: "link", mode: stdfs.ModeSymlink | 0o777, size: 6, modTime: time.Unix(1700000000, 0)},
	}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: files}
	atFDCWD := atCurrentWorkingDirectory

	// 缺省跟随符号链接,返回目标状态。
	writeString(space, 0x1000, "link")
	if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{atFDCWD, 0x1000, 0x3000}}); reply.Errno != "" {
		t.Fatalf("跟随: reply=%+v", reply)
	}
	stat := decodeStat(space, 0x3000)
	if stat.mode != 0o100600 || stat.size != 7 || stat.mtime != 1600000000 {
		t.Fatalf("目标状态=%+v", stat)
	}
	if len(files.statPaths) != 1 || files.statPaths[0] != "link" {
		t.Fatalf("路径解析=%v", files.statPaths)
	}

	// AT_SYMLINK_NOFOLLOW 返回链接本身。
	stat = func() decodedStat {
		if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{atFDCWD, 0x1000, 0x3000, atSymlinkNoFollow}}); reply.Errno != "" {
			t.Fatalf("lstat: reply=%+v", reply)
		}
		return decodeStat(space, 0x3000)
	}()
	if stat.mode != 0o120777 || stat.size != 6 || stat.mtime != 1700000000 {
		t.Fatalf("链接状态=%+v", stat)
	}
}

func TestNewfstatatValidatesFlagsAndDirectoryHandles(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	files := &fakeFiles{statInfo: statFileInfo{name: "x", mode: 0o644}}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: files}
	writeString(space, 0x1000, "x")
	atFDCWD := atCurrentWorkingDirectory

	// 未知标志位 EINVAL。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{atFDCWD, 0x1000, 0x3000, 0x4000}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知标志: reply=%+v want EINVAL", reply)
	}
	// 非空路径 + 非 AT_FDCWD 明确 ENOTSUP。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{4, 0x1000, 0x3000}}); reply.Errno != errno.NotSupported {
		t.Fatalf("目录句柄: reply=%+v want ENOTSUP", reply)
	}
	// 路径不存在透传 ENOENT。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{atFDCWD, 0x1800, 0x3000}}); reply.Errno != errno.NotFound {
		t.Fatalf("缺失路径: reply=%+v want ENOENT", reply)
	}
	// 客体缓冲区非法 EFAULT。
	broken := newFakeMemory()
	broken.writeErr = errno.Range
	writeString(broken, 0x1000, "x")
	faulted := Syscalls{Memory: broken, Descriptors: &fakeDescriptors{}, Files: files}
	if reply := faulted.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{atFDCWD, 0x1000, 0x3000}}); reply.Errno != errno.Fault {
		t.Fatalf("缓冲区非法: reply=%+v want EFAULT", reply)
	}
	// 未装配端口报 ENOSYS。
	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysNewfstatat}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配: reply=%+v want ENOSYS", reply)
	}
}

func TestNewfstatatEmptyPathOperatesOnDescriptor(t *testing.T) {
	ctx := context.Background()
	atFDCWD := atCurrentWorkingDirectory
	space := newFakeMemory()
	writeString(space, 0x1000, "")

	metadata := &vmfs.Metadata{Inode: 7, NLink: 1}
	info := statFileInfo{name: "dir", mode: stdfs.ModeDir | 0o755, sys: metadata}
	descriptors := &fakeDescriptors{get: &statHandle{info: info}}
	syscalls := Syscalls{Memory: space, Descriptors: descriptors, Files: &fakeFiles{}}

	// AT_EMPTY_PATH + 真实描述符等价 fstat。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{4, 0x1000, 0x3000, atEmptyPath}}); reply.Errno != "" {
		t.Fatalf("AT_EMPTY_PATH: reply=%+v", reply)
	}
	stat := decodeStat(space, 0x3000)
	if stat.ino != 7 || stat.mode != 0o040755 {
		t.Fatalf("描述符状态=%+v", stat)
	}
	// AT_FDCWD 与空路径即使带 AT_EMPTY_PATH 也报 ENOENT。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{atFDCWD, 0x1000, 0x3000, atEmptyPath}}); reply.Errno != errno.NotFound {
		t.Fatalf("AT_FDCWD 空路径: reply=%+v want ENOENT", reply)
	}
	// 空路径且未带 AT_EMPTY_PATH 报 ENOENT。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysNewfstatat, Args: [6]uint64{4, 0x1000, 0x3000}}); reply.Errno != errno.NotFound {
		t.Fatalf("空路径: reply=%+v want ENOENT", reply)
	}
}

func decodeStatx(memory *fakeMemory, address uint64) map[string]uint64 {
	word := func(offset uint64) uint64 {
		var raw [8]byte
		for index := range raw {
			raw[index] = memory.data[address+offset+uint64(index)]
		}
		return binary.LittleEndian.Uint64(raw[:])
	}
	halfWord := func(offset uint64) uint64 {
		var raw [4]byte
		for index := range raw {
			raw[index] = memory.data[address+offset+uint64(index)]
		}
		return uint64(binary.LittleEndian.Uint32(raw[:]))
	}
	return map[string]uint64{
		"mask":      halfWord(0x00),
		"blksize":   halfWord(0x04),
		"nlink":     halfWord(0x10),
		"uid":       halfWord(0x14),
		"gid":       halfWord(0x18),
		"mode":      halfWord(0x1c),
		"ino":       word(0x20),
		"size":      word(0x28),
		"blocks":    word(0x30),
		"attrMask":  word(0x38),
		"atimeSec":  word(0x40),
		"btimeSec":  word(0x50),
		"ctimeSec":  word(0x60),
		"mtimeSec":  word(0x70),
		"mtimeNsec": word(0x78),
	}
}

func TestStatxEncodesBasicStatsWithMask(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	metadata := &vmfs.Metadata{
		Inode: 77, NLink: 3, UID: 1, GID: 2,
		MtimeNanos: 1600000000*int64(time.Second) + 42,
	}
	info := statFileInfo{name: "f", mode: stdfs.ModeDir | 0o755, size: 12345, sys: metadata}
	files := &fakeFiles{statInfo: info}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: files}
	writeString(space, 0x1000, "f")

	// statx(dfd, path, flags, mask, buffer)。
	const basicStats = 0x7ff
	if reply := syscalls.Handle(ctx, Call{Syscall: SysStatx, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0, basicStats, 0x3000}}); reply.Errno != "" {
		t.Fatalf("reply=%+v", reply)
	}
	fields := decodeStatx(space, 0x3000)
	if fields["mask"] != basicStats {
		t.Fatalf("mask=%#x want %#x", fields["mask"], basicStats)
	}
	if fields["mode"] != 0o040755 || fields["ino"] != 77 || fields["nlink"] != 3 ||
		fields["uid"] != 1 || fields["gid"] != 2 || fields["size"] != 12345 {
		t.Fatalf("字段=%+v", fields)
	}
	if fields["mtimeSec"] != 1600000000 || fields["mtimeNsec"] != 42 {
		t.Fatalf("mtime=%+v", fields)
	}
	// btime 未支持,保持零。
	if fields["btimeSec"] != 0 {
		t.Fatalf("btime=%+v", fields)
	}
	// 请求掩码之外的位不进入返回掩码。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysStatx, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0, 0x7ff | 0x8000, 0x3000}}); reply.Errno != "" {
		t.Fatalf("带未知位: reply=%+v", reply)
	}
	if got := decodeStatx(space, 0x3000)["mask"]; got != basicStats {
		t.Fatalf("未知位不应扩展掩码: %#x", got)
	}
	// 未知标志 EINVAL。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysStatx, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0x20000, basicStats, 0x3000}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知标志: reply=%+v want EINVAL", reply)
	}
	// 缓冲区非法 EFAULT。
	broken := newFakeMemory()
	broken.writeErr = errno.Range
	writeString(broken, 0x1000, "f")
	faulted := Syscalls{Memory: broken, Descriptors: &fakeDescriptors{}, Files: files}
	if reply := faulted.Handle(ctx, Call{Syscall: SysStatx, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0, basicStats, 0x3000}}); reply.Errno != errno.Fault {
		t.Fatalf("缓冲区非法: reply=%+v want EFAULT", reply)
	}
}

func TestFtruncateUsesHandleCapability(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	handle := &statHandle{}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{get: handle}}

	ftruncate := func(size uint64) Call {
		return Call{Syscall: SysFtruncate, Args: [6]uint64{3, size}}
	}
	if reply := syscalls.Handle(ctx, ftruncate(128)); reply.Errno != "" {
		t.Fatalf("reply=%+v", reply)
	}
	if len(handle.truncate) != 1 || handle.truncate[0] != 128 {
		t.Fatalf("截断调用=%v", handle.truncate)
	}
	// 负长度 EINVAL。
	if reply := syscalls.Handle(ctx, ftruncate(uint64(1)<<63)); reply.Errno != errno.Invalid {
		t.Fatalf("负长度: %+v want EINVAL", reply)
	}
	// 无截断能力的句柄明确 ENOTSUP。
	bare := Syscalls{Memory: space, Descriptors: &fakeDescriptors{get: bareHandle{}}}
	if reply := bare.Handle(ctx, ftruncate(8)); reply.Errno != errno.NotSupported {
		t.Fatalf("无能力: %+v want ENOTSUP", reply)
	}
	// 坏描述符透传 EBADF。
	badFD := Syscalls{Memory: space, Descriptors: &fakeDescriptors{seekErr: errno.BadHandle}}
	if reply := badFD.Handle(ctx, ftruncate(8)); reply.Errno != errno.BadHandle {
		t.Fatalf("坏描述符: %+v want EBADF", reply)
	}
}

func TestFsyncRequiresSyncableHandle(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	handle := &statHandle{}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{get: handle}}

	if reply := syscalls.Handle(ctx, call(SysFsync, 3, 0, 0)); reply.Errno != "" {
		t.Fatalf("fsync: %+v", reply)
	}
	if reply := syscalls.Handle(ctx, call(SysFdatasync, 3, 0, 0)); reply.Errno != "" {
		t.Fatalf("fdatasync: %+v", reply)
	}
	if len(handle.syncCalls) != 2 || handle.syncCalls[0] || handle.syncCalls[1] != true {
		t.Fatalf("同步调用=%v", handle.syncCalls)
	}
	// 管道/eventfd 等不支持同步:EINVAL。
	bare := Syscalls{Memory: space, Descriptors: &fakeDescriptors{get: bareHandle{}}}
	if reply := bare.Handle(ctx, call(SysFsync, 3, 0, 0)); reply.Errno != errno.Invalid {
		t.Fatalf("不可同步: %+v want EINVAL", reply)
	}
	// 已关闭句柄 EBADF。
	closed := &statHandle{syncErr: errno.BadHandle}
	if reply := (Syscalls{Memory: space, Descriptors: &fakeDescriptors{get: closed}}).Handle(ctx, call(SysFsync, 3, 0, 0)); reply.Errno != errno.BadHandle {
		t.Fatalf("已关闭: %+v want EBADF", reply)
	}
}

func TestUtimensatResolvesNowOmitAndValidates(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	files := &fakeFiles{
		statInfo: statFileInfo{name: "f", sys: &vmfs.Metadata{
			AtimeNanos: 1000, MtimeNanos: 2000,
		}},
	}
	clock := &fakeClock{wall: 9000}
	descriptors := &fakeDescriptors{get: &statHandle{}}
	syscalls := Syscalls{Memory: space, Descriptors: descriptors, Files: files, Clock: clock}
	atFDCWD := atCurrentWorkingDirectory
	writeString(space, 0x1000, "f")

	// 显式时间:两个 timespec 按纳秒写入路径目标。
	var spec [32]byte
	binary.LittleEndian.PutUint64(spec[0:], 11)
	binary.LittleEndian.PutUint64(spec[8:], 22)
	binary.LittleEndian.PutUint64(spec[16:], 33)
	binary.LittleEndian.PutUint64(spec[24:], 44)
	_ = space.Write(ctx, 0x2000, spec[:])
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atFDCWD, 0x1000, 0x2000}}); reply.Errno != "" {
		t.Fatalf("显式时间: %+v", reply)
	}
	if len(files.setTimeCalls) != 1 {
		t.Fatalf("调用=%v", files.setTimeCalls)
	}
	got := files.setTimeCalls[0]
	if got.path != "f" || got.atime != 11*nanosPerSecond+22 || got.mtime != 33*nanosPerSecond+44 || !got.follow {
		t.Fatalf("时间参数=%+v", got)
	}

	// UTIME_NOW 取客体时钟,UTIME_OMIT 保留当前值。
	var nowSpec [32]byte
	binary.LittleEndian.PutUint64(nowSpec[8:], utimeNow)
	binary.LittleEndian.PutUint64(nowSpec[24:], utimeOmit)
	_ = space.Write(ctx, 0x2000, nowSpec[:])
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atFDCWD, 0x1000, 0x2000}}); reply.Errno != "" {
		t.Fatalf("NOW/OMIT: %+v", reply)
	}
	got = files.setTimeCalls[1]
	if got.atime != 9000 || got.mtime != 2000 {
		t.Fatalf("NOW/OMIT 解析=%+v", got)
	}

	// 指针为 NULL:双 NOW。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atFDCWD, 0x1000, 0}}); reply.Errno != "" {
		t.Fatalf("NULL 时间: %+v", reply)
	}
	got = files.setTimeCalls[2]
	if got.atime != 9000 || got.mtime != 9000 {
		t.Fatalf("双 NOW=%+v", got)
	}

	// AT_SYMLINK_NOFOLLOW 透传 follow=false。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atFDCWD, 0x1000, 0, atSymlinkNoFollow}}); reply.Errno != "" {
		t.Fatalf("NOFOLLOW: %+v", reply)
	}
	if files.setTimeCalls[3].follow {
		t.Fatal("NOFOLLOW 未生效")
	}

	// 纳秒越界 EINVAL。
	var badSpec [32]byte
	binary.LittleEndian.PutUint64(badSpec[8:], nanosPerSecond)
	_ = space.Write(ctx, 0x2000, badSpec[:])
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atFDCWD, 0x1000, 0x2000}}); reply.Errno != errno.Invalid {
		t.Fatalf("纳秒越界: %+v want EINVAL", reply)
	}
	// 未知标志 EINVAL。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atFDCWD, 0x1000, 0, 0x2000}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知标志: %+v want EINVAL", reply)
	}
	// futimens 形态:路径 NULL,作用于描述符句柄。
	descriptors.get.(*statHandle).times = nil
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{4, 0, 0}}); reply.Errno != "" {
		t.Fatalf("futimens: %+v", reply)
	}
	if len(descriptors.get.(*statHandle).times) != 1 || descriptors.get.(*statHandle).times[0].atime != 9000 {
		t.Fatalf("futimens 参数=%v", descriptors.get.(*statHandle).times)
	}
	// AT_FDCWD + NULL 路径 ENOENT。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atFDCWD, 0, 0}}); reply.Errno != errno.NotFound {
		t.Fatalf("AT_FDCWD+NULL: %+v want ENOENT", reply)
	}
	// 无时钟时 NOW/OMIT 明确 ENOSYS。
	noClock := Syscalls{Memory: space, Descriptors: descriptors, Files: files}
	if reply := noClock.Handle(ctx, Call{Syscall: SysUtimensat, Args: [6]uint64{atFDCWD, 0x1000, 0}}); reply.Errno != errno.NotImplemented {
		t.Fatalf("无时钟: %+v want ENOSYS", reply)
	}
}

func TestStatfsEncodesQuotas(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	files := &fakeFiles{statfsInfo: StatfsInfo{
		Bytes:  vmfs.Quota{Declared: true, Total: 8192, Used: 2048, Free: 6144},
		Inodes: vmfs.Quota{Declared: true, Total: 100, Used: 3, Free: 97},
	}}
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: files}
	writeString(space, 0x1000, "/tmp")

	if reply := syscalls.Handle(ctx, Call{Syscall: SysStatfs, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0x3000}}); reply.Errno != "" {
		t.Fatalf("statfs: %+v", reply)
	}
	stat := decodeStatfs(space, 0x3000)
	if stat["type"] != 0x01021994 || stat["bsize"] != 4096 || stat["namelen"] != 255 {
		t.Fatalf("类型字段=%+v", stat)
	}
	if stat["blocks"] != 2 || stat["bfree"] != 1 || stat["bavail"] != 1 {
		t.Fatalf("块容量=%+v", stat)
	}
	if stat["files"] != 100 || stat["ffree"] != 97 {
		t.Fatalf("inode 容量=%+v", stat)
	}

	// fstatfs 经句柄能力。
	handle := &statHandle{space: vmfs.Space{
		Bytes:  vmfs.Quota{Declared: true, Total: 4096, Free: 0},
		Inodes: vmfs.Quota{Declared: true, Total: 10, Used: 10, Free: 0},
	}}
	fstatfs := Syscalls{Memory: space, Descriptors: &fakeDescriptors{get: handle}}
	if reply := fstatfs.Handle(ctx, Call{Syscall: SysFstatfs, Args: [6]uint64{5, 0x3000}}); reply.Errno != "" {
		t.Fatalf("fstatfs: %+v", reply)
	}
	stat = decodeStatfs(space, 0x3000)
	if stat["blocks"] != 1 || stat["bfree"] != 0 || stat["ffree"] != 0 {
		t.Fatalf("fstatfs 容量=%+v", stat)
	}

	// 未声明配额按 0 报告。
	empty := &fakeFiles{statfsInfo: StatfsInfo{}}
	undeclared := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}, Files: empty}
	if reply := undeclared.Handle(ctx, Call{Syscall: SysStatfs, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0x3000}}); reply.Errno != "" {
		t.Fatalf("未声明: %+v", reply)
	}
	stat = decodeStatfs(space, 0x3000)
	if stat["blocks"] != 0 || stat["files"] != 0 {
		t.Fatalf("未声明配额=%+v", stat)
	}

	// 非目录句柄 ENOTSUP;未装配 ENOSYS;路径错误透传。
	bare := Syscalls{Memory: space, Descriptors: &fakeDescriptors{get: bareHandle{}}}
	if reply := bare.Handle(ctx, Call{Syscall: SysFstatfs, Args: [6]uint64{5, 0x3000}}); reply.Errno != errno.NotSupported {
		t.Fatalf("不可报容量: %+v want ENOTSUP", reply)
	}
	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysStatfs}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配: %+v want ENOSYS", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysStatfs, Args: [6]uint64{4, 0x1000, 0x3000}}); reply.Errno != errno.NotSupported {
		t.Fatalf("非 AT_FDCWD: %+v want ENOTSUP", reply)
	}
	files.statfsErr = errno.Permission
	if reply := syscalls.Handle(ctx, Call{Syscall: SysStatfs, Args: [6]uint64{atCurrentWorkingDirectory, 0x1000, 0x3000}}); reply.Errno != errno.Permission {
		t.Fatalf("容量查询故障: %+v want EACCES", reply)
	}
}

func decodeStatfs(memory *fakeMemory, address uint64) map[string]uint64 {
	word := func(offset uint64) uint64 {
		var raw [8]byte
		for index := range raw {
			raw[index] = memory.data[address+offset+uint64(index)]
		}
		return binary.LittleEndian.Uint64(raw[:])
	}
	half := func(offset uint64) uint64 {
		var raw [4]byte
		for index := range raw {
			raw[index] = memory.data[address+offset+uint64(index)]
		}
		return uint64(binary.LittleEndian.Uint32(raw[:]))
	}
	return map[string]uint64{
		"type":    half(0x00),
		"bsize":   half(0x04),
		"blocks":  word(0x08),
		"bfree":   word(0x10),
		"bavail":  word(0x18),
		"files":   word(0x20),
		"ffree":   word(0x28),
		"namelen": half(0x38),
	}
}

func TestGetrandomValidatesFlags(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	syscalls := Syscalls{Memory: space, Descriptors: &fakeDescriptors{}}
	// 未知标志位 EINVAL。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysGetrandom, Args: [6]uint64{0x1000, 8, 0x8}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知标志: %+v want EINVAL", reply)
	}
	// 已知标志(GRND_NONBLOCK/GRND_RANDOM/GRND_INSECURE)组合被接受;
	// 数据来源由客体熵源配置决定,标志不切换来源。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysGetrandom, Args: [6]uint64{0x1000, 8, 0x7}}); reply.Errno != errno.NotImplemented {
		t.Fatalf("已知标志: %+v want ENOSYS(未装配熵源)", reply)
	}
}

func TestErrnoOfDistinguishesCommonErrorFamilies(t *testing.T) {
	cases := map[string]error{
		"文件缺失":      errno.NotFound,
		"权限不足":      errno.Permission,
		"已存在":       errno.Exist,
		"描述符无效":     errno.BadHandle,
		"预算耗尽":      errno.NoSpace,
		"内存耗尽":      errno.NoMemory,
		"网络不可达":     errno.NetworkUnreachable,
		"进程不存在":     errno.NoProcess,
		"跨挂载":       errno.CrossDevice,
		"非阻塞不可用":    errno.WouldBlock,
		"包装后的描述符无效": errno.Wrap(errno.BadHandle, "读取失败"),
		"嵌套包装的权限不足": errno.Wrap(errno.IO, errno.Wrap(errno.Permission, "链路").Error()),
	}
	for label, err := range cases {
		if got := errnoOf(err); !errors.Is(err, got) {
			t.Fatalf("%s: %v 映射为 %v", label, err, got)
		}
	}
	// 未知宿主错误不伪装成功,统一 EIO。
	if got := errnoOf(errors.New("宿主磁盘冒烟")); got != errno.IO {
		t.Fatalf("未知错误=%v want EIO", got)
	}
	// 每个声明码都能被 errnoOf 识别(身份保持)。
	for _, code := range errno.Codes() {
		if got := errnoOf(code); got != code {
			t.Fatalf("%s 识别为 %v", code, got)
		}
	}
}

func TestLinuxNumbersRemainDistinctPerFamily(t *testing.T) {
	// 关键错误族在 asm-generic 中数值不同,客体能区分;
	// Limit 与 WouldBlock 共享 EAGAIN 是 Linux 的既定事实。
	notImplemented, _ := errno.Linux(errno.NotImplemented)
	seen := map[int32]errno.Code{}
	for _, code := range errno.Codes() {
		number, _ := errno.Linux(code)
		if code != errno.NotImplemented && number == notImplemented {
			t.Fatalf("%s 缺少 Linux 数值", code)
		}
		if previous, ok := seen[number]; ok && previous != code {
			allowed := (previous == errno.Limit && code == errno.WouldBlock) ||
				(previous == errno.WouldBlock && code == errno.Limit)
			if !allowed {
				t.Fatalf("%s 与 %s 共享 Linux 数值 %d", previous, code, number)
			}
		}
		seen[number] = code
	}
}

func TestAffinityAndTopologyReportConfiguredCPUs(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()

	// 单 CPU(缺省):掩码 0x01,getcpu 报告 CPU 0 / 节点 0。
	syscalls := Syscalls{Memory: space, Identity: &Identity{PID: 7}}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysSchedGetAffinity, Args: [6]uint64{0, 8, 0x2000}}); reply.Errno != "" || reply.Value != 8 {
		t.Fatalf("getaffinity: %+v", reply)
	}
	if mask := space.data[0x2000]; mask != 0x01 {
		t.Fatalf("缺省掩码=%#x want 0x01", mask)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysGetCPU, Args: [6]uint64{0x2000, 0x2004}}); reply.Errno != "" {
		t.Fatalf("getcpu: %+v", reply)
	}
	if space.data[0x2000] != 0 || space.data[0x2004] != 0 {
		t.Fatalf("getcpu 拓扑=%v %v", space.data[0x2000], space.data[0x2004])
	}

	// 多 CPU 配置:掩码按位数展开。
	multi := Syscalls{Memory: space, CPUs: 4, Identity: &Identity{PID: 7}}
	if reply := multi.Handle(ctx, Call{Syscall: SysSchedGetAffinity, Args: [6]uint64{7, 8, 0x2000}}); reply.Errno != "" || reply.Value != 8 {
		t.Fatalf("多 CPU: %+v", reply)
	}
	if mask := space.data[0x2000]; mask != 0x0F {
		t.Fatalf("4 CPU 掩码=%#x want 0x0F", mask)
	}

	// setaffinity:包含 CPU 0 成功,不含 EINVAL。
	if reply := multi.Handle(ctx, Call{Syscall: SysSchedSetAffinity, Args: [6]uint64{0, 8, 0x2000}}); reply.Errno != "" {
		t.Fatalf("setaffinity: %+v", reply)
	}
	space.data[0x2000] = 0x02 // 仅 CPU 1,客体不存在
	if reply := multi.Handle(ctx, Call{Syscall: SysSchedSetAffinity, Args: [6]uint64{0, 8, 0x2000}}); reply.Errno != errno.Invalid {
		t.Fatalf("空交集: %+v want EINVAL", reply)
	}

	// 跨进程查询 ESRCH;缓冲区过小 EINVAL;坏地址 EFAULT。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysSchedGetAffinity, Args: [6]uint64{999, 8, 0x2000}}); reply.Errno != errno.NoProcess {
		t.Fatalf("跨进程: %+v want ESRCH", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysSchedGetAffinity, Args: [6]uint64{0, 4, 0x2000}}); reply.Errno != errno.Invalid {
		t.Fatalf("小缓冲: %+v want EINVAL", reply)
	}
	broken := newFakeMemory()
	broken.readErr = errno.Range
	faulted := Syscalls{Memory: broken, Identity: &Identity{PID: 7}}
	if reply := faulted.Handle(ctx, Call{Syscall: SysSchedSetAffinity, Args: [6]uint64{0, 8, 0x2000}}); reply.Errno != errno.Fault {
		t.Fatalf("坏地址: %+v want EFAULT", reply)
	}
}

type fakeWaiter struct {
	state   ChildState
	found   bool
	err     error
	targets []uint64
	nohangs []bool
	hasKids bool
}

func (w *fakeWaiter) WaitChild(_ context.Context, target uint64, nohang bool) (ChildState, bool, error) {
	w.targets = append(w.targets, target)
	w.nohangs = append(w.nohangs, nohang)
	if w.err != nil {
		return ChildState{}, false, w.err
	}
	return w.state, w.found, nil
}

func (w *fakeWaiter) HasChildren() bool { return w.hasKids }

func TestWaitidEncodesSigchldAndHonoursNoHang(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	waiter := &fakeWaiter{found: true, state: ChildState{PID: 42, ExitCode: 7}}
	syscalls := Syscalls{Memory: space, Waiter: waiter}

	if reply := syscalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{pPID, 42, 0x3000, wExited}}); reply.Errno != "" {
		t.Fatalf("waitid: %+v", reply)
	}
	if waiter.targets[0] != 42 || waiter.nohangs[0] {
		t.Fatalf("参数=%v %v", waiter.targets, waiter.nohangs)
	}
	raw := make([]byte, siginfoSize)
	if err := space.Read(ctx, 0x3000, raw); err != nil {
		t.Fatal(err)
	}
	if signo := int32(binary.LittleEndian.Uint32(raw[0:])); signo != sigchldNumber {
		t.Fatalf("si_signo=%d want %d", signo, sigchldNumber)
	}
	if code := int32(binary.LittleEndian.Uint32(raw[8:])); code != cldExited {
		t.Fatalf("si_code=%d want CLD_EXITED", code)
	}
	if pid := int32(binary.LittleEndian.Uint32(raw[siginfoPID:])); pid != 42 {
		t.Fatalf("si_pid=%d", pid)
	}
	if status := int32(binary.LittleEndian.Uint32(raw[siginfoStatus:])); status != 7 {
		t.Fatalf("si_status=%d", status)
	}

	// 被信号终止:CLD_KILLED + 信号编号。
	killed := &fakeWaiter{found: true, state: ChildState{PID: 9, ExitSignal: 15}}
	killedCalls := Syscalls{Memory: space, Waiter: killed}
	if reply := killedCalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{pPID, 9, 0x3000, wExited}}); reply.Errno != "" {
		t.Fatalf("killed: %+v", reply)
	}
	if err := space.Read(ctx, 0x3000, raw); err != nil {
		t.Fatal(err)
	}
	if code := int32(binary.LittleEndian.Uint32(raw[8:])); code != cldKilled {
		t.Fatalf("si_code=%d want CLD_KILLED", code)
	}
	if status := int32(binary.LittleEndian.Uint32(raw[siginfoStatus:])); status != 15 {
		t.Fatalf("si_status=%d", status)
	}

	// WNOHANG 无就绪子进程:清零返回 0。
	nohang := &fakeWaiter{found: false}
	nohangCalls := Syscalls{Memory: space, Waiter: nohang}
	space.data[0x3400] = 0xAB
	if reply := nohangCalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{pAll, 0, 0x3000, wExited | wNoHang}}); reply.Errno != "" {
		t.Fatalf("WNOHANG: %+v", reply)
	}
	if err := space.Read(ctx, 0x3000, raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, make([]byte, siginfoSize)) {
		t.Fatal("WNOHANG 未就绪应清零 siginfo")
	}
}

func TestWaitidValidatesOptionsAndTargets(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	waiter := &fakeWaiter{err: errno.NoChild}
	syscalls := Syscalls{Memory: space, Waiter: waiter}

	// 缺 WEXITED EINVAL;P_PID 且 id=0 EINVAL;infop 为 NULL EINVAL。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{pAll, 0, 0x3000, 0}}); reply.Errno != errno.Invalid {
		t.Fatalf("缺 WEXITED: %+v want EINVAL", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{pPID, 0, 0x3000, wExited}}); reply.Errno != errno.Invalid {
		t.Fatalf("P_PID id=0: %+v want EINVAL", reply)
	}
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{pAll, 0, 0, wExited}}); reply.Errno != errno.Invalid {
		t.Fatalf("infop NULL: %+v want EINVAL", reply)
	}
	// WSTOPPED/WCONTINUED/WNOWAIT 未纳入兼容配置,ENOTSUP。
	for _, flag := range []uint64{wStopped, wContinued, wNoWait} {
		if reply := syscalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{pAll, 0, 0x3000, wExited | flag}}); reply.Errno != errno.NotSupported {
			t.Fatalf("flag %#x: %+v want ENOTSUP", flag, reply)
		}
	}
	// idtype 不识别 ENOTSUP。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{3, 0, 0x3000, wExited}}); reply.Errno != errno.NotSupported {
		t.Fatalf("P_PIDFD: %+v want ENOTSUP", reply)
	}
	// 无子进程透传 ECHILD。
	if reply := syscalls.Handle(ctx, Call{Syscall: SysWaitid, Args: [6]uint64{pAll, 0, 0x3000, wExited}}); reply.Errno != errno.NoChild {
		t.Fatalf("ECHILD: %+v want ECHILD", reply)
	}
	// 未装配端口 ENOSYS。
	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysWaitid}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未装配: %+v want ENOSYS", reply)
	}
}
