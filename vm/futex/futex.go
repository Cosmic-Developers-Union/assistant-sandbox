// Package futex 实现 Linux futex 的内核核心部分(K05):按
// {所有者,客体地址} 键控的等待队列与 WAIT/WAKE 原语。
//
// 丢失唤醒的消除依赖一个不变量:值校验(load 闭包在表锁内读取
// 客体字)与等待者入队构成一个原子段,与 WAKE 的扫描互斥。唤醒方
// 先写字后 WAKE 的序列因此必然命中两种结果之一:等待者看到新值
// 返回 EAGAIN,或入队后被 WAKE 扫到被唤醒——不存在"校验通过却
// 无人唤醒"的窗口。
package futex

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// Key 标识一个 futex 字:所有者(进程/线程组)与客体地址。
// 跨地址空间共享(共享映射上的 FUTEX_SHARED)随 E08/E09 落地,
// 当前键控即为私有语义。
type Key struct {
	Owner   uint64
	Address uint64
}

// BitsetMatchAny 与内核 FUTEX_BITSET_MATCH_ANY 一致:普通 WAIT/WAKE
// 与任意位集匹配。
const BitsetMatchAny = ^uint32(0)

// waiter 是一个阻塞中的等待者;wake 关闭即被唤醒;bitset 是
// WAIT_BITSET 登记的位集(唤醒方按位交集匹配)。
type waiter struct {
	wake   chan struct{}
	bitset uint32
}

// entry 是一个键上的等待者队列(先进先出,与 Linux 唤醒顺序一致)。
type entry struct {
	waiters []*waiter
}

// Table 是 futex 等待表:关闭后 WAIT 报 ECANCELED,WAKE 返回 0。
type Table struct {
	mu     sync.Mutex
	closed bool
	keys   map[Key]*entry
}

// New 创建一个 futex 等待表。
func New() *Table {
	return &Table{keys: map[Key]*entry{}}
}

// Wait 原子地校验并阻塞:load 在表锁内读取客体字,与入队构成
// 原子段;值不匹配返回 EWOULDBLOCK(客体 EAGAIN),读取失败由
// 调用方转换为 EFAULT。阻塞直到 WAKE 唤醒(返回 nil)或上下文
// 结束(errno.Interrupted);已被唤醒的取消仍报告成功(唤醒优先)。
func (t *Table) Wait(ctx context.Context, key Key, expected uint32, load func() (uint32, bool)) error {
	return t.WaitBitset(ctx, key, expected, load, BitsetMatchAny)
}

// WaitBitset 是带位集的 WAIT(FUTEX_WAIT_BITSET):唤醒方以
// WAKE_BITSET 的位集与登记位集按交集匹配。
func (t *Table) WaitBitset(ctx context.Context, key Key, expected uint32, load func() (uint32, bool), bitset uint32) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errno.Wrap(errno.Cancelled, "futex 表已关闭")
	}
	// 原子段:校验值并入队,与 Wake 的扫描互斥。
	value, ok := load()
	if !ok {
		t.mu.Unlock()
		return errno.Wrap(errno.Fault, "futex 字不可读")
	}
	if value != expected {
		t.mu.Unlock()
		return errno.Wrap(errno.WouldBlock, "futex 值不匹配")
	}
	w := &waiter{wake: make(chan struct{}), bitset: bitset}
	queue := t.keys[key]
	if queue == nil {
		queue = &entry{}
		t.keys[key] = queue
	}
	queue.waiters = append(queue.waiters, w)
	t.mu.Unlock()
	select {
	case <-w.wake:
		t.mu.Lock()
		closed := t.closed
		t.mu.Unlock()
		if closed {
			return errno.Wrap(errno.Cancelled, "futex 表已关闭")
		}
		return nil
	case <-ctx.Done():
		t.mu.Lock()
		removed := t.removeLocked(key, w)
		t.mu.Unlock()
		if !removed {
			// 取消竞态:已被 WAKE 取走,唤醒优先。
			<-w.wake
			return nil
		}
		return errno.Wrap(errno.Interrupted, "futex 等待被取消")
	}
}

