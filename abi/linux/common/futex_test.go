package common

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/futex"
)

// futexHandler 构造带 futex 端口的调用层:客体字位于 0x3000。
func futexHandler(t *testing.T, value uint32) (Syscalls, *futex.Table) {
	t.Helper()
	table := futex.New()
	handler := Syscalls{
		Memory:   newFakeMemory(),
		Identity: &Identity{PID: 77},
		Clock:    &fakeClock{monotonic: 0},
		Sleeper:  &fakeSleeper{onSleep: func(ns uint64) {}},
		Futexes:  table,
	}
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], value)
	if err := handler.Memory.Write(context.Background(), 0x3000, raw[:]); err != nil {
		t.Fatal(err)
	}
	return handler, table
}

// writeFutexWord 写客体 futex 字。
func writeFutexWord(t *testing.T, handler Syscalls, address uint64, value uint32) {
	t.Helper()
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], value)
	if err := handler.Memory.Write(context.Background(), address, raw[:]); err != nil {
		t.Fatal(err)
	}
}

// TestFutexWaitValueMismatchValidatesArgs 验证 K05:值不匹配立即
// EAGAIN;未对齐地址、未知操作与实时钟标志 EINVAL。
func TestFutexWaitValueMismatchValidatesArgs(t *testing.T) {
	ctx := context.Background()
	handler, _ := futexHandler(t, 1)
	wait := func(args [6]uint64) Reply {
		return handler.Handle(ctx, Call{Syscall: SysFutex, Args: args})
	}
	// 值不匹配:字为 1,期望 0。
	if r := wait([6]uint64{0x3000, futexOpWait, 0, 0, 0, 0}); r.Errno != errno.WouldBlock {
		t.Fatalf("值不匹配: %+v want EAGAIN", r)
	}
	// 未对齐地址。
	if r := wait([6]uint64{0x3001, futexOpWait, 0, 0, 0, 0}); r.Errno != errno.Invalid {
		t.Fatalf("未对齐: %+v want EINVAL", r)
	}
	// 未知操作(PI 族/waitv 等随 K07 登记前 EINVAL)。
	if r := wait([6]uint64{0x3000, 0x20, 0, 0, 0, 0}); r.Errno != errno.Invalid {
		t.Fatalf("未知操作: %+v want EINVAL", r)
	}
	// FUTEX_CLOCK_REALTIME 与 WAIT/WAKE 不兼容。
	if r := wait([6]uint64{0x3000, futexOpWait | futexClockRealtime, 0, 0, 0, 0}); r.Errno != errno.Invalid {
		t.Fatalf("实时钟: %+v want EINVAL", r)
	}
	// 私有标志被接受(值不匹配路径)。
	if r := wait([6]uint64{0x3000, futexOpWait | futexPrivateFlag, 0, 0, 0, 0}); r.Errno != errno.WouldBlock {
		t.Fatalf("私有标志: %+v want EAGAIN", r)
	}
	// 读取失败转 EFAULT(fakeMemory 以 readErr 模拟坏指针)。
	handler.Memory.(*fakeMemory).readErr = errno.Fault
	if r := wait([6]uint64{0x9000, futexOpWait, 0, 0, 0, 0}); r.Errno != errno.Fault {
		t.Fatalf("坏指针: %+v want EFAULT", r)
	}
}

// TestFutexWaitTimesOut 验证 K05:值匹配时阻塞,超时到期返回
// ETIMEDOUT;负 timespec EINVAL。
func TestFutexWaitTimesOut(t *testing.T) {
	ctx := context.Background()
	handler, _ := futexHandler(t, 0)
	writeTimespec(t, handler, 0x4000, 0, 1_000_000)
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, 0x5B, 0, 0, 0, 0}}); r.Errno != errno.Invalid {
		t.Fatalf("未知操作: %+v want EINVAL", r)
	}
	r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWait, 0, 0x4000, 0, 0}})
	if r.Errno != errno.TimedOut {
		t.Fatalf("超时: %+v want ETIMEDOUT", r)
	}
	// 负 timespec。
	writeTimespec(t, handler, 0x4000, -1, 0)
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWait, 0, 0x4000, 0, 0}}); r.Errno != errno.Invalid {
		t.Fatalf("负超时: %+v want EINVAL", r)
	}
}

