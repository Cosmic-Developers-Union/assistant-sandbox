package common

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// writeFDSet 在客体内存写一个只含给定描述符的 fd_set。
func writeFDSet(t *testing.T, handler Syscalls, address uint64, fds ...uint64) {
	t.Helper()
	raw := make([]byte, fdSetBytes)
	for _, id := range fds {
		raw[id/8] |= 1 << (id % 8)
	}
	if err := handler.Memory.Write(context.Background(), address, raw); err != nil {
		t.Fatal(err)
	}
}

// readFDSetBits 读回 fd_set 中置位的描述符。
func readFDSetBits(t *testing.T, handler Syscalls, address uint64) []uint64 {
	t.Helper()
	raw := make([]byte, fdSetBytes)
	if err := handler.Memory.Read(context.Background(), address, raw); err != nil {
		t.Fatal(err)
	}
	var out []uint64
	for bit := uint64(0); bit < fdSetLimit; bit++ {
		if raw[bit/8]&(1<<(bit%8)) != 0 {
			out = append(out, bit)
		}
	}
	return out
}

// newPselectHarness 构造带真实描述符表的处理器(普通文件句柄 3)。
func newPselectHarness(t *testing.T) Syscalls {
	t.Helper()
	table := fd.New()
	const pid = 91
	table.Attach(pid, fd.Streams{})
	if err := table.Install(pid, 3, &plainHandle{}); err != nil {
		t.Fatal(err)
	}
	return Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		Clock:       &fakeClock{monotonic: 0},
		Sleeper:     &fakeSleeper{onSleep: func(ns uint64) {}},
	}
}

// TestPselect6ReportsReadyRegularFile 验证 L06:普通文件恒就绪,结果
// 集合写回就绪位,返回计数正确。
func TestPselect6ReportsReadyRegularFile(t *testing.T) {
	ctx := context.Background()
	handler := newPselectHarness(t)
	writeFDSet(t, handler, 0x2000, 3)
	writeFDSet(t, handler, 0x2080, 3)
	reply := handler.Handle(ctx, Call{Syscall: SysPselect6, Args: [6]uint64{8, 0x2000, 0x2080, 0, 0, 0}})
	if reply.Errno != "" || reply.Value != 2 {
		t.Fatalf("pselect6: %+v want 2(读+写)", reply)
	}
	if got := readFDSetBits(t, handler, 0x2000); len(got) != 1 || got[0] != 3 {
		t.Fatalf("读集合=%v want [3]", got)
	}
	if got := readFDSetBits(t, handler, 0x2080); len(got) != 1 || got[0] != 3 {
		t.Fatalf("写集合=%v want [3]", got)
	}
}

// TestPselect6BadDescriptorReturnsEBADF 验证 L06:集合中的坏描述符
// 立即 EBADF,不进入等待(不忙循环)。
func TestPselect6BadDescriptorReturnsEBADF(t *testing.T) {
	ctx := context.Background()
	handler := newPselectHarness(t)
	writeFDSet(t, handler, 0x2000, 7)
	started := time.Now()
	if reply := handler.Handle(ctx, Call{Syscall: SysPselect6, Args: [6]uint64{8, 0x2000, 0, 0, 0, 0}}); reply.Errno != errno.BadHandle {
		t.Fatalf("坏描述符: %+v want EBADF", reply)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("坏描述符不得进入等待: %v", time.Since(started))
	}
}

// TestPselect6EmptySetsSleepUntilTimeout 验证 L06:三个集合都为空时按
// 超时纯睡眠,超时返回 0,不忙循环(睡眠端口被实际使用)。
func TestPselect6EmptySetsSleepUntilTimeout(t *testing.T) {
	ctx := context.Background()
	handler := newPselectHarness(t)
	slept := make(chan uint64, 8)
	handler.Sleeper = &fakeSleeper{onSleep: func(ns uint64) { slept <- ns }}
	var timeout [16]byte
	binary.LittleEndian.PutUint64(timeout[0:], 0)
	binary.LittleEndian.PutUint64(timeout[8:], 3_000_000) // 3ms
	if err := handler.Memory.Write(ctx, 0x3000, timeout[:]); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	reply := handler.Handle(ctx, Call{Syscall: SysPselect6, Args: [6]uint64{0, 0, 0, 0, 0x3000, 0}})
	if reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("空集合超时: %+v want 0", reply)
	}
	_ = time.Since(started)
	total := uint64(0)
	for {
		select {
		case ns := <-slept:
			total += ns
		default:
			goto done
		}
	}
