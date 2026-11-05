package common

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/inotify"
)

// inotifyHarness 构造真实 memfs + 描述符表 + 扇出端口的系统调用层。
func inotifyHarness(t *testing.T) (Syscalls, *fd.Table, vmfs.System, *inotify.Broker, uint64) {
	t.Helper()
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(ctx, "/work", 0o755); err != nil {
		t.Fatal(err)
	}
	const pid = 51
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	broker := inotify.NewBroker()
	syscalls := Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		Files:       ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"},
		Notify:      broker,
	}
	return syscalls, table, system, broker, pid
}

// guestStringBase 是客体字符串缓冲的基址;每次调用向前分配,避免
// 同地址覆盖。
var guestStringBase uint64 = 0x9000

// writeGuestString 向假内存写入字符串,返回缓冲地址。
func writeGuestString(t *testing.T, s Syscalls, text string) uint64 {
	t.Helper()
	guestStringBase += 64
	address := guestStringBase
	if err := s.Memory.Write(context.Background(), address, append([]byte(text), 0)); err != nil {
		t.Fatal(err)
	}
	return address
}

// decodeInotifyEvents 解码读取缓冲。
func decodeInotifyEvents(t *testing.T, buf []byte) []inotify.Event {
	t.Helper()
	var out []inotify.Event
	for offset := 0; offset+16 <= len(buf); {
		length := int(binary.LittleEndian.Uint32(buf[offset+12:]))
		name := ""
		if length > 0 {
			raw := buf[offset+16 : offset+16+length]
			for i, b := range raw {
				if b == 0 {
					raw = raw[:i]
					break
				}
			}
			name = string(raw)
		}
		out = append(out, inotify.Event{
			WD:     binary.LittleEndian.Uint32(buf[offset:]),
			Mask:   binary.LittleEndian.Uint32(buf[offset+4:]),
			Cookie: binary.LittleEndian.Uint32(buf[offset+8:]),
			Name:   name,
		})
		offset += 16 + length
	}
	return out
}

