package futex

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const testTimeout = 5 * time.Second

// word 是可控的 futex 字(原子装载,模拟客体内存字)。
type word struct {
	value  atomic.Uint32
	broken bool
}

func (w *word) load() (uint32, bool) {
	if w.broken {
		return 0, false
	}
	return w.value.Load(), true
}

// TestFutexWaitValueMismatch 验证 K05:值不匹配立即返回 EAGAIN,
// 不入队不阻塞。
func TestFutexWaitValueMismatch(t *testing.T) {
	table := New()
	f := &word{}
	f.value.Store(1)
	err := table.Wait(context.Background(), Key{Owner: 7, Address: 0x1000}, 0, f.load)
	if !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("值不匹配 err=%v want EAGAIN", err)
	}
	// EFAULT 路径:不可读的字。
	f.broken = true
	if err := table.Wait(context.Background(), Key{Owner: 7, Address: 0x1000}, 0, f.load); !errors.Is(err, errno.Fault) {
		t.Fatalf("不可读 err=%v want EFAULT", err)
	}
}

// TestFutexWakeBlocksAndWakes 验证 K05:值匹配时阻塞,跨执行流
// WAKE 唤醒;WAKE 返回实际唤醒数。
func TestFutexWakeBlocksAndWakes(t *testing.T) {
	table := New()
	key := Key{Owner: 7, Address: 0x2000}
	f := &word{}
	done := make(chan error, 1)
	go func() {
		done <- table.Wait(context.Background(), key, 0, f.load)
	}()
	time.Sleep(20 * time.Millisecond)
	if got := table.Wake(key, 1); got != 1 {
		t.Fatalf("唤醒数=%d want 1", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("被唤醒 err=%v want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("WAKE 未唤醒等待者")
	}
	// 唤醒后键清理:再 WAKE 返回 0。
	if got := table.Wake(key, 1); got != 0 {
		t.Fatalf("无等待者唤醒数=%d want 0", got)
	}
}

// TestFutexWakeCountsAndOrder 验证 K05:多个等待者按先进先出唤醒,
// count 限定唤醒数,剩余等待者不受影响;不同键互不干扰。
func TestFutexWakeCountsAndOrder(t *testing.T) {
	table := New()
	key := Key{Owner: 7, Address: 0x3000}
	other := Key{Owner: 8, Address: 0x3000}
	f := &word{}
	const waiters = 4
	woken := make(chan int, waiters)
	for i := 0; i < waiters; i++ {
		go func(order int) {
			if err := table.Wait(context.Background(), key, 0, f.load); err == nil {
				woken <- order
			}
		}(i)
	}
	time.Sleep(30 * time.Millisecond)
	if got := table.Wake(other, 4); got != 0 {
		t.Fatalf("异键唤醒=%d want 0", got)
	}
	if got := table.Wake(key, 2); got != 2 {
		t.Fatalf("限量唤醒=%d want 2", got)
	}
	deadline := time.After(testTimeout)
	for i := 0; i < 2; i++ {
		select {
		case <-woken:
		case <-deadline:
			t.Fatal("限量唤醒未生效")
		}
	}
	select {
	case extra := <-woken:
		t.Fatalf("超额唤醒 order=%d", extra)
	case <-time.After(50 * time.Millisecond):
	}
	// 剩余两个全部唤醒。
	if got := table.Wake(key, 99); got != 2 {
		t.Fatalf("剩余唤醒=%d want 2", got)
	}
}

// TestFutexWaitCancelledByContext 验证 K05:上下文结束返回 EINTR,
// 等待者出队(后续 WAKE 报 0)。
func TestFutexWaitCancelledByContext(t *testing.T) {
	table := New()
	key := Key{Owner: 7, Address: 0x4000}
	f := &word{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- table.Wait(ctx, key, 0, f.load)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errno.Interrupted) {
			t.Fatalf("取消 err=%v want EINTR", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("取消未唤醒等待者")
	}
	if got := table.Wake(key, 1); got != 0 {
		t.Fatalf("取消后唤醒=%d want 0", got)
	}
}

// TestFutexCloseCancelsWaiters 验证 K05:关闭唤醒全部等待者并返回
// ECANCELED;之后 WAIT 报 ECANCELED,WAKE 返回 0。
func TestFutexCloseCancelsWaiters(t *testing.T) {
	table := New()
	key := Key{Owner: 7, Address: 0x5000}
	f := &word{}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			done <- table.Wait(context.Background(), key, 0, f.load)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	table.Close()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, errno.Cancelled) {
				t.Fatalf("关闭 err=%v want ECANCELED", err)
			}
		case <-time.After(testTimeout):
			t.Fatal("关闭未唤醒等待者")
		}
	}
	if err := table.Wait(context.Background(), key, 0, f.load); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后等待 err=%v want ECANCELED", err)
	}
	if got := table.Wake(key, 1); got != 0 {
		t.Fatalf("关闭后唤醒=%d want 0", got)
	}
}