done:
	if total != 3_000_000 {
		t.Fatalf("空集合睡眠总时长=%d want 3000000", total)
	}
}

// TestPselect6ValidatesArguments 验证 L06:nfds 越界、例外集、非法
// timespec 与非法 sigmask 长度按 EINVAL 拒绝。
func TestPselect6ValidatesArguments(t *testing.T) {
	ctx := context.Background()
	handler := newPselectHarness(t)
	if reply := handler.Handle(ctx, Call{Syscall: SysPselect6, Args: [6]uint64{2000, 0, 0, 0, 0, 0}}); reply.Errno != errno.Invalid {
		t.Fatalf("nfds 越界: %+v want EINVAL", reply)
	}
	writeFDSet(t, handler, 0x2100, 3)
	if reply := handler.Handle(ctx, Call{Syscall: SysPselect6, Args: [6]uint64{8, 0, 0, 0x2100, 0, 0}}); reply.Errno != errno.Invalid {
		t.Fatalf("例外集: %+v want EINVAL", reply)
	}
	var badTimeout [16]byte
	binary.LittleEndian.PutUint64(badTimeout[0:], 0)
	binary.LittleEndian.PutUint64(badTimeout[8:], 2_000_000_000)
	if err := handler.Memory.Write(ctx, 0x3080, badTimeout[:]); err != nil {
		t.Fatal(err)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysPselect6, Args: [6]uint64{0, 0, 0, 0, 0x3080, 0}}); reply.Errno != errno.Invalid {
		t.Fatalf("纳秒越界: %+v want EINVAL", reply)
	}
	var maskBlock [16]byte
	binary.LittleEndian.PutUint64(maskBlock[0:], 0x4000)
	binary.LittleEndian.PutUint64(maskBlock[8:], 4) // sigsetsize 错误
	if err := handler.Memory.Write(ctx, 0x3100, maskBlock[:]); err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint64(maskBlock[0:], 0x5000)
	binary.LittleEndian.PutUint64(maskBlock[8:], 8)
	if err := handler.Memory.Write(ctx, 0x3180, maskBlock[:]); err != nil {
		t.Fatal(err)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysPselect6, Args: [6]uint64{0, 0, 0, 0, 0, 0x3100}}); reply.Errno != errno.Invalid {
		t.Fatalf("sigmask 长度: %+v want EINVAL", reply)
	}
}

// TestPselect6WaitsForPipeReadiness 验证 L06:未就绪的管道读端等待到
// 数据到达后报告就绪(与 poll 共用等待图)。
func TestPselect6WaitsForPipeReadiness(t *testing.T) {
	ctx := context.Background()
	table := fd.New()
	const pid = 92
	table.Attach(pid, fd.Streams{})
	reader, writer, err := table.Pipe(pid)
	if err != nil {
		t.Fatal(err)
	}
	handler := Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		Clock:       &fakeClock{monotonic: 0},
		Sleeper:     &fakeSleeper{onSleep: func(ns uint64) {}},
	}
	// 只等读端:写端有空间恒可写,会在就绪检查中立即返回。
	writeFDSet(t, handler, 0x2000, uint64(reader))
	type reply struct {
		value uint64
		errno errno.Code
	}
	done := make(chan reply, 1)
	go func() {
		r := handler.Handle(ctx, Call{Syscall: SysPselect6, Args: [6]uint64{16, 0x2000, 0, 0, 0, 0}})
		done <- reply{r.Value, r.Errno}
	}()
	// 等待读取端阻塞后写入数据唤醒。
	time.Sleep(30 * time.Millisecond)
	if err := writePipe(t, table, pid, writer, "wake"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.errno != "" || got.value == 0 {
			t.Fatalf("pselect6: %+v", got)
		}
		bits := readFDSetBits(t, handler, 0x2000)
		if len(bits) != 1 || bits[0] != uint64(reader) {
			t.Fatalf("读集合=%v want [%d]", bits, reader)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("管道就绪未唤醒 pselect6")
	}
}

