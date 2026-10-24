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

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
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
	return nil, d.seekErr
}

func (d *fakeDescriptors) Truncate(_ context.Context, _ uint64, size int64) error {
	return d.seekErr
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
	requests []OpenRequest
	next     uint64
	err      error
}

func (f *fakeFiles) Mkdir(_ context.Context, path string, mode uint32) error  { return f.err }
func (f *fakeFiles) Remove(_ context.Context, path string) error              { return f.err }
func (f *fakeFiles) Symlink(_ context.Context, target, linkpath string) error { return f.err }
func (f *fakeFiles) Link(_ context.Context, oldpath, newpath string) error    { return f.err }
func (f *fakeFiles) Rename(_ context.Context, oldpath, newpath string) error  { return f.err }
func (f *fakeFiles) ReadDir(_ context.Context, path string) ([]stdfs.DirEntry, error) {
	return nil, f.err
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
	return nil, nil
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