// TestFutexWaitBlocksAndWakesCrossThread 验证 K05:值匹配的无限等待
// 阻塞,跨执行流改值并 WAKE 唤醒后返回 0;WAKE 无等待者返回 0,
// 有等待者返回实际唤醒数。
func TestFutexWaitBlocksAndWakesCrossThread(t *testing.T) {
	ctx := context.Background()
	handler, table := futexHandler(t, 0)
	// 无等待者唤醒。
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWake, 1, 0, 0, 0}}); r.Errno != "" || r.Value != 0 {
		t.Fatalf("空唤醒: %+v want 0", r)
	}
	type reply struct {
		errno errno.Code
	}
	done := make(chan reply, 1)
	go func() {
		r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWait, 0, 0, 0, 0}})
		done <- reply{r.Errno}
	}()
	// 等待者入队后改值并经端口唤醒(写后唤醒的有序序列)。
	time.Sleep(30 * time.Millisecond)
	writeFutexWord(t, handler, 0x3000, 1)
	woken := table.Wake(futex.Key{Owner: 77, Address: 0x3000}, 1)
	if woken != 1 {
		t.Fatalf("唤醒数=%d want 1", woken)
	}
	select {
	case got := <-done:
		if got.errno != "" {
			t.Fatalf("被唤醒: %+v want 0", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("唤醒未结束等待")
	}
	// 等待者出队后再唤醒返回 0。
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWake, 1, 0, 0, 0}}); r.Value != 0 {
		t.Fatalf("空唤醒: %+v want 0", r)
	}
}