// writePipe 经描述符表写管道。
func writePipe(t *testing.T, table *fd.Table, pid uint64, writer uint32, message string) error {
	t.Helper()
	handle, err := table.Get(context.Background(), pid, writer)
	if err != nil {
		return err
	}
	_, err = handle.Write([]byte(message))
	return err
}

// writeEpollEvent 在客体内存写一个 struct epoll_event。
func writeEpollEvent(t *testing.T, handler Syscalls, address uint64, events uint32, data uint64) {
	t.Helper()
	var raw [16]byte
	binary.LittleEndian.PutUint32(raw[0:], events)
	binary.LittleEndian.PutUint64(raw[8:], data)
	if err := handler.Memory.Write(context.Background(), address, raw[:]); err != nil {
		t.Fatal(err)
	}
}

// readEpollEvents 读回就绪事件数组。
func readEpollEvents(t *testing.T, handler Syscalls, address uint64, count int) []struct {
	events uint32
	data   uint64
} {
	t.Helper()
	raw := make([]byte, count*16)
	if err := handler.Memory.Read(context.Background(), address, raw); err != nil {
		t.Fatal(err)
	}
	out := make([]struct {
		events uint32
		data   uint64
	}, count)
	for i := range out {
		out[i].events = binary.LittleEndian.Uint32(raw[i*16:])
		out[i].data = binary.LittleEndian.Uint64(raw[i*16+8:])
	}
	return out
}

// TestEpollCreate1CtlAndLevelTriggeredWait 验证 L07:create1 建实例,
// ctl 增删改与重复登记,水平触发等待报告管道读就绪并随读空恢复
// 未就绪;普通文件恒就绪;坏描述符 EBADF。
func TestEpollCreate1CtlAndLevelTriggeredWait(t *testing.T) {
	ctx := context.Background()
	table := fd.New()
	const pid = 93
	table.Attach(pid, fd.Streams{})
	if err := table.Install(pid, 3, &plainHandle{}); err != nil { // 普通文件
		t.Fatal(err)
	}
	reader, writer, err := table.Pipe(pid)
	if err != nil {
		t.Fatal(err)
	}
	handler := Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		Clock:       &fakeClock{monotonic: 0},
		Sleeper:     &fakeSleeper{onSleep: func(ns uint64) {}},
	}

	// create1:未知标志 EINVAL。
	if reply := handler.Handle(ctx, Call{Syscall: SysEpollCreate1, Args: [6]uint64{0x8}}); reply.Errno != errno.Invalid {
		t.Fatalf("未知标志: %+v want EINVAL", reply)
	}
	reply := handler.Handle(ctx, Call{Syscall: SysEpollCreate1})
	if reply.Errno != "" {
		t.Fatalf("create1: %+v", reply)
	}
	epfd := reply.Value

	// ctl:登记普通文件(恒就绪)与管道读端。
	writeEpollEvent(t, handler, 0x4000, epollIn, 0x77)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlAdd, 3, 0x4000}}); r.Errno != "" {
		t.Fatalf("登记文件: %+v", r)
	}
	writeEpollEvent(t, handler, 0x4010, epollIn, uint64(reader))
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlAdd, uint64(reader), 0x4010}}); r.Errno != "" {
		t.Fatalf("登记管道读端: %+v", r)
	}
	// 重复登记 EEXIST;未知操作 EINVAL;坏目标 EBADF。
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlAdd, 3, 0x4000}}); r.Errno != errno.Exist {
		t.Fatalf("重复登记: %+v want EEXIST", r)
	}
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, 9, 3, 0x4000}}); r.Errno != errno.Invalid {
		t.Fatalf("未知操作: %+v want EINVAL", r)
	}
	writeEpollEvent(t, handler, 0x4020, epollIn, 0)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlAdd, 99, 0x4020}}); r.Errno != errno.BadHandle {
		t.Fatalf("坏目标: %+v want EBADF", r)
	}

	// pwait 0ms:普通文件就绪 → 1 条事件,data 透传。
	r := handler.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0x5000, 8, 0, 0}})
	if r.Errno != "" || r.Value != 1 {
		t.Fatalf("立即等待: %+v want 1", r)
	}
	events := readEpollEvents(t, handler, 0x5000, 1)
	if events[0].events&epollIn == 0 || events[0].data != 0x77 {
		t.Fatalf("文件事件: %+v", events[0])
	}

	// 删除文件登记后,空管道读端未就绪 → 超时返回 0。
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlDel, 3, 0}}); r.Errno != "" {
		t.Fatalf("删除: %+v", r)
	}
	slept := make(chan uint64, 16)
	handler.Sleeper = &fakeSleeper{onSleep: func(ns uint64) { slept <- ns }}
	r = handler.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0x5000, 8, 5, 0}})
	if r.Errno != "" || r.Value != 0 {
		t.Fatalf("空等待超时: %+v want 0", r)
	}
	total := uint64(0)
	for {
		select {
		case ns := <-slept:
			total += ns
		default:
			goto sleptEnough
		}
	}
