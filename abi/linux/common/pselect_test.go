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
