package instance

import (
	"context"
	"errors"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

// Shutdown 关闭 VM(C06/C07):回收顺序固定为
//
//  1. 停止接受新连接与新任务;
//  2. 协作退出:向全部客体进程投递 SIGTERM;
//  3. 等待进程退出(受上下文截止时间约束);
//  4. 超时后进入明确终止流程:向残留进程投递 SIGKILL 并关闭内核;
//  5. 释放连接句柄。
//
// 重复调用幂等:第二次调用直接返回。上下文无截止时间时,协作退出
// 之后仍会以强制终止收尾,保证接口有界返回、不遗留宿主 goroutine。
func (v *VM) Shutdown(ctx context.Context) error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	connections := make([]*Connection, 0, len(v.connections))
	for _, connection := range v.connections {
		connections = append(connections, connection)
	}
	v.connections = map[uint64]*Connection{}
	v.mu.Unlock()

	// 2) 协作退出:先取消执行上下文(不依赖执行器登记时机),再向
	// 全部客体进程投递 SIGTERM。
	v.cancelRunning()
	for _, pid := range v.kernel.ActiveProcesses() {
		_ = v.kernel.Signal(pid, signal.TERM)
	}
	// 3) 等待退出:受上下文截止时间约束。
	waitErr := v.awaitExecutions(ctx)
	// 4) 超时则强制终止。
	if waitErr != nil {
		for _, pid := range v.kernel.ActiveProcesses() {
			_ = v.kernel.Signal(pid, signal.KILL)
		}
	}
	// 5) 关闭内核:中断残留执行、关闭等待队列、进入终态。
	closeErr := v.kernel.Close(ctx)
	for _, connection := range connections {
		connection.markClosed()
	}
	if waitErr != nil {
		return errors.Join(waitErr, closeErr)
	}
	return closeErr
}

// awaitExecutions 等待 VM 发起的客体执行全部结束;上下文到期即返回
// 错误。执行结束后进程可能仍处于僵尸态(由等待方回收),不影响此处
// 的完成判定。
func (v *VM) awaitExecutions(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		v.executions.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ErrShutdownTimeout 表示协作退出未在截止时间内完成,已转入强制
// 终止流程。
var ErrShutdownTimeout = errno.TimedOut
