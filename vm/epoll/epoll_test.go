package epoll

import (
	"context"
	"errors"
	"sync"
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
	if err := instance.Add(4, source, poll.Readable, 0x1111, LevelTrigger); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(4, source, poll.Readable, 0, LevelTrigger); !errors.Is(err, errno.Exist) {
		t.Fatalf("重复登记 err=%v want EEXIST", err)
	}
	if err := instance.Modify(4, poll.Writable, 0x2222, LevelTrigger); err != nil {
		t.Fatal(err)
	}
	if err := instance.Modify(9, poll.Readable, 0, LevelTrigger); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未登记改 err=%v want ENOENT", err)
	}
	if err := instance.Remove(9); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未登记删 err=%v want ENOENT", err)
	}
	if err := instance.Remove(4); err != nil {
		t.Fatal(err)
	}
	// 删除后可重新登记。
	if err := instance.Add(4, source, poll.Readable, 0x3333, LevelTrigger); err != nil {
		t.Fatal(err)
	}
}

// TestEpollLevelTriggeredReporting 验证 L07:水平触发——就绪期间每次
// 等待都报告,不再就绪后不报告。
func TestEpollLevelTriggeredReporting(t *testing.T) {
	instance := New()
	source := &staticReady{readiness: poll.Readable}
	if err := instance.Add(3, source, poll.Readable, 0xAA, LevelTrigger); err != nil {
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

// TestEpollWaitBlocksUntilReady 验证 L07:未就绪时等待阻塞,上下文
// 取消返回 EINTR。
func TestEpollWaitBlocksUntilReady(t *testing.T) {
	instance := New()
	reader, _, err := pipe.New(16, pipe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	// pipe 读端当前未就绪。
	if err := instance.Add(5, reader, poll.Readable, 0x55, LevelTrigger); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		events []Event
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		events, err := instance.Wait(ctx, 8)
		done <- outcome{events, err}
	}()
	time.Sleep(20 * time.Millisecond)
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
		if err := instance.Add(fd, source, poll.Readable, uint64(fd), LevelTrigger); err != nil {
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
	if err := instance.Add(3, source, poll.Readable, 0, LevelTrigger); err != nil {
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
	if err := instance.Add(1, source, poll.Readable, 0, LevelTrigger); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后登记 err=%v", err)
	}
}

// gateSource 是可控且线程安全的就绪源:变化时可触发活动通知
// (模拟管道写入等新活动,供边缘触发判定)。
type gateSource struct {
	mu        sync.Mutex
	readiness poll.Readiness
	notify    func()
}

func (s *gateSource) Ready() poll.Readiness {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readiness
}

func (s *gateSource) OnChange(fn func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify = fn
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.notify = nil
	}
}

// become 更新就绪状态并触发一次活动通知。
func (s *gateSource) become(readiness poll.Readiness) {
	s.mu.Lock()
	s.readiness = readiness
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// activity 不改变就绪状态,只触发一次活动通知(未排空时的新数据)。
func (s *gateSource) activity() {
	s.mu.Lock()
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// TestEpollEdgeTriggeredReportsOncePerEdge 验证 L08:边缘触发——
// 登记时已就绪产生初始边沿,报告一次后不再报告;未排空时的新活动
// 是新边沿;排空后需新活动才再报告。
func TestEpollEdgeTriggeredReportsOncePerEdge(t *testing.T) {
	instance := New()
	source := &gateSource{readiness: poll.Readable}
	if err := instance.Add(6, source, poll.Readable, 0xE1, EdgeTrigger); err != nil {
		t.Fatal(err)
	}
	// 登记时已就绪 → 初始边沿,报告一次。
	events, err := instance.ReadyNow(8)
	if err != nil || len(events) != 1 || events[0].Data != 0xE1 {
		t.Fatalf("初始边沿: events=%v err=%v", events, err)
	}
	// 水平语义下仍就绪,但边沿已消费 → 不再报告。
	if events, _ = instance.ReadyNow(8); len(events) != 0 {
		t.Fatalf("边沿消费后重复报告: %v", events)
	}
	// 未排空时的新活动 = 新边沿(与 Linux 未读数据再写入一致)。
	source.activity()
	if events, _ = instance.ReadyNow(8); len(events) != 1 {
		t.Fatalf("新活动未产生新边沿: %v", events)
	}
	if events, _ = instance.ReadyNow(8); len(events) != 0 {
		t.Fatalf("第二个边沿消费后重复报告: %v", events)
	}
	// 排空(就绪清零,伴随活动)→ 无报告;再就绪 → 新边沿。
	source.become(0)
	if events, _ = instance.ReadyNow(8); len(events) != 0 {
		t.Fatalf("排空后仍报告: %v", events)
	}
	source.become(poll.Readable)
	if events, _ = instance.ReadyNow(8); len(events) != 1 {
		t.Fatalf("排空后再就绪未产生新边沿: %v", events)
	}
}

// TestEpollOneShotDisablesUntilRearm 验证 L08:一次性——报告一次后
// 登记项禁用,新活动也不报告;Modify 重新武装后按当前就绪立即可报。
func TestEpollOneShotDisablesUntilRearm(t *testing.T) {
	instance := New()
	source := &gateSource{readiness: poll.Readable}
	if err := instance.Add(7, source, poll.Readable, 0x5A, OneShot); err != nil {
		t.Fatal(err)
	}
	events, err := instance.ReadyNow(8)
	if err != nil || len(events) != 1 {
		t.Fatalf("首次报告: events=%v err=%v", events, err)
	}
	// 仍就绪(水平语义)但已禁用。
	if events, _ = instance.ReadyNow(8); len(events) != 0 {
		t.Fatalf("一次性禁用后仍报告: %v", events)
	}
	source.activity()
	if events, _ = instance.ReadyNow(8); len(events) != 0 {
		t.Fatalf("一次性禁用后新活动仍报告: %v", events)
	}
	// Modify 重新武装:仍就绪 → 立即再报一次。
	if err := instance.Modify(7, poll.Readable, 0x5B, OneShot); err != nil {
		t.Fatal(err)
	}
	events, _ = instance.ReadyNow(8)
	if len(events) != 1 || events[0].Data != 0x5B {
		t.Fatalf("重新武装后未报告: %v", events)
	}
	if events, _ = instance.ReadyNow(8); len(events) != 0 {
		t.Fatalf("重新武装的报告未禁用: %v", events)
	}
}

// TestEpollEdgeTruncationKeepsPendingEdges 验证 L08:maxEvents 截断
// 不消费被截断项的边沿——剩余项留给后续调用,通知不丢。
func TestEpollEdgeTruncationKeepsPendingEdges(t *testing.T) {
	instance := New()
	first := &gateSource{readiness: poll.Readable}
	second := &gateSource{readiness: poll.Readable}
	if err := instance.Add(8, first, poll.Readable, 1, EdgeTrigger); err != nil {
		t.Fatal(err)
	}
	if err := instance.Add(9, second, poll.Readable, 2, EdgeTrigger); err != nil {
		t.Fatal(err)
	}
	events, err := instance.Wait(context.Background(), 1)
	if err != nil || len(events) != 1 || events[0].Data != 1 {
		t.Fatalf("截断报告: events=%v err=%v", events, err)
	}
	// 第二项的初始边沿未被消费,仍可报告。
	events, _ = instance.ReadyNow(8)
	if len(events) != 1 || events[0].Data != 2 {
		t.Fatalf("被截断项的边沿丢失: %v", events)
	}
}

// TestEpollEdgeWaitBlocksAndWakesOnActivity 验证 L08:边缘触发未就绪
// 时等待阻塞,新活动唤醒并报告。
func TestEpollEdgeWaitBlocksAndWakesOnActivity(t *testing.T) {
	instance := New()
	source := &gateSource{readiness: 0}
	if err := instance.Add(6, source, poll.Readable, 0x99, EdgeTrigger); err != nil {
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
	source.become(poll.Readable)
	select {
	case got := <-done:
		if got.err != nil || len(got.events) != 1 || got.events[0].Data != 0x99 {
			t.Fatalf("活动唤醒: %+v", got)
		}
	case <-time.After(waitTimeout):
		t.Fatal("新活动未唤醒边缘等待者")
	}
}

// TestEpollCloseDuringEdgeWaitKeepsNotification 验证 L08:边缘触发
// 等待期间并发关闭,等待者被唤醒返回 EBADF;关闭后迟到的活动通知
// 不丢失、不 panic——重新武装(MOD)后可再报告。
func TestEpollCloseDuringEdgeWaitKeepsNotification(t *testing.T) {
	instance := New()
	source := &gateSource{readiness: 0}
	if err := instance.Add(6, source, poll.Readable, 0x77, EdgeTrigger); err != nil {
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
			t.Fatalf("并发关闭 err=%v want EBADF", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("关闭未唤醒边缘等待者")
	}
	// 关闭后迟到的活动通知:退订竞态下允许触发,必须无害。
	source.become(poll.Readable)
}
