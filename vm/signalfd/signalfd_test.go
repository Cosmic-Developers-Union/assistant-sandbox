package signalfd

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

const waitTimeout = 5 * time.Second

// newFD 创建一个只阻塞 SIGUSR1/SIGUSR2 的 signalfd。
func newFD(t *testing.T, options Options) *FD {
	t.Helper()
	f, err := New(uint64(1)<<9|uint64(1)<<10, options)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// waitWaiter 等待读操作真正阻塞在等待队列上。
func waitWaiter(t *testing.T, f *FD) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for f.wake.Waiters() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("读取未进入等待状态")
		}
		time.Sleep(time.Millisecond)
	}
}

// decode 解析第 index 条记录的关键字段。
func decode(t *testing.T, buf []byte, index int) (signo, code, pid, uid uint32) {
	t.Helper()
	base := index * RecordSize
	if len(buf) < base+RecordSize {
		t.Fatalf("缓冲区不足: len=%d base=%d", len(buf), base)
	}
	return binary.LittleEndian.Uint32(buf[base+0 : base+4]),
		binary.LittleEndian.Uint32(buf[base+8 : base+12]),
		binary.LittleEndian.Uint32(buf[base+16 : base+20]),
		binary.LittleEndian.Uint32(buf[base+20 : base+24])
}

