package vm

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// runQueue 是 H02 的可运行队列:以执行权令牌实现单核语义——同一
// 时刻至多一个进程执行客体指令。时间片用尽或进入(可能阻塞的)
// 系统调用时让出令牌排到队尾;驻停与退出归还令牌;阻塞在等待
// 队列/睡眠中的进程不持有令牌,不占用其他进程的执行时间片。
// 没有其他等待者时让出立即拿回,单进程执行不产生额外等待。
type runQueue struct {
	token     chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newRunQueue() *runQueue {
	q := &runQueue{token: make(chan struct{}, 1), closed: make(chan struct{})}
	q.token <- struct{}{}
	return q
}

// enter 排队获取执行权;上下文结束或队列关闭时返回错误。
func (q *runQueue) enter(ctx context.Context) error {
	select {
	case <-q.token:
		return nil
	case <-q.closed:
		return errno.Wrap(errno.Cancelled, "内核已关闭")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// leave 归还令牌;不持有时(重复归还)静默忽略,保证幂等。
func (q *runQueue) leave() {
	select {
	case q.token <- struct{}{}:
	default:
	}
}

// slice 让出执行权并重新排队;没有其他等待者时立即拿回继续执行。
func (q *runQueue) slice(ctx context.Context) error {
	q.leave()
	return q.enter(ctx)
}

// close 唤醒全部排队等待者(以错误放行);此后 enter 一律失败。
func (q *runQueue) close() {
	q.closeOnce.Do(func() { close(q.closed) })
}