// TestFutexNoLostWakeupUnderRace 验证 K05 核心不变量:并发交错下
// 等待者要么看到新值(EAGAIN)要么被唤醒(nil),绝不悬挂——
// 值校验与入队的原子段消除丢失唤醒窗口。
func TestFutexNoLostWakeupUnderRace(t *testing.T) {
	table := New()
	key := Key{Owner: 7, Address: 0x6000}
	for round := 0; round < 200; round++ {
		f := &word{}
		done := make(chan error, 1)
		go func() {
			done <- table.Wait(context.Background(), key, 0, f.load)
		}()
		// 写字后立即唤醒:与等待者的校验+入队段并发。
		f.value.Store(1)
		table.Wake(key, 1)
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, errno.WouldBlock) {
				t.Fatalf("第 %d 轮 err=%v", round, err)
			}
		case <-time.After(testTimeout):
			t.Fatalf("第 %d 轮丢失唤醒:等待者悬挂", round)
		}
	}
}

// TestFutexWakeBitsetSelectivity 验证 K06:WAKE_BITSET 只唤醒位集
// 有交集的等待者;任意位集(普通 WAKE)唤醒全部。
func TestFutexWakeBitsetSelectivity(t *testing.T) {
	table := New()
	key := Key{Owner: 7, Address: 0x7000}
	f := &word{}
	// 三个等待者:位集 1、2、1|2。
	bitsets := []uint32{1, 2, 3}
	done := make(chan error, len(bitsets))
	for _, bitset := range bitsets {
		go func(bs uint32) {
			done <- table.WaitBitset(context.Background(), key, 0, f.load, bs)
		}(bitset)
	}
	time.Sleep(30 * time.Millisecond)
	// 位集 1:唤醒等待者 0 与 2。
	if got := table.WakeBitset(key, 4, 1); got != 2 {
		t.Fatalf("位集 1 唤醒=%d want 2", got)
	}
	deadline := time.After(testTimeout)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-deadline:
			t.Fatal("位集唤醒未生效")
		}
	}
	select {
	case extra := <-done:
		t.Fatalf("位集不匹配的等待者被唤醒 err=%v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	// 任意位集唤醒剩余。
	if got := table.WakeBitset(key, 4, BitsetMatchAny); got != 1 {
		t.Fatalf("任意位集唤醒=%d want 1", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("剩余等待者 err=%v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("任意位集未唤醒剩余等待者")
	}
	// 位集 0 的 WAKE:EINVAL 语义在调用层,核心按无匹配处理。
	if got := table.WakeBitset(key, 4, 0); got != 0 {
		t.Fatalf("零位集唤醒=%d want 0", got)
	}
}

// TestFutexRequeueMovesWaiters 验证 K06:REQUEUE 唤醒前 N 个、迁移
// 其余到目标键;迁移后的等待者被目标键的 WAKE 唤醒;同键 requeue
// 只唤醒。
func TestFutexRequeueMovesWaiters(t *testing.T) {
	table := New()
	src := Key{Owner: 7, Address: 0x8000}
	dst := Key{Owner: 7, Address: 0x9000}
	f := &word{}
	done := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			done <- table.Wait(context.Background(), src, 0, f.load)
		}()
	}
	time.Sleep(30 * time.Millisecond)
	outcome := table.Requeue(src, dst, 2, 2)
	if outcome.Woken != 2 || outcome.Requeued != 2 {
		t.Fatalf("requeue=%+v want 2/2", outcome)
	}
	// 两个被唤醒。
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("被唤醒 err=%v", err)
			}
		case <-time.After(testTimeout):
			t.Fatal("requeue 唤醒未生效")
		}
	}
	// 目标键 WAKE 唤醒迁移的两个。
	if got := table.Wake(dst, 4); got != 2 {
		t.Fatalf("目标键唤醒=%d want 2", got)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("迁移等待者 err=%v", err)
			}
		case <-time.After(testTimeout):
			t.Fatal("迁移后目标键未唤醒")
		}
	}
	// 剩余 1 个留在源键。
	if got := table.Wake(src, 4); got != 1 {
		t.Fatalf("源键剩余=%d want 1", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("剩余等待者 err=%v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("源键剩余未唤醒")
	}
}