// Wake 唤醒键上至多 count 个等待者(先进先出),返回实际唤醒数;
// 表已关闭返回 0。
func (t *Table) Wake(key Key, count int) int {
	return t.WakeBitset(key, count, BitsetMatchAny)
}

// WakeBitset 是带位集的 WAKE(FUTEX_WAKE_BITSET):只唤醒位集与
// bitset 有交集的等待者,跳过不匹配者(保持队序)。
func (t *Table) WakeBitset(key Key, count int, bitset uint32) int {
	t.mu.Lock()
	if t.closed || count <= 0 || bitset == 0 {
		t.mu.Unlock()
		return 0
	}
	queue := t.keys[key]
	if queue == nil {
		t.mu.Unlock()
		return 0
	}
	woken := 0
	remaining := queue.waiters[:0]
	for _, candidate := range queue.waiters {
		if woken < count && candidate.bitset&bitset != 0 {
			close(candidate.wake)
			woken++
			continue
		}
		remaining = append(remaining, candidate)
	}
	queue.waiters = remaining
	if len(queue.waiters) == 0 {
		delete(t.keys, key)
	}
	t.mu.Unlock()
	return woken
}

// RequeueOutcome 是一次 requeue 的结果。
type RequeueOutcome struct {
	// Woken 是唤醒的等待者数。
	Woken int
	// Requeued 是迁移到目标键的等待者数。
	Requeued int
}

// Requeue 把源键的等待队列前 wakeCount 个唤醒,其余至多 requeueCount
// 个按先进先出迁移到目标键队尾(同一表锁内原子完成,不存在等待者
// 在两键间丢失的窗口;Linux REQUEUE/CMP_REQUEUE 语义)。目标键与
// 源键相同只唤醒不迁移。
func (t *Table) Requeue(src, dst Key, wakeCount, requeueCount int) RequeueOutcome {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || (wakeCount <= 0 && requeueCount <= 0) {
		return RequeueOutcome{}
	}
	queue := t.keys[src]
	if queue == nil {
		return RequeueOutcome{}
	}
	outcome := RequeueOutcome{}
	keep := queue.waiters[:0]
	for _, candidate := range queue.waiters {
		switch {
		case outcome.Woken < wakeCount:
			close(candidate.wake)
			outcome.Woken++
		case outcome.Requeued < requeueCount && src != dst:
			target := t.keys[dst]
			if target == nil {
				target = &entry{}
				t.keys[dst] = target
			}
			target.waiters = append(target.waiters, candidate)
			outcome.Requeued++
		default:
			keep = append(keep, candidate)
		}
	}
	if len(keep) == 0 {
		delete(t.keys, src)
	} else {
		queue.waiters = keep
	}
	return outcome
}

// removeLocked 把等待者移出队列(取消路径);已被取走返回 false。
// 空队列的键随之删除,表不随等待历史增长。
func (t *Table) removeLocked(key Key, w *waiter) bool {
	queue := t.keys[key]
	if queue == nil {
		return false
	}
	for i, candidate := range queue.waiters {
		if candidate == w {
			queue.waiters = append(queue.waiters[:i], queue.waiters[i+1:]...)
			if len(queue.waiters) == 0 {
				delete(t.keys, key)
			}
			return true
		}
	}
	return false
}

// Close 关闭等待表:阻塞等待者被唤醒并以 ECANCELED 返回,后续
// WAIT 报 ECANCELED、WAKE 返回 0。
func (t *Table) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	waiters := make([]*waiter, 0)
	for _, queue := range t.keys {
		waiters = append(waiters, queue.waiters...)
	}
	t.keys = map[Key]*entry{}
	t.mu.Unlock()
	for _, w := range waiters {
		close(w.wake)
	}
}