func TestNewRejectsEmptyMask(t *testing.T) {
	t.Parallel()
	if _, err := New(0, Options{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空掩码应报 EINVAL: %v", err)
	}
}

func TestNewKeepsMaskAndOptions(t *testing.T) {
	t.Parallel()
	f, err := New(uint64(1)<<9|uint64(1)<<10, Options{NonBlock: true})
	if err != nil {
		t.Fatal(err)
	}
	if f.Mask() != uint64(1)<<9|uint64(1)<<10 {
		t.Fatalf("mask=%x", f.Mask())
	}
	if !f.Masked(10) || !f.Masked(11) || f.Masked(12) {
		t.Fatal("掩码判定错误")
	}
	if f.Masked(0) || f.Masked(MaxSignal+1) {
		t.Fatal("越界信号号不得被屏蔽")
	}
}

func TestParseSigset(t *testing.T) {
	t.Parallel()
	raw := make([]byte, SigsetSize)
	binary.LittleEndian.PutUint64(raw, uint64(1)<<9)
	mask, err := ParseSigset(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mask != uint64(1)<<9 {
		t.Fatalf("mask=%x", mask)
	}
	if _, err := ParseSigset(raw[:4]); !errors.Is(err, errno.Invalid) {
		t.Fatalf("长度错误应报 EINVAL: %v", err)
	}
}

func TestDeliverMaskedSignalIsReadable(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	sig := Signal{Signo: 10, PID: 4242, UID: 1000, Code: CodeUser}
	if err := f.Deliver(sig); err != nil {
		t.Fatal(err)
	}
	if f.Pending() != 1 {
		t.Fatalf("pending=%d", f.Pending())
	}
	buf := make([]byte, RecordSize)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != RecordSize {
		t.Fatalf("n=%d", n)
	}
	signo, code, pid, uid := decode(t, buf, 0)
	if signo != 10 || code != CodeUser || pid != 4242 || uid != 1000 {
		t.Fatalf("signo=%d code=%d pid=%d uid=%d", signo, code, pid, uid)
	}
	if f.Pending() != 0 {
		t.Fatalf("读取后队列未清空: %d", f.Pending())
	}
}

func TestRecordLayoutMatchesLinuxABI(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	if err := f.Deliver(Signal{Signo: 11, PID: 7, UID: 9, Code: CodeKernel}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, RecordSize)
	if _, err := f.Read(buf); err != nil {
		t.Fatal(err)
	}
	expected := make([]byte, RecordSize)
	binary.LittleEndian.PutUint32(expected[0:4], 11)
	binary.LittleEndian.PutUint32(expected[4:8], 0)
	binary.LittleEndian.PutUint32(expected[8:12], CodeKernel)
	binary.LittleEndian.PutUint32(expected[12:16], 0)
	binary.LittleEndian.PutUint32(expected[16:20], 7)
	binary.LittleEndian.PutUint32(expected[20:24], 9)
	binary.LittleEndian.PutUint32(expected[24:28], 0)
	binary.LittleEndian.PutUint32(expected[28:32], 0)
	if !bytes.Equal(buf, expected) {
		t.Fatalf("记录布局不符:\n got=%x\nwant=%x", buf, expected)
	}
}

func TestDeliverRejectsSignalOutsideMask(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	err := f.Deliver(Signal{Signo: 12, PID: 1, UID: 0, Code: CodeUser})
	if !errors.Is(err, errno.NotPermitted) {
		t.Fatalf("掩码外信号应报 EPERM: %v", err)
	}
	if f.Pending() != 0 {
		t.Fatalf("队列不应变化: %d", f.Pending())
	}
}

func TestDeliverRejectsInvalidSignalNumber(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	if err := f.Deliver(Signal{Signo: 0}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("信号号 0 应报 EINVAL: %v", err)
	}
	if err := f.Deliver(Signal{Signo: MaxSignal + 1}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("越界信号号应报 EINVAL: %v", err)
	}
}

func TestReadReturnsMultipleRecordsInQueueOrder(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	for _, sig := range []Signal{
		{Signo: 10, PID: 1, UID: 100, Code: CodeUser},
		{Signo: 11, PID: 2, UID: 200, Code: CodeUser},
		{Signo: 10, PID: 3, UID: 300, Code: CodeQueue},
	} {
		if err := f.Deliver(sig); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, 3*RecordSize)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3*RecordSize {
		t.Fatalf("n=%d", n)
	}
	want := []Signal{
		{Signo: 10, PID: 1, UID: 100, Code: CodeUser},
		{Signo: 11, PID: 2, UID: 200, Code: CodeUser},
		{Signo: 10, PID: 3, UID: 300, Code: CodeQueue},
	}
	for index, expect := range want {
		signo, code, pid, uid := decode(t, buf, index)
		if signo != expect.Signo || code != expect.Code || pid != expect.PID || uid != expect.UID {
			t.Fatalf("第 %d 条: signo=%d code=%d pid=%d uid=%d", index, signo, code, pid, uid)
		}
	}
	if f.Pending() != 0 {
		t.Fatalf("队列未清空: %d", f.Pending())
	}
}

func TestReadKeepsSurplusRecordsForNextRead(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	for _, signo := range []uint32{10, 11, 10} {
		if err := f.Deliver(Signal{Signo: signo}); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, 2*RecordSize)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2*RecordSize {
		t.Fatalf("n=%d", n)
	}
	if f.Pending() != 1 {
		t.Fatalf("pending=%d", f.Pending())
	}
	buf = make([]byte, RecordSize)
	if n, err = f.Read(buf); err != nil || n != RecordSize {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if signo, _, _, _ := decode(t, buf, 0); signo != 10 {
		t.Fatalf("signo=%d", signo)
	}
}

func TestReadRejectsBufferNotMultipleOfRecordSize(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	if err := f.Deliver(Signal{Signo: 10}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, RecordSize+1)
	if _, err := f.Read(buf); !errors.Is(err, errno.Invalid) {
		t.Fatalf("长度不整除应报 EINVAL: %v", err)
	}
	if f.Pending() != 1 {
		t.Fatalf("非法长度不得消费队列: %d", f.Pending())
	}
}

func TestReadWithEmptyBufferReturnsZero(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	n, err := f.Read(nil)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	buf := make([]byte, 0)
	if n, err = f.Read(buf); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestReadEmptyQueueNonBlockingReturnsWouldBlock(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{NonBlock: true})
	buf := make([]byte, RecordSize)
	if _, err := f.Read(buf); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("空队列非阻塞读应报 EWOULDBLOCK: %v", err)
	}
}

func TestBlockingReadWakesOnDeliver(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	type result struct {
		n   int
		err error
		buf []byte
	}
	results := make(chan result, 1)
	go func() {
		buf := make([]byte, RecordSize)
		n, err := f.Read(buf)
		results <- result{n: n, err: err, buf: buf}
	}()
	waitWaiter(t, f)
	if err := f.Deliver(Signal{Signo: 10, PID: 55, UID: 66, Code: CodeUser}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-results:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.n != RecordSize {
			t.Fatalf("n=%d", got.n)
		}
		signo, _, pid, uid := decode(t, got.buf, 0)
		if signo != 10 || pid != 55 || uid != 66 {
			t.Fatalf("signo=%d pid=%d uid=%d", signo, pid, uid)
		}
	case <-time.After(waitTimeout):
		t.Fatal("阻塞读未被唤醒")
	}
}

func TestBlockingReadContextCancelReturnsInterrupted(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	ctx, cancel := context.WithCancel(t.Context())
	errs := make(chan error, 1)
	go func() {
		buf := make([]byte, RecordSize)
		_, err := f.ReadContext(ctx, buf)
		errs <- err
	}()
	waitWaiter(t, f)
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, errno.Interrupted) {
			t.Fatalf("取消后应报 EINTR: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("取消未唤醒等待者")
	}
}

func TestReadyReflectsQueueState(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	ready := f.Ready()
	if ready&poll.Readable != 0 {
		t.Fatalf("空队列不得可读: %v", ready)
	}
	if ready&poll.Writable == 0 {
		t.Fatalf("未关闭时应当可写: %v", ready)
	}
	if err := f.Deliver(Signal{Signo: 10}); err != nil {
		t.Fatal(err)
	}
	ready = f.Ready()
	if ready&poll.Readable == 0 {
		t.Fatalf("入队后必须可读: %v", ready)
	}
	if ready&poll.Writable == 0 {
		t.Fatalf("未关闭时应当可写: %v", ready)
	}
	buf := make([]byte, RecordSize)
	if _, err := f.Read(buf); err != nil {
		t.Fatal(err)
	}
	if ready = f.Ready(); ready&poll.Readable != 0 {
		t.Fatalf("读空后不得可读: %v", ready)
	}
}

func TestOnChangeNotifiesOnDeliver(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	notified := make(chan struct{}, 4)
	cancel := f.OnChange(func() { notified <- struct{}{} })
	defer cancel()
	if err := f.Deliver(Signal{Signo: 10}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	case <-time.After(waitTimeout):
		t.Fatal("入队未触发回调")
	}
}

func TestOnChangeCancelStopsNotifications(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	notified := make(chan struct{}, 4)
	cancel := f.OnChange(func() { notified <- struct{}{} })
	cancel()
	if err := f.Deliver(Signal{Signo: 10}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
		t.Fatal("取消后不得再触发回调")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCloseWakesBlockedReaderAndFailsRead(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	errs := make(chan error, 1)
	go func() {
		buf := make([]byte, RecordSize)
		_, err := f.Read(buf)
		errs <- err
	}()
	waitWaiter(t, f)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, errno.BadHandle) {
			t.Fatalf("关闭后读取应报 EBADF: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("关闭未唤醒等待者")
	}
	buf := make([]byte, RecordSize)
	if _, err := f.Read(buf); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后读取应报 EBADF: %v", err)
	}
	if err := f.Deliver(Signal{Signo: 10}); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后投递应报 EBADF: %v", err)
	}
	if err := f.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("重复关闭应报 EBADF: %v", err)
	}
	if ready := f.Ready(); ready&poll.Writable != 0 || ready&poll.Readable != 0 {
		t.Fatalf("关闭后不得就绪: %v", ready)
	}
}

func TestDeliverAfterCloseReturnsBadHandle(t *testing.T) {
	t.Parallel()
	f := newFD(t, Options{})
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Deliver(Signal{Signo: 10}); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后投递应报 EBADF: %v", err)
	}
}
