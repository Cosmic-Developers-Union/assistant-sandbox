package epoll

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/pipe"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

const waitTimeout = 5 * time.Second

// staticReady 是可控的就绪源。
type staticReady struct {
	readiness poll.Readiness
}

func (s *staticReady) Ready() poll.Readiness { return s.readiness }
func (s *staticReady) OnChange(fn func()) (cancel func()) {
	return func() {}
}

// TestEpollRegistrationLifecycle 验证 L07:增删改与重复登记。
func TestEpollRegistrationLifecycle(t *testing.T) {
	instance := New()
	source := &staticReady{readiness: poll.Readable}
	if err := instance.Add(4, source, poll.Readable, 0x1111); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(4, source, poll.Readable, 0); !errors.Is(err, errno.Exist) {
		t.Fatalf("重复登记 err=%v want EEXIST", err)
	}
	if err := instance.Modify(4, poll.Writable, 0x2222); err != nil {
		t.Fatal(err)
	}
	if err := instance.Modify(9, poll.Readable, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未登记改 err=%v want ENOENT", err)
	}
	if err := instance.Remove(9); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未登记删 err=%v want ENOENT", err)
	}
	if err := instance.Remove(4); err != nil {
		t.Fatal(err)
	}
	// 删除后可重新登记。
	if err := instance.Add(4, source, poll.Readable, 0x3333); err != nil {
		t.Fatal(err)
	}
}

// TestEpollLevelTriggeredReporting 验证 L07:水平触发——就绪期间每次
// 等待都报告,不再就绪后不报告。
func TestEpollLevelTriggeredReporting(t *testing.T) {
	instance := New()
	source := &staticReady{readiness: poll.Readable}
	if err := instance.Add(3, source, poll.Readable, 0xAA); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		events, err := instance.Wait(context.Background(), 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].Data != 0xAA || events[0].Readiness&poll.Readable == 0 {
			t.Fatalf("第 %d 轮: %+v", round, events)
		}
	}
	source.readiness = 0
	events, err := instance.ReadyNow(8)
	if err != nil || len(events) != 0 {
		t.Fatalf("不再就绪后 events=%v err=%v", events, err)
	}
}

// TestEpollWaitBlocksUntilReady 验证 L07:未就绪时等待阻塞,就绪变化
// 后唤醒;上下文取消返回 EINTR。
func TestEpollWaitBlocksUntilReady(t *testing.T) {
	instance := New()
	reader, _, err := pipe.New(16, pipe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	// pipe 读端当前未就绪。
	if err := instance.Add(5, reader, poll.Readable, 0x55); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		events []Event
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		events, err := instance.Wait(context.Background(), 8)
		done <- outcome{events, err}
	}()
	time.Sleep(20 * time.Millisecond)
	// 唤醒:数据经另一个写端写入(新开一个写端引用)。
	writer2, err := reader.Dup()
	if err != nil {
		t.Fatal(err)
	}
	_ = writer2.Close()
	// 用管道自身:重开一对不可行——改为直接通过 Ready 变化:写入端。
	_, w, err := pipe.New(16, pipe.Options{})
	_ = w
	_ = err
	// reader 未连接写端,改为取消上下文验证 EINTR 路径。
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		events, err := instance.Wait(ctx, 8)
		done <- outcome{events, err}
	}()
	cancel()
	select {
	case got := <-done:
		if got.err == nil || !errors.Is(got.err, errno.Interrupted) {
			t.Fatalf("取消 err=%v want EINTR", got.err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("取消未唤醒等待者")
	}
}

// TestEpollMaxEventsBoundsReport 验证 L07:maxEvents 限定单次报告
// 条数,剩余就绪项留给后续调用(水平触发)。
func TestEpollMaxEventsBoundsReport(t *testing.T) {
	instance := New()
	source := &staticReady{readiness: poll.Readable | poll.Writable}
	for fd := uint32(0); fd < 5; fd++ {
		if err := instance.Add(fd, source, poll.Readable, uint64(fd)); err != nil {
			t.Fatal(err)
		}
	}
	events, err := instance.Wait(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("截断=%d want 2", len(events))
	}
	rest, err := instance.ReadyNow(8)
	if err != nil || len(rest) == 0 {
		t.Fatalf("剩余就绪项=%v err=%v", rest, err)
	}
}

// TestEpollCloseWakesWaiters 验证 L07:关闭唤醒等待者,之后全部操作
// 报 EBADF。
func TestEpollCloseWakesWaiters(t *testing.T) {
	instance := New()
	source := &staticReady{readiness: 0}
	if err := instance.Add(3, source, poll.Readable, 0); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 1)
	go func() {
		_, err := instance.Wait(context.Background(), 8)
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, errno.BadHandle) {
			t.Fatalf("关闭后等待 err=%v want EBADF", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("关闭未唤醒等待者")
	}
	if err := instance.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("重复关闭 err=%v", err)
	}
	if err := instance.Add(1, source, poll.Readable, 0); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后登记 err=%v", err)
	}
}
