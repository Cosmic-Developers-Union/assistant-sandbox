package pipe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

func TestInvalidCapacity(t *testing.T) {
	t.Parallel()
	if _, _, err := Pair(0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if _, _, err := Pair(-1); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	t.Parallel()
	r, w, err := Pair(16)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("hello")); n != 5 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	buf := make([]byte, 8)
	if n, err := r.Read(buf); n != 5 || string(buf[:n]) != "hello" || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, err := w.Write([]byte("world")); n != 5 || err != nil {
		t.Fatal(n, err)
	}

	if n, err := r.Read(buf[:3]); n != 3 || string(buf[:3]) != "wor" || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	rest := make([]byte, 8)
	if n, err := r.Read(rest); n != 2 || string(rest[:n]) != "ld" || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestEOFAfterWriterClose(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(8)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if n, err := w.Write([]byte("x")); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("write after close: n=%d err=%v", n, err)
	}

	if _, err := r.Read(make([]byte, 4)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}

	if _, err := r.Read(make([]byte, 4)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestBrokenPipeWhenReadersGone(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(8)
	var broken atomic.Int32
	r.OnBroken(func() { broken.Add(1) })
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, errno.BrokenPipe) {
		t.Fatalf("err=%v", err)
	}
	if broken.Load() != 1 {
		t.Fatalf("broken=%d", broken.Load())
	}

	if err := r.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("err=%v", err)
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, errno.BrokenPipe) {
		t.Fatal(err)
	}
	if broken.Load() != 1 {
		t.Fatalf("broken fired twice: %d", broken.Load())
	}
}

func TestDupSharesStateAndCloseIsRefCounted(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(8)
	dup, err := w.Dup()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dup.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}

	if got := r.Buffered(); got != 3 {
		t.Fatalf("buffered=%d", got)
	}

	if err := dup.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("de")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if n, err := r.Read(buf); n != 5 || string(buf[:n]) != "abcde" || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}

	if err := w.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
}

func TestReaderBlocksUntilData(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2)
		n, err := r.Read(buf)
		if n != 2 || string(buf) != "ok" || err != nil {
			t.Errorf("n=%d err=%v", n, err)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("读不应在数据到达前返回")
	default:
	}
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("数据到达后读未返回")
	}
}

func TestWriterBlocksOnFullBufferUntilDrained(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(4)
	if n, err := w.Write([]byte("abcd")); n != 4 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	done := make(chan error, 1)
	var written int
	go func() {
		var err error
		written, err = w.Write([]byte("efghij"))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("写不应在缓冲排空前返回")
	default:
	}
	buf := make([]byte, 4)
	if n, _ := r.Read(buf); n != 4 || string(buf) != "abcd" {
		t.Fatalf("n=%d buf=%q", n, buf)
	}

	if n, _ := r.Read(buf); n != 4 || string(buf) != "efgh" {
		t.Fatalf("n=%d buf=%q", n, buf)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("写入未完成")
	}
	if written != 6 {
		t.Fatalf("written=%d", written)
	}
	if n, err := r.Read(buf); n != 2 || string(buf[:n]) != "ij" || err != nil {
		t.Fatalf("n=%d buf=%q err=%v", n, buf[:n], err)
	}
}

func TestCancelUnblocksReader(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(8)
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := r.ReadContext(ctx, make([]byte, 4)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		if _, err := w.Write([]byte("y")); err != nil {
			t.Error(err)
		}
	}()
	if n, err := r.Read(make([]byte, 4)); n != 1 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestPollReadinessAndTimeout(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(4)

	if ready := r.Ready(); ready&poll.Readable != 0 {
		t.Fatalf("empty readable: %v", ready)
	}
	if ready := w.Ready(); ready&poll.Writable == 0 {
		t.Fatalf("not writable: %v", ready)
	}
	if _, err := w.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}

	if ready := w.Ready(); ready&poll.Writable == 0 {
		t.Fatalf("not writable: %v", ready)
	}
	if _, err := w.Write([]byte("cd")); err != nil {
		t.Fatal(err)
	}

	if ready := w.Ready(); ready&poll.Writable != 0 {
		t.Fatalf("full writable: %v", ready)
	}
	if ready := r.Ready(); ready&poll.Readable == 0 {
		t.Fatalf("not readable: %v", ready)
	}

	var clk clock.Virtual
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = clk.Advance(60)
		time.Sleep(10 * time.Millisecond)
		_ = clk.Advance(60)
	}()
	if _, err := poll.WaitFor(ctx, &clk, []poll.Entry{{Source: w, Want: poll.Writable}}, 100); !errors.Is(err, errno.TimedOut) {
		t.Fatalf("expect timeout, got %v", err)
	}
	if _, err := r.Read(make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	results, err := poll.WaitFor(ctx, &clk, []poll.Entry{{Source: w, Want: poll.Writable}}, 100)
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%v err=%v", results, err)
	}
	cancel()
}