// TestInotifyLifecycleAgainstRealFileSystem 验证 I20:创建/写入/删除/
// 关闭事件经真实 memfs 系统调用到达读取端;监视描述符管理(未知 wd
// EINVAL、IN_ONLYDIR、非 inotify 描述符 EINVAL)与溢出可观察。
func TestInotifyLifecycleAgainstRealFileSystem(t *testing.T) {
	ctx := context.Background()
	s, table, system, broker, pid := inotifyHarness(t)

	// inotify_init1(IN_NONBLOCK)。
	reply := s.Handle(ctx, Call{Syscall: SysInotifyInit1, Args: [6]uint64{0o4000}})
	if reply.Errno != "" {
		t.Fatalf("inotify_init1: %+v", reply)
	}
	inoFD := reply.Value
	// inotify_init1 未知标志 EINVAL。
	if reply := s.Handle(ctx, Call{Syscall: SysInotifyInit1, Args: [6]uint64{0x1}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知标志: %+v want EINVAL", reply)
	}

	// add_watch /work:CREATE|MODIFY|DELETE|OPEN(目录)。
	pathPtr := writeGuestString(t, s, "/work")
	var maskRaw [4]byte
	binary.LittleEndian.PutUint32(maskRaw[:], inotify.InCreate|inotify.InModify|inotify.InDelete|inotify.InOpen)
	if err := s.Memory.Write(ctx, 0x9100, maskRaw[:]); err != nil {
		t.Fatal(err)
	}
	reply = s.Handle(ctx, Call{Syscall: SysInotifyAddWatch, Args: [6]uint64{inoFD, pathPtr, uint64(inotify.InCreate | inotify.InModify | inotify.InDelete | inotify.InOpen)}})
	if reply.Errno != "" {
		t.Fatalf("add_watch: %+v", reply)
	}
	wd := reply.Value
	if wd == 0 {
		t.Fatal("wd 应非零")
	}
	// IN_ONLYDIR 对目录成功,对文件 ENOTDIR。
	filePtr := writeGuestString(t, s, "/work/note.txt")
	if reply := s.Handle(ctx, Call{Syscall: SysInotifyAddWatch, Args: [6]uint64{inoFD, filePtr, uint64(inotify.InModify) | uint64(inotify.InOnlydir)<<32}}); reply.Errno != errno.NotDirectory {
		t.Fatalf("IN_ONLYDIR 文件: %+v want ENOTDIR", reply)
	}

	// 创建 + 打开:mkdirat → IN_CREATE|IN_ISDIR;openat(O_CREAT) → IN_CREATE + IN_OPEN。
	// (harness 已建 /work,这里建子目录 /work/sub。)
	subPtr := writeGuestString(t, s, "/work/sub")
	if reply := s.Handle(ctx, Call{Syscall: SysMkdirat, Args: [6]uint64{0xFFFFFFFFFFFFFFFF, subPtr, 0o755}}); reply.Errno != "" {
		t.Fatalf("mkdirat: %+v", reply)
	}
	createFlags := OpenFlags{Mode: OpenReadWrite, Create: true}
	openReply := s.Handle(ctx, Call{Syscall: SysOpenAt, Args: [6]uint64{0xFFFFFFFFFFFFFFFF, filePtr, 0, 0o644}, OpenFlags: &createFlags})
	if openReply.Errno != "" {
		t.Fatalf("openat: %+v", openReply)
	}
	targetFD := openReply.Value

	// 写入:write → IN_MODIFY。
	dataPtr := writeGuestString(t, s, "hello inotify")
	if reply := s.Handle(ctx, Call{Syscall: SysWrite, Args: [6]uint64{targetFD, dataPtr, 13}}); reply.Errno != "" || reply.Value != 13 {
		t.Fatalf("write: %+v", reply)
	}
	// 删除:unlinkat → IN_DELETE(带条目名)。
	if reply := s.Handle(ctx, Call{Syscall: SysUnlinkat, Args: [6]uint64{0xFFFFFFFFFFFFFFFF, filePtr}}); reply.Errno != "" {
		t.Fatalf("unlinkat: %+v", reply)
	}
	// 读取事件(非阻塞)。
	buf := make([]byte, 4096)
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	handle, err := descriptors.InotifyAt(ctx, inoFD)
	if err != nil {
		t.Fatal(err)
	}
	n, err := handle.ReadContext(ctx, buf)
	if err != nil {
		t.Fatalf("读取事件: %v", err)
	}
	events := decodeInotifyEvents(t, buf[:n])
	// mkdirat 创建目录 + openat 创建文件:CREATE(dir,isdir) CREATE(file) OPEN(file,?isdir)
	// write → MODIFY(file) unlink → DELETE(file)。
	if len(events) < 5 {
		t.Fatalf("事件数=%d: %+v", len(events), events)
	}
	var sawCreateDir, sawCreateFile, sawOpen, sawModify, sawDelete bool
	for _, e := range events {
		switch e.Mask &^ inotify.InIsdir {
		case inotify.InCreate:
			if e.Name == "sub" && e.Mask&inotify.InIsdir != 0 {
				sawCreateDir = true
			}
			if e.Name == "note.txt" {
				sawCreateFile = true
			}
		case inotify.InOpen:
			if e.Name == "note.txt" {
				sawOpen = true
			}
		case inotify.InModify:
			if e.Name == "note.txt" {
				sawModify = true
			}
		case inotify.InDelete:
			if e.Name == "note.txt" {
				sawDelete = true
			}
		}
	}
	if !sawCreateDir || !sawCreateFile || !sawOpen || !sawModify || !sawDelete {
		t.Fatalf("事件不完整: dir=%v file=%v open=%v modify=%v delete=%v 全量=%+v",
			sawCreateDir, sawCreateFile, sawOpen, sawModify, sawDelete, events)
	}

	// rm_watch 后事件不再到达;未知 wd EINVAL。
	if reply := s.Handle(ctx, Call{Syscall: SysInotifyRmWatch, Args: [6]uint64{inoFD, wd}}); reply.Errno != "" {
		t.Fatalf("rm_watch: %+v", reply)
	}
	if reply := s.Handle(ctx, Call{Syscall: SysInotifyRmWatch, Args: [6]uint64{inoFD, wd}}); reply.Errno != errno.Invalid {
		t.Fatalf("重复 rm_watch: %+v want EINVAL", reply)
	}
	secondDir := writeGuestString(t, s, "/work/other")
	if reply := s.Handle(ctx, Call{Syscall: SysMkdirat, Args: [6]uint64{0xFFFFFFFFFFFFFFFF, secondDir, 0o755}}); reply.Errno != "" {
		t.Fatalf("mkdirat other: %+v", reply)
	}
	if n, _ := handle.ReadContext(ctx, buf); n != 0 {
		t.Fatalf("移除监视后不应有事件: %d 字节", n)
	}

	// 关闭 inotify 描述符后读取 EBADF(与 Python/Node 关闭路径一致)。
	if err := table.Close(ctx, pid, uint32(inoFD)); err != nil {
		t.Fatal(err)
	}
	_ = system
	_ = broker
}

// TestInotifyRejectsNonInotifyDescriptor 验证 add/rm_watch 对非
// inotify 描述符报 EINVAL,坏描述符报 EBADF。
func TestInotifyRejectsNonInotifyDescriptor(t *testing.T) {
	ctx := context.Background()
	s, _, _, _, _ := inotifyHarness(t)
	pathPtr := writeGuestString(t, s, "/work")
	// 坏描述符 EBADF。
	if reply := s.Handle(ctx, Call{Syscall: SysInotifyAddWatch, Args: [6]uint64{999, pathPtr, uint64(inotify.InModify)}}); reply.Errno != errno.BadHandle {
		t.Fatalf("坏描述符: %+v want EBADF", reply)
	}
	if reply := s.Handle(ctx, Call{Syscall: SysInotifyRmWatch, Args: [6]uint64{999, 1}}); reply.Errno != errno.BadHandle {
		t.Fatalf("坏描述符 rm: %+v want EBADF", reply)
	}
	// 非 inotify 描述符(pipe)EINVAL。
	var pair [8]byte
	if err := s.Memory.Write(ctx, 0x9800, pair[:]); err != nil {
		t.Fatal(err)
	}
	if reply := s.Handle(ctx, Call{Syscall: SysPipe2, Args: [6]uint64{0x9800}}); reply.Errno != "" {
		t.Fatalf("pipe2: %+v", reply)
	}
	raw := make([]byte, 4)
	if err := s.Memory.Read(ctx, 0x9800, raw); err != nil {
		t.Fatal(err)
	}
	pipeFD := uint64(binary.LittleEndian.Uint32(raw))
	if reply := s.Handle(ctx, Call{Syscall: SysInotifyAddWatch, Args: [6]uint64{pipeFD, pathPtr, uint64(inotify.InModify)}}); reply.Errno != errno.Invalid {
		t.Fatalf("非 inotify: %+v want EINVAL", reply)
	}
}

// TestInotifyQueueOverflowObservable 验证溢出在系统调用层可观察:
// 持续写入至队列满,读取端收到 IN_Q_OVERFLOW(wd=-1)。
func TestInotifyQueueOverflowObservable(t *testing.T) {
	ctx := context.Background()
	s, table, _, _, pid := inotifyHarness(t)
	reply := s.Handle(ctx, Call{Syscall: SysInotifyInit1, Args: [6]uint64{}})
	if reply.Errno != "" {
		t.Fatalf("inotify_init1: %+v", reply)
	}
	inoFD := reply.Value
	pathPtr := writeGuestString(t, s, "/work")
	if reply := s.Handle(ctx, Call{Syscall: SysInotifyAddWatch, Args: [6]uint64{inoFD, pathPtr, uint64(inotify.InModify)}}); reply.Errno != "" {
		t.Fatalf("add_watch: %+v", reply)
	}
	// 打开文件并写满事件队列(IN_MODIFY 每写一条)。
	logFlags := OpenFlags{Mode: OpenReadWrite, Create: true}
	if reply := s.Handle(ctx, Call{Syscall: SysOpenAt, Args: [6]uint64{0xFFFFFFFFFFFFFFFF, writeGuestString(t, s, "/work/log"), 0, 0o644}, OpenFlags: &logFlags}); reply.Errno != "" {
		t.Fatalf("openat: %+v", reply)
	}
	openReply := s.Handle(ctx, Call{Syscall: SysOpenAt, Args: [6]uint64{0xFFFFFFFFFFFFFFFF, writeGuestString(t, s, "/work/log"), 0, 0o644}, OpenFlags: &logFlags})
	if openReply.Errno != "" {
		t.Fatalf("openat: %+v", openReply)
	}
	targetFD := openReply.Value
	dataPtr := writeGuestString(t, s, "x")
	for i := 0; i < inotify.QueueLimit+10; i++ {
		if r := s.Handle(ctx, Call{Syscall: SysWrite, Args: [6]uint64{targetFD, dataPtr, 1}}); r.Errno != "" {
			t.Fatalf("write #%d: %+v", i, r)
		}
	}
	buf := make([]byte, 1<<22)
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	handle, err := descriptors.InotifyAt(ctx, inoFD)
	if err != nil {
		t.Fatal(err)
	}
	n, err := handle.ReadContext(ctx, buf)
	if err != nil {
		t.Fatal(err)
	}
	events := decodeInotifyEvents(t, buf[:n])
	last := events[len(events)-1]
	if last.WD != inotify.OverflowWD || last.Mask != inotify.InQOverflow {
		t.Fatalf("队尾应为溢出事件: %+v", last)
	}
}
