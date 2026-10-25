package common

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// plainHandle 是不报告就绪状态的普通文件句柄。
type plainHandle struct{}

func (h *plainHandle) Read([]byte) (int, error)    { return 0, nil }
func (h *plainHandle) Write(p []byte) (int, error) { return len(p), nil }
func (h *plainHandle) Close() error                { return nil }

// pollReadyHandle 是一个可报告就绪状态的描述符句柄。
type pollReadyHandle struct {
	ready poll.Readiness
}

func (h *pollReadyHandle) Read([]byte) (int, error)    { return 0, nil }
func (h *pollReadyHandle) Write(p []byte) (int, error) { return len(p), nil }
func (h *pollReadyHandle) Close() error                { return nil }
func (h *pollReadyHandle) Ready() poll.Readiness       { return h.ready }
func (h *pollReadyHandle) OnChange(func()) (cancel func()) {
	return func() {}
}

// writePollfd 写入一个 struct pollfd。
func writePollfd(t *testing.T, space *fakeMemory, address uint64, descriptor uint32, events uint16) {
	t.Helper()
	var raw [pollfdSize]byte
	binary.LittleEndian.PutUint32(raw[0:], descriptor)
	binary.LittleEndian.PutUint16(raw[4:], events)
	if err := space.Write(context.Background(), address, raw[:]); err != nil {
		t.Fatal(err)
	}
}

// readPollfd 读回一个 struct pollfd 的 revents。
func readPollfd(t *testing.T, space *fakeMemory, address uint64) uint16 {
	t.Helper()
	var raw [pollfdSize]byte
	if err := space.Read(context.Background(), address, raw[:]); err != nil {
		t.Fatal(err)
	}
	return binary.LittleEndian.Uint16(raw[6:])
}

// TestPollReportsReadyRegularFileAndClosedDescriptor 验证 L05:普通文件
// 恒就绪,关闭的描述符报 POLLNVAL。
func TestPollReportsReadyRegularFileAndClosedDescriptor(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	table := fd.New()
	const pid = 61
	table.Attach(pid, fd.Streams{})
	// 一个普通文件句柄(不实现就绪能力)。
	if err := table.Install(pid, 3, &plainHandle{}); err != nil {
		t.Fatal(err)
	}
	handler := Syscalls{Memory: space, Descriptors: ProcessDescriptors{Table: table, PID: pid}}
	writePollfd(t, space, 0x2000, 3, pollIn|pollOut)
	writePollfd(t, space, 0x2000+pollfdSize, 99, pollIn)
	reply := handler.Handle(ctx, Call{Syscall: SysPoll, Args: [6]uint64{0x2000, 2, 0}})
	if reply.Errno != "" {
		t.Fatalf("poll: %+v", reply)
	}
	if reply.Value != 2 {
		t.Fatalf("就绪数=%d want 2", reply.Value)
	}
	revents := readPollfd(t, space, 0x2000)
	if revents&(pollIn|pollOut) == 0 {
		t.Fatalf("普通文件应就绪: revents=%#x", revents)
	}
	closed := readPollfd(t, space, 0x2000+pollfdSize)
	if closed&pollNVal == 0 {
		t.Fatalf("关闭的描述符应报 POLLNVAL: revents=%#x", closed)
	}
}

// TestPollWaitsForReadiness 验证 L05:未就绪的描述符等待到就绪后返回,
// 超时返回 0。
func TestPollWaitsForReadiness(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	table := fd.New()
	const pid = 62
	table.Attach(pid, fd.Streams{})
	ready := &pollReadyHandle{}
	if err := table.Install(pid, 3, ready); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{monotonic: 0}
	sleeper := &fakeSleeper{onSleep: func(ns uint64) { clock.monotonic += ns }}
	handler := Syscalls{Memory: space, Descriptors: ProcessDescriptors{Table: table, PID: pid}, Clock: clock, Sleeper: sleeper}
	writePollfd(t, space, 0x2100, 3, pollIn)
	// 超时:没有就绪事件,等待到期后返回 0。
	reply := handler.Handle(ctx, Call{Syscall: SysPoll, Args: [6]uint64{0x2100, 1, 10}})
	if reply.Errno != "" {
		t.Fatalf("poll: %+v", reply)
	}
	if reply.Value != 0 {
		t.Fatalf("超时返回=%d want 0", reply.Value)
	}
	if revents := readPollfd(t, space, 0x2100); revents != 0 {
		t.Fatalf("超时后 revents=%#x want 0", revents)
	}
	// 就绪:描述符可读时立即返回。
	ready.ready = poll.Readable
	reply = handler.Handle(ctx, Call{Syscall: SysPoll, Args: [6]uint64{0x2100, 1, 0}})
	if reply.Errno != "" || reply.Value != 1 {
		t.Fatalf("poll: %+v want 1 个就绪", reply)
	}
	if revents := readPollfd(t, space, 0x2100); revents&pollIn == 0 {
		t.Fatalf("revents=%#x want POLLIN", revents)
	}
}

// TestPpollAppliesSignalMaskDuringWait 验证 L05:ppoll 在等待期间替换
// 信号屏蔽集,结束后恢复。
func TestPpollAppliesSignalMaskDuringWait(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	table := fd.New()
	const pid = 63
	table.Attach(pid, fd.Streams{})
	ready := &pollReadyHandle{ready: poll.Readable}
	if err := table.Install(pid, 3, ready); err != nil {
		t.Fatal(err)
	}
	signals := &fakeSignals{}
	handler := Syscalls{Memory: space, Descriptors: ProcessDescriptors{Table: table, PID: pid}, Signals: signals}
	writePollfd(t, space, 0x2200, 3, pollIn)
	mask := make([]byte, 8)
	binary.LittleEndian.PutUint64(mask, 1<<(10-1))
	if err := space.Write(ctx, 0x2300, mask); err != nil {
		t.Fatal(err)
	}
	reply := handler.Handle(ctx, Call{Syscall: SysPpoll, Args: [6]uint64{0x2200, 1, 0, 0x2300, sigsetSize}})
	if reply.Errno != "" {
		t.Fatalf("ppoll: %+v", reply)
	}
	// 等待期间屏蔽集被替换,结束后恢复原值。
	if signals.mask != 0 {
		t.Fatalf("等待结束后屏蔽集=%x want 0(已恢复)", signals.mask)
	}
}

// TestPollRejectsInvalidArguments 验证 L05:描述符数量超限与非法参数
// 报 EINVAL。
func TestPollRejectsInvalidArguments(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	handler := Syscalls{Memory: space, Descriptors: ProcessDescriptors{Table: fd.New(), PID: 64}}
	if reply := handler.Handle(ctx, Call{Syscall: SysPoll, Args: [6]uint64{0x1000, pollMaxFDs + 1, 0}}); reply.Errno != errno.Invalid {
		t.Fatalf("超限 poll: %+v want EINVAL", reply)
	}
	// 描述符不存在:按 POLLNVAL 计为一个就绪事件(Linux 语义)。
	if reply := handler.Handle(ctx, Call{Syscall: SysPoll, Args: [6]uint64{0x1000, 1, 0}}); reply.Errno != "" || reply.Value != 1 {
		t.Fatalf("poll: %+v want 1 个 POLLNVAL 事件", reply)
	}
	_ = time.Millisecond
}
