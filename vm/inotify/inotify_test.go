package inotify

import (
	"context"
	"encoding/binary"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// drain 读取 n 条事件并解码。
func drain(t *testing.T, fd *FD, count int) []Event {
	t.Helper()
	buf := make([]byte, 1<<20)
	n, err := fd.Read(buf)
	if err != nil {
		t.Fatalf("读取事件: %v", err)
	}
	events := decodeAll(t, buf[:n])
	if len(events) < count {
		t.Fatalf("事件数=%d want >=%d", len(events), count)
	}
	return events
}

// decodeAll 把读取缓冲按 struct inotify_event 流解码。
func decodeAll(t *testing.T, buf []byte) []Event {
	t.Helper()
	var out []Event
	for offset := 0; offset < len(buf); {
		if offset+EventSize > len(buf) {
			t.Fatalf("事件流越界: offset=%d len=%d", offset, len(buf))
		}
		wd := binary.LittleEndian.Uint32(buf[offset:])
		mask := binary.LittleEndian.Uint32(buf[offset+4:])
		cookie := binary.LittleEndian.Uint32(buf[offset+8:])
		length := binary.LittleEndian.Uint32(buf[offset+12:])
		name := ""
		if int(length) > 0 {
			raw := buf[offset+EventSize : offset+EventSize+int(length)]
			for i, b := range raw {
				if b == 0 {
					raw = raw[:i]
					break
				}
			}
			name = string(raw)
		}
		out = append(out, Event{WD: wd, Mask: mask, Cookie: cookie, Name: name})
		offset += EventSize + int(length)
	}
	return out
}

// TestAddWatchLifecycleAndMaskFlags 验证监视生命周期:登记返回递增
// wd、同路径更新(IN_MASK_ADD 叠加/默认覆盖/IN_MASK_CREATE 报
// EEXIST)、IN_ONLYDIR 非目录 ENOTDIR、不支持标志 ENOTSUP、未知位
// EINVAL、不可请求事件位 EINVAL、移除后未知 wd EINVAL。
func TestAddWatchLifecycleAndMaskFlags(t *testing.T) {
	fd := New(Options{})
	wd, err := fd.AddWatch("/work", InCreate|InDelete, 0, true)
	if err != nil || wd == 0 {
		t.Fatalf("首次登记: wd=%d err=%v", wd, err)
	}
	second, err := fd.AddWatch("/other", InModify, 0, false)
	if err != nil || second != wd+1 {
		t.Fatalf("wd 应递增: wd=%d err=%v", second, err)
	}
	// 同路径默认覆盖掩码。
	wd2, err := fd.AddWatch("/work", InModify, 0, true)
	if err != nil || wd2 != wd {
		t.Fatalf("同路径应复用 wd: wd=%d err=%v", wd2, err)
	}
	fd.Observe("/work/x", InDelete, 0, false)
	if fd.Pending() != 0 {
		t.Fatal("覆盖后 IN_DELETE 不应再命中")
	}
	fd.Observe("/work/x", InModify, 0, false)
	if fd.Pending() != 1 {
		t.Fatalf("覆盖后 IN_MODIFY 应命中: %d", fd.Pending())
	}
	// IN_MASK_ADD 叠加。
	if _, err := fd.AddWatch("/work", InDelete, InMaskAdd, true); err != nil {
		t.Fatalf("叠加登记: %v", err)
	}
	fd.Observe("/work/x", InDelete, 0, false)
	if fd.Pending() != 2 {
		t.Fatalf("叠加后 IN_DELETE 应命中: %d", fd.Pending())
	}
	// IN_MASK_CREATE 已存在 EEXIST。
	if _, err := fd.AddWatch("/work", InModify, InMaskCreate, true); !errors.Is(err, errno.Exist) {
		t.Fatalf("IN_MASK_CREATE=%v want EEXIST", err)
	}
	// IN_ONLYDIR 非目录 ENOTDIR。
	if _, err := fd.AddWatch("/file", InModify, InOnlydir, false); !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("IN_ONLYDIR=%v want ENOTDIR", err)
	}
	// 不支持标志 ENOTSUP。
	if _, err := fd.AddWatch("/work", InModify, InDontFollow, true); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("IN_DONT_FOLLOW=%v want ENOTSUP", err)
	}
	// 未知位 EINVAL。
	if _, err := fd.AddWatch("/work", InModify, 0x1, true); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知标志=%v want EINVAL", err)
	}
	// 不可请求事件位 EINVAL。
	if _, err := fd.AddWatch("/work", InQOverflow, 0, true); !errors.Is(err, errno.Invalid) {
		t.Fatalf("IN_Q_OVERFLOW 掩码=%v want EINVAL", err)
	}
	// 移除后再移除 EINVAL。
	if err := fd.RemoveWatch(second); err != nil {
		t.Fatalf("移除: %v", err)
	}
	if err := fd.RemoveWatch(second); !errors.Is(err, errno.Invalid) {
		t.Fatalf("重复移除=%v want EINVAL", err)
	}
}