sleptEnough:
	if total != 5_000_000 {
		t.Fatalf("超时睡眠总时长=%d want 5ms", total)
	}

	// 水平触发:写入数据 → 就绪;读空 → 恢复未就绪;再写 → 又就绪。
	if err := writePipe(t, table, pid, writer, "hi"); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		r = handler.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0x5000, 8, 0, 0}})
		if r.Errno != "" || r.Value != 1 {
			t.Fatalf("就绪轮 %d: %+v", round, r)
		}
		events = readEpollEvents(t, handler, 0x5000, 1)
		if events[0].events&epollIn == 0 || events[0].data != uint64(reader) {
			t.Fatalf("管道事件轮 %d: %+v", round, events[0])
		}
		// 读空数据,恢复未就绪。
		handle, err := table.Get(ctx, pid, reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handle.Read(make([]byte, 2)); err != nil {
			t.Fatal(err)
		}
		r = handler.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0x5000, 8, 0, 0}})
		if r.Errno != "" || r.Value != 0 {
			t.Fatalf("读空后轮 %d: %+v want 0", round, r)
		}
		if err := writePipe(t, table, pid, writer, "hi"); err != nil {
			t.Fatal(err)
		}
	}

	// MOD:把 data 改掉后报告新 data。
	writeEpollEvent(t, handler, 0x4030, epollIn, 0xBEEF)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlMod, uint64(reader), 0x4030}}); r.Errno != "" {
		t.Fatalf("修改: %+v", r)
	}
	r = handler.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0x5000, 8, 0, 0}})
	if r.Errno != "" || r.Value != 1 {
		t.Fatalf("修改后等待: %+v", r)
	}
	events = readEpollEvents(t, handler, 0x5000, 1)
	if events[0].data != 0xBEEF {
		t.Fatalf("修改后 data=%#x want 0xBEEF", events[0].data)
	}
}

