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

// waiter 是一个阻塞中的等待者;ch 关闭即被唤醒。
type waiter struct {
	wake chan struct{}
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
	w := &waiter{wake: make(chan struct{})}
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
	t.mu.Lock()
	if t.closed || count <= 0 {
		t.mu.Unlock()
		return 0
	}
	queue := t.keys[key]
	if queue == nil {
		t.mu.Unlock()
		return 0
	}
	if count > len(queue.waiters) {
		count = len(queue.waiters)
	}
	for i := 0; i < count; i++ {
		w := queue.waiters[0]
		queue.waiters = queue.waiters[1:]
		close(w.wake)
	}
	if len(queue.waiters) == 0 {
		delete(t.keys, key)
	}
	t.mu.Unlock()
	return count
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