// TestObserveMatchesSelfAndParent 验证事件匹配:父目录监视带条目名,
// 目标自身监视无名字;目标被删除转 DELETE_SELF 并自动移除+IN_IGNORED,
// 目标被移走转 MOVE_SELF 且监视保留。
func TestObserveMatchesSelfAndParent(t *testing.T) {
	fd := New(Options{})
	dirWD, _ := fd.AddWatch("/work", InCreate|InDelete|InModify|InMovedFrom, 0, true)
	fileWD, _ := fd.AddWatch("/work/a", InModify|InDelete|InMovedFrom, 0, false)

	fd.Observe("/work/a", InModify, 0, false)
	events := drain(t, fd, 2)
	// 目录监视与文件监视各得一条;目录监视的带条目名。
	found := map[uint32]Event{}
	for _, e := range events {
		found[e.WD] = e
	}
	if got := found[dirWD]; got.Name != "a" || got.Mask != InModify {
		t.Fatalf("目录监视事件=%+v", got)
	}
	if got := found[fileWD]; got.Name != "" || got.Mask != InModify {
		t.Fatalf("文件监视事件=%+v", got)
	}

	// 删除:文件监视得 DELETE_SELF+IN_IGNORED(自动移除),目录监视得 IN_DELETE 名 a。
	fd.Observe("/work/a", InDelete, 0, false)
	events = drain(t, fd, 3)
	// 监视遍历无固定次序,按 (wd, mask) 配对断言。
	saw := func(wd, mask uint32) Event {
		for _, e := range events {
			if e.WD == wd && e.Mask == mask {
				return e
			}
		}
		t.Fatalf("缺少事件 wd=%d mask=%#x: %+v", wd, mask, events)
		return Event{}
	}
	if e := saw(fileWD, InDeleteSelf); e.Name != "" {
		t.Fatalf("自监视 DELETE_SELF 不应带名字: %+v", e)
	}
	saw(fileWD, InIgnored)
	if e := saw(dirWD, InDelete); e.Name != "a" {
		t.Fatalf("目录监视 IN_DELETE 应带条目名: %+v", e)
	}
	// 自监视自动移除后,IN_MODIFY 仅由目录监视命中(条目名)。
	fd.Observe("/work/a", InModify, 0, false)
	events = drain(t, fd, 1)
	if events[0].WD != dirWD || events[0].Mask != InModify || events[0].Name != "a" {
		t.Fatalf("自动移除后应只剩目录监视命中: %+v", events[0])
	}

	// 移走:自监视转 MOVE_SELF 且保留,目录监视得 MOVED_FROM。
	// (前一用例的文件自监视已随 DELETE 自动移除,重新登记。)
	fileWD, _ = fd.AddWatch("/work/b", InModify|InMovedFrom, 0, false)
	fd.Observe("/work/b", InMovedFrom, 0, false)
	events = drain(t, fd, 2)
	sawMove := func(wd, mask uint32) Event {
		for _, e := range events {
			if e.WD == wd && e.Mask == mask {
				return e
			}
		}
		t.Fatalf("缺少事件 wd=%d mask=%#x: %+v", wd, mask, events)
		return Event{}
	}
	sawMove(fileWD, InMoveSelf)
	if e := sawMove(dirWD, InMovedFrom); e.Name != "b" {
		t.Fatalf("目录监视 MOVED_FROM 应带条目名: %+v", e)
	}
	fd.Observe("/work/b", InModify, 0, false)
	// MOVE_SELF 后文件自监视保留,目录监视同路径子事件也命中(共 2 条)。
	events = drain(t, fd, 2)
	if events[0].WD != fileWD && events[1].WD != fileWD {
		t.Fatalf("MOVE_SELF 后自监视应保留: %+v", events)
	}
}