// TestEpollPwaitBlocksAndWakes 验证 L07:无限等待(-1)阻塞到就绪
// 变化后唤醒。
func TestEpollPwaitBlocksAndWakes(t *testing.T) {
	ctx := context.Background()
	table := fd.New()
	const pid = 94
	table.Attach(pid, fd.Streams{})
	reader, writer, err := table.Pipe(pid)
	if err != nil {
		t.Fatal(err)
	}
	handler := Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		Clock:       &fakeClock{monotonic: 0},
		Sleeper:     &fakeSleeper{onSleep: func(ns uint64) {}},
	}
	reply := handler.Handle(ctx, Call{Syscall: SysEpollCreate1})
	if reply.Errno != "" {
		t.Fatal(reply)
	}
	epfd := reply.Value
	writeEpollEvent(t, handler, 0x4000, epollIn, 7)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlAdd, uint64(reader), 0x4000}}); r.Errno != "" {
		t.Fatal(r)
	}
	type result struct {
		value uint64
		errno errno.Code
	}
	done := make(chan result, 1)
	go func() {
		// timeout=-1(补码)表示无限等待。
		r := handler.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0x5000, 8, ^uint64(0), 0}})
		done <- result{r.Value, r.Errno}
	}()
	time.Sleep(30 * time.Millisecond)
	if err := writePipe(t, table, pid, writer, "wake"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.errno != "" || got.value != 1 {
			t.Fatalf("无限等待: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("就绪变化未唤醒无限等待")
	}
}

// writeTimespec 在客体内存写一个 struct timespec。
func writeTimespec(t *testing.T, handler Syscalls, address uint64, seconds int64, nanoseconds int64) {
	t.Helper()
	var raw [16]byte
	binary.LittleEndian.PutUint64(raw[0:], uint64(seconds))
	binary.LittleEndian.PutUint64(raw[8:], uint64(nanoseconds))
	if err := handler.Memory.Write(context.Background(), address, raw[:]); err != nil {
		t.Fatal(err)
	}
}

// TestEpollEdgeTriggeredAndOneShotThroughSyscalls 验证 L08:EPOLLET 经
// ctl 登记后只在初始边沿与新活动时报告(等待 0ms),非阻塞语义下
// 重复查询不再报告;EPOLLONESHOT 报告一次后禁用,MOD 重新武装。
func TestEpollEdgeTriggeredAndOneShotThroughSyscalls(t *testing.T) {
	ctx := context.Background()
	table := fd.New()
	const pid = 95
	table.Attach(pid, fd.Streams{})
	reader, writer, err := table.Pipe(pid)
	if err != nil {
		t.Fatal(err)
	}
	handler := Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		Clock:       &fakeClock{monotonic: 0},
		Sleeper:     &fakeSleeper{onSleep: func(ns uint64) {}},
	}
	reply := handler.Handle(ctx, Call{Syscall: SysEpollCreate1})
	if reply.Errno != "" {
		t.Fatal(reply)
	}
	epfd := reply.Value

	// 边缘触发登记:空管道无初始边沿。
	writeEpollEvent(t, handler, 0x4000, epollIn|epollEt, 0x77)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlAdd, uint64(reader), 0x4000}}); r.Errno != "" {
		t.Fatal(r)
	}
	pollZero := func() uint64 {
		r := handler.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0x5000, 8, 0, 0}})
		if r.Errno != "" {
			t.Fatalf("pwait 0ms: %+v", r)
		}
		return r.Value
	}
	if got := pollZero(); got != 0 {
		t.Fatalf("空管道边缘等待=%d want 0", got)
	}
	// 写入 → 边沿报告一次;数据未读再查不再报告(水平触发会)。
	if err := writePipe(t, table, pid, writer, "hi"); err != nil {
		t.Fatal(err)
	}
	if got := pollZero(); got != 1 {
		t.Fatalf("首次边沿=%d want 1", got)
	}
	events := readEpollEvents(t, handler, 0x5000, 1)
	if events[0].events&epollIn == 0 || events[0].data != 0x77 {
		t.Fatalf("边沿事件: %+v", events[0])
	}
	if got := pollZero(); got != 0 {
		t.Fatalf("边沿消费后重复报告=%d want 0", got)
	}
	// 未排空时的新写入是新边沿。
	if err := writePipe(t, table, pid, writer, "yo"); err != nil {
		t.Fatal(err)
	}
	if got := pollZero(); got != 1 {
		t.Fatalf("未排空新活动=%d want 1", got)
	}
	// 排空后 MOD 换一次性模式:排空状态无初始报告;新数据报告一次
	// 后禁用,再写也不报;重新武装后立即可报。
	handle, err := table.Get(ctx, pid, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Read(make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	writeEpollEvent(t, handler, 0x4010, epollIn|epollOneShot, 0x88)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlMod, uint64(reader), 0x4010}}); r.Errno != "" {
		t.Fatal(r)
	}
	if got := pollZero(); got != 0 {
		t.Fatalf("排空后重新武装立即报告=%d want 0", got)
	}
	if err := writePipe(t, table, pid, writer, "1"); err != nil {
		t.Fatal(err)
	}
	if got := pollZero(); got != 1 {
		t.Fatalf("一次性首次报告=%d want 1", got)
	}
	events = readEpollEvents(t, handler, 0x5000, 1)
	if events[0].data != 0x88 {
		t.Fatalf("一次性事件 data=%#x want 0x88", events[0].data)
	}
	if got := pollZero(); got != 0 {
		t.Fatalf("一次性禁用后仍报告=%d want 0", got)
	}
	if err := writePipe(t, table, pid, writer, "2"); err != nil {
		t.Fatal(err)
	}
	if got := pollZero(); got != 0 {
		t.Fatalf("一次性禁用后新数据仍报告=%d want 0", got)
	}
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlMod, uint64(reader), 0x4010}}); r.Errno != "" {
		t.Fatal(r)
	}
	if got := pollZero(); got != 1 {
		t.Fatalf("重新武装后未报告=%d want 1", got)
	}
}

