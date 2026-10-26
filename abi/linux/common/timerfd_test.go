package common

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// realSource 是真实时间时钟,供 timerfd 的 abi 层测试使用。
type realSource struct{ start time.Time }

func newRealSource() realSource { return realSource{start: time.Now()} }

func (c realSource) Wall(context.Context) (int64, error) { return time.Now().UnixNano(), nil }

func (c realSource) Monotonic(context.Context) (uint64, error) {
	return uint64(time.Since(c.start)), nil
}

func (c realSource) Sleep(ctx context.Context, nanoseconds uint64) error {
	timer := time.NewTimer(time.Duration(nanoseconds))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// writeITimerspecRaw 在客体内存写 itimerspec(纳秒)。
func writeITimerspecRaw(t *testing.T, handler Syscalls, address uint64, intervalNs, valueNs uint64) {
	t.Helper()
	var raw [32]byte
	binary.LittleEndian.PutUint64(raw[0:], intervalNs/1_000_000_000)
	binary.LittleEndian.PutUint64(raw[8:], intervalNs%1_000_000_000)
	binary.LittleEndian.PutUint64(raw[16:], valueNs/1_000_000_000)
	binary.LittleEndian.PutUint64(raw[24:], valueNs%1_000_000_000)
	if err := handler.Memory.Write(context.Background(), address, raw[:]); err != nil {
		t.Fatal(err)
	}
}

// readITimerspecRaw 读回客体内存中的 itimerspec(纳秒)。
func readITimerspecRaw(t *testing.T, handler Syscalls, address uint64) (intervalNs, valueNs uint64) {
	t.Helper()
	var raw [32]byte
	if err := handler.Memory.Read(context.Background(), address, raw[:]); err != nil {
		t.Fatal(err)
	}
	intervalNs = binary.LittleEndian.Uint64(raw[0:]) * 1_000_000_000
	intervalNs += binary.LittleEndian.Uint64(raw[8:])
	valueNs = binary.LittleEndian.Uint64(raw[16:]) * 1_000_000_000
	valueNs += binary.LittleEndian.Uint64(raw[24:])
	return intervalNs, valueNs
}

// TestTimerfdSyscallLifecycle 验证 L10:create1/settime/gettime/read
// 全链路:周期计时、剩余查询、非阻塞读、解除武装、旧状态写回与
// 参数校验。
func TestTimerfdSyscallLifecycle(t *testing.T) {
	ctx := context.Background()
	table := fd.New()
	table.SetTimeSource(newRealSource())
	const pid = 61
	table.Attach(pid, fd.Streams{})
	handler := Syscalls{
		Memory:      newFakeMemory(),
		Clock:       newRealSource(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		TimerSource: ProcessDescriptors{Table: table, PID: pid},
	}
	// 校验:未知时钟与未知标志 EINVAL。
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdCreate, Args: [6]uint64{7, 0}}); r.Errno != errno.Invalid {
		t.Fatalf("未知时钟: %+v want EINVAL", r)
	}
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdCreate, Args: [6]uint64{1, 0x8}}); r.Errno != errno.Invalid {
		t.Fatalf("未知标志: %+v want EINVAL", r)
	}
	reply := handler.Handle(ctx, Call{Syscall: SysTimerfdCreate, Args: [6]uint64{1, tfdNonBlock}})
	if reply.Errno != "" {
		t.Fatal(reply)
	}
	tfd := reply.Value

	// settime:30ms 首次、50ms 周期,旧状态写到 old。
	writeITimerspecRaw(t, handler, 0x5000, 0, 30) // 全零旧值缓冲
	writeITimerspecRaw(t, handler, 0x6000, 50*1e6, 30*1e6)
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdSettime, Args: [6]uint64{tfd, 0, 0x6000, 0x5000}}); r.Errno != "" {
		t.Fatalf("settime: %+v", r)
	}
	oldInterval, oldValue := readITimerspecRaw(t, handler, 0x5000)
	if oldInterval != 0 || oldValue != 0 {
		t.Fatalf("旧状态=%d/%d want 0/0", oldInterval, oldValue)
	}
	// gettime:剩余在 (0,30ms],区间 50ms。
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdGettime, Args: [6]uint64{tfd, 0x5000}}); r.Errno != "" {
		t.Fatalf("gettime: %+v", r)
	}
	interval, value := readITimerspecRaw(t, handler, 0x5000)
	if interval != 50*1e6 || value == 0 || value > 30*1e6 {
		t.Fatalf("剩余=%d/%d want 50ms/(0,30ms]", interval, value)
	}
	// 非阻塞读:到期前 EAGAIN。
	readBuffer := make([]byte, 8)
	stream, err := table.Get(ctx, pid, uint32(tfd))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Read(readBuffer); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("到期前读 err=%v want EAGAIN", err)
	}
	// 到期(30ms):读 1;两次周期(80/130ms):累计读 2。
	time.Sleep(40 * time.Millisecond)
	if _, err := stream.Read(readBuffer); err != nil || binary.LittleEndian.Uint64(readBuffer) != 1 {
		t.Fatalf("首次读=%d err=%v", binary.LittleEndian.Uint64(readBuffer), err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := stream.Read(readBuffer); err != nil || binary.LittleEndian.Uint64(readBuffer) != 2 {
		t.Fatalf("累计读=%d err=%v", binary.LittleEndian.Uint64(readBuffer), err)
	}
	// 解除武装:全零 value → gettime 全零,读 EAGAIN。
	writeITimerspecRaw(t, handler, 0x6000, 0, 0)
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdSettime, Args: [6]uint64{tfd, 0, 0x6000, 0}}); r.Errno != "" {
		t.Fatalf("解除武装: %+v", r)
	}
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdGettime, Args: [6]uint64{tfd, 0x5000}}); r.Errno != "" {
		t.Fatal(r)
	}
	interval, value = readITimerspecRaw(t, handler, 0x5000)
	if interval != 0 || value != 0 {
		t.Fatalf("解除后=%d/%d want 0/0", interval, value)
	}
	if _, err := stream.Read(readBuffer); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("解除后读 err=%v want EAGAIN", err)
	}
	// 非 timerfd 描述符上 settime → EINVAL;坏描述符 → EBADF。
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdGettime, Args: [6]uint64{99, 0x5000}}); r.Errno != errno.BadHandle {
		t.Fatalf("坏描述符: %+v want EBADF", r)
	}
	// ABSTIME 标志:过去时刻立即到期。
	now, _ := handler.Clock.Monotonic(ctx)
	writeITimerspecRaw(t, handler, 0x6000, 0, now-1)
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdSettime, Args: [6]uint64{tfd, tfdAbstime, 0x6000, 0}}); r.Errno != "" {
		t.Fatalf("过去绝对时刻: %+v", r)
	}
	if _, err := stream.Read(readBuffer); err != nil || binary.LittleEndian.Uint64(readBuffer) != 1 {
		t.Fatalf("绝对过期读=%d err=%v", binary.LittleEndian.Uint64(readBuffer), err)
	}
}

// TestTimerfdWithoutTimeSource 验证 L10:时钟端口未装配时明确 ENOSYS。
func TestTimerfdWithoutTimeSource(t *testing.T) {
	ctx := context.Background()
	table := fd.New()
	const pid = 62
	table.Attach(pid, fd.Streams{})
	handler := Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		TimerSource: ProcessDescriptors{Table: table, PID: pid},
	}
	if r := handler.Handle(ctx, Call{Syscall: SysTimerfdCreate, Args: [6]uint64{1, 0}}); r.Errno != errno.NotImplemented {
		t.Fatalf("无时钟端口: %+v want ENOSYS", r)
	}
}