// TestOneshotRemovesAfterFirstEvent 验证一次性监视消费后自动移除。
func TestOneshotRemovesAfterFirstEvent(t *testing.T) {
	fd := New(Options{})
	wd, err := fd.AddWatch("/work", InCreate, InOneshot, true)
	if err != nil {
		t.Fatal(err)
	}
	fd.Observe("/work/x", InCreate, 0, false)
	events := drain(t, fd, 1)
	if events[0].WD != wd {
		t.Fatalf("一次性事件=%+v", events[0])
	}
	fd.Observe("/work/y", InCreate, 0, false)
	if fd.Pending() != 0 {
		t.Fatal("一次性监视应已移除")
	}
}

// TestQueueOverflowKeepsSingleOverflowEvent 验证队列溢出:超限事件
// 丢弃,队尾恰一条 IN_Q_OVERFLOW(wd=-1),读取后可继续接收。
func TestQueueOverflowKeepsSingleOverflowEvent(t *testing.T) {
	fd := New(Options{})
	if _, err := fd.AddWatch("/work", InModify, 0, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < QueueLimit; i++ {
		fd.Observe("/work/a", InModify, 0, false)
	}
	if fd.Pending() != QueueLimit {
		t.Fatalf("满队列=%d want %d", fd.Pending(), QueueLimit)
	}
	fd.Observe("/work/a", InModify, 0, false)
	fd.Observe("/work/a", InModify, 0, false)
	if fd.Pending() != QueueLimit+1 {
		t.Fatalf("溢出后=%d want %d", fd.Pending(), QueueLimit+1)
	}
	// 持续溢出不堆积。
	for i := 0; i < 10; i++ {
		fd.Observe("/work/a", InModify, 0, false)
	}
	if fd.Pending() != QueueLimit+1 {
		t.Fatalf("持续溢出=%d want %d", fd.Pending(), QueueLimit+1)
	}
	buf := make([]byte, 1<<20)
	n, err := fd.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	events := decodeAll(t, buf[:n])
	if len(events) != QueueLimit+1 {
		t.Fatalf("事件数=%d", len(events))
	}
	last := events[len(events)-1]
	if last.WD != OverflowWD || last.Mask != InQOverflow {
		t.Fatalf("队尾应为溢出事件: %+v", last)
	}
	// 读取后空间释放,新事件重新入队。
	fd.Observe("/work/a", InModify, 0, false)
	if fd.Pending() != 1 {
		t.Fatalf("溢出后新事件=%d want 1", fd.Pending())
	}
}

// TestReadEncodingAndPartialFill 验证读取编码:名字段含 NUL 与 4 字
// 节对齐;缓冲区容纳不下下一条整事件时按整事件截断。
func TestReadEncodingAndPartialFill(t *testing.T) {
	fd := New(Options{})
	if _, err := fd.AddWatch("/work", InCreate|InModify, 0, true); err != nil {
		t.Fatal(err)
	}
	fd.Observe("/work/newfile", InCreate, 7, false)
	fd.Observe("/work/newfile", InModify, 0, false)

	buf := make([]byte, 128)
	n, err := fd.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	events := decodeAll(t, buf[:n])
	if len(events) != 2 {
		t.Fatalf("事件数=%d want 2", len(events))
	}
	if events[0].Mask != InCreate || events[0].Name != "newfile" || events[0].Cookie != 7 {
		t.Fatalf("创建事件=%+v", events[0])
	}
	expectedLen := (len("newfile") + 1 + 3) &^ 3
	if int(binary.LittleEndian.Uint32(buf[12:16])) != expectedLen {
		t.Fatalf("len 字段应含对齐填充")
	}
	if int(binary.LittleEndian.Uint32(buf[12:16]))+EventSize != n/2 {
		t.Fatalf("两个事件等长: n=%d", n)
	}

	// 容纳不下一条整事件:队列再入一条,第一条完整读出后截断。
	fd.Observe("/work/newfile", InModify, 0, false)
	small := make([]byte, EventSize+expectedLen)
	n, err = fd.Read(small)
	if err != nil {
		t.Fatal(err)
	}
	if n != EventSize+expectedLen {
		t.Fatalf("截断读取=%d want %d", n, EventSize+expectedLen)
	}
	// 队列空后非阻塞返回 EAGAIN。
	nonblock := New(Options{NonBlock: true})
	if _, err := nonblock.Read(make([]byte, 64)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("非阻塞空读=%v want EAGAIN", err)
	}
}

// TestBlockingReadWakesOnEvent 验证阻塞读被新事件唤醒。
func TestBlockingReadWakesOnEvent(t *testing.T) {
	fd := New(Options{})
	if _, err := fd.AddWatch("/work", InModify, 0, false); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		n, err := fd.Read(make([]byte, 128))
		if err != nil {
			n = -1
		}
		done <- n
	}()
	time.Sleep(20 * time.Millisecond)
	fd.Observe("/work/a", InModify, 0, false)
	select {
	case n := <-done:
		if n <= 0 {
			t.Fatalf("阻塞读=%d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("阻塞读未被唤醒")
	}
}

// TestCloseWakesAndRejects 验证关闭:等待者放行、读 EBADF、事件静默。
func TestCloseWakesAndRejects(t *testing.T) {
	fd := New(Options{})
	if _, err := fd.AddWatch("/work", InModify, 0, false); err != nil {
		t.Fatal(err)
	}
	var woken atomic.Bool
	go func() {
		_, _ = fd.Read(make([]byte, 128))
		woken.Store(true)
	}()
	time.Sleep(20 * time.Millisecond)
	if err := fd.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if !woken.Load() {
		t.Fatal("关闭应唤醒阻塞读")
	}
	if _, err := fd.Read(make([]byte, 64)); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后读=%v want EBADF", err)
	}
	fd.Observe("/work/a", InModify, 0, false)
	if fd.Pending() != 0 {
		t.Fatal("关闭后事件应静默丢弃")
	}
	// 重复关闭 EBADF。
	if err := fd.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("重复关闭=%v want EBADF", err)
	}
}