// TestFutexWakeWakesSyscallWaiter 验证 K05:阻塞中的系统调用等待者
// 由 WAKE 系统调用本身唤醒(系统调用层往返)。
func TestFutexWakeWakesSyscallWaiter(t *testing.T) {
	ctx := context.Background()
	handler, _ := futexHandler(t, 0)
	done := make(chan Reply, 1)
	go func() {
		// 无限等待:阻塞到唤醒(无信号端口时直通等待结果)。
		done <- handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWait, 0, 0, 0, 0}})
	}()
	time.Sleep(30 * time.Millisecond)
	// 值未变:WAKE 唤醒匹配的等待者(虚假条件由客体循环复查)。
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWake, 1, 0, 0, 0}}); r.Errno != "" || r.Value != 1 {
		t.Fatalf("唤醒: %+v want 1", r)
	}
	select {
	case got := <-done:
		if got.Errno != "" {
			t.Fatalf("被唤醒: %+v want 0", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WAKE 未唤醒系统调用等待者")
	}
}

// TestFutexOwnerKeysIsolate 验证 K05:等待表按进程键控,另一进程的
// WAKE 不唤醒当前进程的等待者。
func TestFutexOwnerKeysIsolate(t *testing.T) {
	ctx := context.Background()
	handler, table := futexHandler(t, 0)
	other := Syscalls{
		Memory:   handler.Memory,
		Identity: &Identity{PID: 88},
		Futexes:  table,
	}
	done := make(chan Reply, 1)
	go func() {
		done <- handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWait, 0, 0, 0, 0}})
	}()
	time.Sleep(30 * time.Millisecond)
	// 异进程同地址唤醒:返回 0(键不同)。
	if r := other.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWake, 1, 0, 0, 0}}); r.Value != 0 {
		t.Fatalf("异进程唤醒=%d want 0", r.Value)
	}
	select {
	case got := <-done:
		t.Fatalf("异进程唤醒错误到达: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	// 同进程唤醒收尾,等待者正常返回。
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3000, futexOpWake, 1, 0, 0, 0}}); r.Value != 1 {
		t.Fatalf("同进程唤醒=%d want 1", r.Value)
	}
	select {
	case got := <-done:
		if got.Errno != "" {
			t.Fatalf("被唤醒: %+v want 0", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("同进程唤醒未结束等待")
	}
}

// TestFutexRequeueAndBitsetThroughSyscalls 验证 K06:REQUEUE 唤醒并
// 迁移(CMP 变体值校验 EAGAIN)、WAIT_BITSET 超时与位集 0 EINVAL、
// WAKE_BITSET 计数返回。
func TestFutexRequeueAndBitsetThroughSyscalls(t *testing.T) {
	ctx := context.Background()
	handler, table := futexHandler(t, 0)
	const address = 0x3000

	// 三个等待者登记在 address。
	done := make(chan Reply, 3)
	for i := 0; i < 3; i++ {
		go func() {
			done <- handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpWait, 0, 0, 0, 0}})
		}()
	}
	time.Sleep(30 * time.Millisecond)
	// CMP_REQUEUE 值不匹配:字为 0,期望 1 → EAGAIN。
	writeFutexWord(t, handler, address, 0)
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpCmpRequeue, 1, 2, 0x3100, 1}}); r.Errno != errno.WouldBlock {
		t.Fatalf("CMP 不匹配: %+v want EAGAIN", r)
	}
	// CMP_REQUEUE 匹配:唤醒 1,迁移 2 到 0x3100。
	writeFutexWord(t, handler, address, 0)
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpCmpRequeue, 1, 2, 0x3100, 0}}); r.Errno != "" || r.Value != 3 {
		t.Fatalf("CMP requeue: %+v want 3", r)
	}
	select {
	case got := <-done:
		if got.Errno != "" {
			t.Fatalf("被唤醒: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("requeue 唤醒未生效")
	}
	// 迁移等待者由目标键 WAKE 唤醒(2 个)。
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{0x3100, futexOpWake, 2, 0, 0, 0}}); r.Errno != "" || r.Value != 2 {
		t.Fatalf("目标键唤醒: %+v want 2", r)
	}
	for i := 0; i < 2; i++ {
		select {
		case got := <-done:
			if got.Errno != "" {
				t.Fatalf("迁移等待者: %+v", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("目标键唤醒未生效")
		}
	}
	// WAIT_BITSET:位集 0 EINVAL;超时已过期立即 ETIMEDOUT;
	// 正常绝对超时到期 ETIMEDOUT。
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpWaitBitset, 0, 0, 0, 0}}); r.Errno != errno.Invalid {
		t.Fatalf("零位集: %+v want EINVAL", r)
	}
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpWaitBitset | futexClockRealtime, 0, 0, 0xFFFFFFFF, 0}}); r.Errno != errno.Invalid {
		t.Fatalf("实时钟位集: %+v want EINVAL", r)
	}
	// 位集超时基于单调钟:把假钟拨到 1s,避免 now-1 下溢。
	handler.Clock.(*fakeClock).monotonic = 1_000_000_000
	now, _ := handler.Clock.Monotonic(ctx)
	past := now - 1
	writeTimespec(t, handler, 0x4000, int64(past/1_000_000_000), int64(past%1_000_000_000))
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpWaitBitset, 0, 0x4000, 0xFFFFFFFF, 0}}); r.Errno != errno.TimedOut {
		t.Fatalf("过去绝对时刻: %+v want ETIMEDOUT", r)
	}
	future := now + 2_000_000
	writeTimespec(t, handler, 0x4000, int64(future/1_000_000_000), int64(future%1_000_000_000))
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpWaitBitset, 0, 0x4000, 0xFFFFFFFF, 0}}); r.Errno != errno.TimedOut {
		t.Fatalf("2ms 绝对超时: %+v want ETIMEDOUT", r)
	}
	// WAKE_BITSET 无等待者返回 0;值不匹配的 WAIT_BITSET EAGAIN。
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpWakeBitset, 1, 0, 0xFFFFFFFF, 0}}); r.Errno != "" || r.Value != 0 {
		t.Fatalf("空唤醒: %+v want 0", r)
	}
	if r := handler.Handle(ctx, Call{Syscall: SysFutex, Args: [6]uint64{address, futexOpWaitBitset, 1, 0, 0xFFFFFFFF, 0}}); r.Errno != errno.WouldBlock {
		t.Fatalf("位集值不匹配: %+v want EAGAIN", r)
	}
	_ = table
}
