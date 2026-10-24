package eventfd

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

func encode(value uint64) []byte {
	var raw [8]byte
	binary.LittleEndian.PutUint64(raw[:], value)
	return raw[:]
}

func TestCounterRoundTripAndDrain(t *testing.T) {
	e, err := New(7, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.Write(encode(5)); err != nil || n != 8 {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
	var raw [8]byte
	if n, err := e.Read(raw[:]); err != nil || n != 8 {
		t.Fatalf("read: n=%d err=%v", n, err)
	}
	if got := binary.LittleEndian.Uint64(raw[:]); got != 12 {
		t.Fatalf("计数应累加 7+5=%d, got %d", 12, got)
	}
	// 计数清零后对象回到不可读状态(非阻塞探测,避免无限阻塞)。
	e.nonblock = true
	if _, err := e.Read(raw[:]); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("读后清零,再次读应返回 EAGAIN: %v", err)
	}
}

func TestSemaphoreModeReturnsOne(t *testing.T) {
	e, err := New(3, Options{Semaphore: true})
	if err != nil {
		t.Fatal(err)
	}
	for want := range uint64(3) {
		var raw [8]byte
		if _, err := e.Read(raw[:]); err != nil {
			t.Fatal(err)
		}
		if got := binary.LittleEndian.Uint64(raw[:]); got != 1 {
			t.Fatalf("信号量模式应每次返回 1, got %d", got)
		}
		_ = want
	}
}

func TestNonBlockReturnsWouldBlock(t *testing.T) {
	e, err := New(0, Options{NonBlock: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Read(make([]byte, 8)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("空计数非阻塞读应返回 EAGAIN: %v", err)
	}
	if _, err := e.Write(encode(ValueUpperLimit)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Write(encode(1)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("计满非阻塞写应返回 EAGAIN: %v", err)
	}
	if _, err := e.Write(encode(ValueUpperLimit + 1)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("单次写入超上限应返回 EINVAL: %v", err)
	}
}

func TestBufferSizeValidation(t *testing.T) {
	e, err := New(1, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Read(make([]byte, 4)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("小于 8 字节的读应返回 EINVAL: %v", err)
	}
	if _, err := e.Write(make([]byte, 4)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非 8 字节的写应返回 EINVAL: %v", err)
	}
	if n, err := e.Read(nil); err != nil || n != 0 {
		t.Fatalf("零长度读应返回 0: n=%d err=%v", n, err)
	}
}

func TestBlockingReadWakesOnWrite(t *testing.T) {
	e, err := New(0, Options{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var raw [8]byte
		if _, err := e.Read(raw[:]); err != nil {
			t.Errorf("阻塞读被唤醒后不应报错: %v", err)
			return
		}
		if got := binary.LittleEndian.Uint64(raw[:]); got != 42 {
			t.Errorf("读到的计数=%d want 42", got)
		}
	}()
	deadline := time.After(time.Second)
	select {
	case <-done:
		t.Fatal("无写入时读不应返回")
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := e.Write(encode(42)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-deadline:
		t.Fatal("写入后阻塞读未被唤醒")
	}
}

func TestCloseWakesWaitersAndRejectsReuse(t *testing.T) {
	e, err := New(0, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := e.Read(make([]byte, 8)); !errors.Is(err, errno.Interrupted) {
			t.Errorf("关闭后阻塞读应返回中断: %v", err)
		}
	}()
	if _, err := e.Write(encode(1)); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := e.Write(encode(1)); !errors.Is(err, errno.Interrupted) {
		t.Fatalf("关闭后写应返回中断: %v", err)
	}
	if err := e.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("重复关闭应返回 EBADF: %v", err)
	}
}

func TestContextCancelWakesBlockedRead(t *testing.T) {
	e, err := New(0, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = context.Background()
	// Read 使用内部后台上下文,仅由 Close 唤醒;此处验证阻塞读可被并发 Close 解除。
	go func() {
		if _, err := e.Read(make([]byte, 8)); !errors.Is(err, errno.Interrupted) {
			t.Errorf("阻塞读应被 Close 解除: %v", err)
		}
	}()
	if _, err := e.Write(encode(1)); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessAndChangeNotification(t *testing.T) {
	e, err := New(0, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ready := e.Ready(); ready&poll.Readable != 0 || ready&poll.Writable == 0 {
		t.Fatalf("空计数应只可写: %v", ready)
	}
	notified := make(chan struct{}, 1)
	cancel := e.OnChange(func() {
		notified <- struct{}{}
	})
	defer cancel()
	if _, err := e.Write(encode(1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	default:
		t.Fatal("写入应触发变更通知")
	}
	if ready := e.Ready(); ready&poll.Readable == 0 || ready&poll.Writable == 0 {
		t.Fatalf("非空计数应既可读又可写: %v", ready)
	}
	if _, err := e.Read(make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	if ready := e.Ready(); ready&poll.Readable != 0 {
		t.Fatalf("清零后应不可读: %v", ready)
	}
}

func TestInitialValueValidation(t *testing.T) {
	if _, err := New(ValueUpperLimit, Options{}); err != nil {
		t.Fatalf("上限初值应合法: %v", err)
	}
	if _, err := New(ValueUpperLimit+1, Options{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("超限初值应返回 EINVAL: %v", err)
	}
}