// TestReadyAndOnChange 验证 poll/epoll 集成:空队列不可读,入队后
// Readable 并触发 OnChange。
func TestReadyAndOnChange(t *testing.T) {
	fd := New(Options{})
	if _, err := fd.AddWatch("/work", InModify, 0, false); err != nil {
		t.Fatal(err)
	}
	if fd.Ready()&poll.Readable != 0 {
		t.Fatal("空队列不应可读")
	}
	var notified atomic.Bool
	cancel := fd.OnChange(func() { notified.Store(true) })
	defer cancel()
	fd.Observe("/work/a", InModify, 0, false)
	if fd.Ready()&poll.Readable == 0 {
		t.Fatal("入队后应可读")
	}
	if !notified.Load() {
		t.Fatal("入队应触发 OnChange")
	}
}

// TestBrokerFansOutAndPrunes 验证扇出注册表:事件送达全部实例,
// 已关闭实例被惰性剔除,cookie 递增。
func TestBrokerFansOutAndPrunes(t *testing.T) {
	broker := NewBroker()
	first, second := New(Options{}), New(Options{})
	if _, err := first.AddWatch("/work", InCreate, 0, true); err != nil {
		t.Fatal(err)
	}
	if _, err := second.AddWatch("/other", InModify, 0, false); err != nil {
		t.Fatal(err)
	}
	broker.Register(first)
	broker.Register(second)

	if !broker.Observe("/work/x", InCreate, 0, false) {
		t.Fatal("首个实例应命中")
	}
	if first.Pending() != 1 || second.Pending() != 0 {
		t.Fatalf("扇出结果: first=%d second=%d", first.Pending(), second.Pending())
	}
	cookie := broker.NextCookie()
	if cookie == 0 {
		t.Fatal("cookie 应递增")
	}
	if broker.Observe("/other", InModify, cookie, false) != true {
		t.Fatal("第二实例应命中")
	}
	// 关闭 second:惰性剔除。
	_ = second.Close()
	broker.Observe("/other", InModify, 0, false)
	if first.Pending() != 1 {
		t.Fatalf("关闭实例不应命中: first=%d", first.Pending())
	}
	_ = context.Background()
}