// TestEpollPwait2TimespecAndSigmask 验证 L08:epoll_pwait2 超时为
// timespec 指针(NULL 无限、零立即、非法值 EINVAL),sigmask 是
// 直指针且非空时长度必须为 8。
func TestEpollPwait2TimespecAndSigmask(t *testing.T) {
	ctx := context.Background()
	table := fd.New()
	const pid = 96
	table.Attach(pid, fd.Streams{})
	reader, writer, err := table.Pipe(pid)
	if err != nil {
		t.Fatal(err)
	}
	handler := Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		Clock:       &fakeClock{monotonic: 0},
		Sleeper:     &fakeSleeper{onSleep: func(ns uint64) {}},
	}
	reply := handler.Handle(ctx, Call{Syscall: SysEpollCreate1})
	if reply.Errno != "" {
		t.Fatal(reply)
	}
	epfd := reply.Value
	writeEpollEvent(t, handler, 0x4000, epollIn, 0)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, epollCtlAdd, uint64(reader), 0x4000}}); r.Errno != "" {
		t.Fatal(r)
	}

	// timespec 非法:nsec 越界与负秒 EINVAL。
	writeTimespec(t, handler, 0x6000, 0, 1_000_000_000)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollPwait2, Args: [6]uint64{epfd, 0x5000, 8, 0x6000, 0, 0}}); r.Errno != errno.Invalid {
		t.Fatalf("纳秒越界: %+v want EINVAL", r)
	}
	writeTimespec(t, handler, 0x6000, -1, 0)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollPwait2, Args: [6]uint64{epfd, 0x5000, 8, 0x6000, 0, 0}}); r.Errno != errno.Invalid {
		t.Fatalf("负秒: %+v want EINVAL", r)
	}
	// sigmask 非空且长度 ≠ 8:EINVAL。
	writeTimespec(t, handler, 0x6000, 0, 1_000_000)
	if r := handler.Handle(ctx, Call{Syscall: SysEpollPwait2, Args: [6]uint64{epfd, 0x5000, 8, 0x6000, 0x7000, 7}}); r.Errno != errno.Invalid {
		t.Fatalf("sigsetsize≠8: %+v want EINVAL", r)
	}
	// 有限超时(1ms)空实例到期返回 0;NULL timespec 无限路径由
	// ReadyNow 快路径覆盖(就绪即返回)。
	slept := make(chan uint64, 16)
	handler.Sleeper = &fakeSleeper{onSleep: func(ns uint64) { slept <- ns }}
	if r := handler.Handle(ctx, Call{Syscall: SysEpollPwait2, Args: [6]uint64{epfd, 0x5000, 8, 0x6000, 0, 0}}); r.Errno != "" || r.Value != 0 {
		t.Fatalf("超时返回: %+v want 0", r)
	}
	total := uint64(0)
	for {
		select {
		case ns := <-slept:
			total += ns
		default:
			goto sleptEnough
		}
	}
sleptEnough:
	if total != 1_000_000 {
		t.Fatalf("超时睡眠总时长=%d want 1ms", total)
	}
	// 就绪项 + NULL timespec(无限):立即报告。
	if err := writePipe(t, table, pid, writer, "x"); err != nil {
		t.Fatal(err)
	}
	if r := handler.Handle(ctx, Call{Syscall: SysEpollPwait2, Args: [6]uint64{epfd, 0x5000, 8, 0, 0, 0}}); r.Errno != "" || r.Value != 1 {
		t.Fatalf("无限路径就绪报告: %+v want 1", r)
	}
}