func TestPollWakesOnChange(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(4)
	results := make(chan []poll.Result, 1)
	go func() {
		res, err := poll.Wait(context.Background(), []poll.Entry{{Source: r, Want: poll.Readable}})
		if err != nil {
			t.Error(err)
		}
		results <- res
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := w.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-results:
		if len(res) != 1 || res[0].Index != 0 {
			t.Fatalf("res=%v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("状态变化未唤醒 poll")
	}
}

func TestReaderCloseWakesBlockedWriter(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(2)
	if _, err := w.Write([]byte("aa")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.Write([]byte("bb"))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errno.BrokenPipe) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("阻塞的写未被唤醒")
	}
}

func TestZeroLengthOperations(t *testing.T) {
	t.Parallel()
	r, w, _ := Pair(4)
	if n, err := r.Read(nil); n != 0 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// TestNonBlockingReadReturnsEAGAIN 验证 I15:空缓冲非阻塞读返回
// EWOULDBLOCK,不等待;有数据后正常读取。
func TestNonBlockingReadReturnsEAGAIN(t *testing.T) {
	r, w, err := New(64, Options{NonBlock: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if _, err := r.Read(make([]byte, 8)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("空读 err=%v want EWOULDBLOCK", err)
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	n, err := r.Read(make([]byte, 8))
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v want 3", n, err)
	}
}

// TestNonBlockingWriteFullReturnsEAGAIN 验证 I15:缓冲已满时非阻塞写
// 返回 EWOULDBLOCK 且不产生部分写入。
func TestNonBlockingWriteFullReturnsEAGAIN(t *testing.T) {
	r, w, err := New(4, Options{NonBlock: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	// 剩余空间 0:≤PIPE_BUF 的原子写整体拒绝。
	if _, err := w.Write([]byte("xy")); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("满写 err=%v want EWOULDBLOCK", err)
	}
	if r.Buffered() != 4 {
		t.Fatalf("缓冲被部分写入污染: %d", r.Buffered())
	}
}

// TestSmallWriteIsAtomicUnderPressure 验证 I15:容量不足以完整容纳
// ≤PIPE_BUF 的请求时阻塞等待而非部分写入;腾出空间后完整出现。
func TestSmallWriteIsAtomicUnderPressure(t *testing.T) {
	r, w, err := New(8, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if _, err := w.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.Write([]byte("abcdef"))
		done <- err
	}()
	// 等待写者阻塞在等待队列上(未产生部分写入)。
	deadline := time.Now().Add(2 * time.Second)
	for w.Buffered() != 8 {
		if time.Now().After(deadline) {
			t.Fatal("原子写未阻塞")
		}
		time.Sleep(time.Millisecond)
	}
	got := make([]byte, 8)
	if _, err := r.Read(got); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r.Buffered() != 6 {
		t.Fatalf("原子写被部分写入: 缓冲=%d want 8", r.Buffered())
	}
}

// TestLargeWriteSplitsWhenAtomicImpossible 验证 I15:请求超过总容量时
// 回退分块模式,避免永不满足的原子等待。
func TestLargeWriteSplitsWhenAtomicImpossible(t *testing.T) {
	r, w, err := New(4, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	payload := []byte("0123456789abcdef")
	got := make([]byte, len(payload))
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(r, got)
		readDone <- err
	}()
	n, err := w.Write(payload)
	if err != nil || n != len(payload) {
		t.Fatalf("n=%d err=%v want %d", n, err, len(payload))
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("数据不一致: %q", got)
	}
}

// TestDirectionMismatchesReturnEBADF 验证 I15:读端写、写端读报 EBADF。
func TestDirectionMismatchesReturnEBADF(t *testing.T) {
	r, w, err := New(8, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if _, err := r.Write([]byte("x")); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("读端写 err=%v want EBADF", err)
	}
	if _, err := w.Read(make([]byte, 1)); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("写端读 err=%v want EBADF", err)
	}
}

// TestStatusFlagsRoundTrip 验证 I15:F_GETFL/F_SETFL 的 O_NONBLOCK 切换
// 与访问模式报告;切换唤醒阻塞的读/写者按新模式重试。
func TestStatusFlagsRoundTrip(t *testing.T) {
	r, w, err := New(8, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	flags, err := r.StatusFlags()
	if err != nil || flags != 0 {
		t.Fatalf("读端初始 flags=%#x err=%v want O_RDONLY(0)", flags, err)
	}
	flags, err = w.StatusFlags()
	if err != nil || flags != 1 {
		t.Fatalf("写端初始 flags=%#x err=%v want O_WRONLY(1)", flags, err)
	}
	if err := r.SetStatusFlags(0o4000); err != nil {
		t.Fatal(err)
	}
	if flags, _ := r.StatusFlags(); flags != 0o4000 {
		t.Fatalf("切换后 flags=%#x want O_NONBLOCK", flags)
	}
	// 阻塞中的读者在切换后被唤醒并按 EWOULDBLOCK 返回。
	results := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 4))
		results <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(results) == 0 {
		_ = r.SetStatusFlags(0o4000)
		if time.Now().After(deadline) {
			t.Fatal("切换 O_NONBLOCK 未唤醒阻塞读")
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-results; !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("唤醒后 err=%v want EWOULDBLOCK", err)
	}
	// 清除非阻塞后恢复阻塞语义。
	if err := r.SetStatusFlags(0); err != nil {
		t.Fatal(err)
	}
	if flags, _ := r.StatusFlags(); flags != 0 {
		t.Fatalf("清除后 flags=%#x", flags)
	}
}
