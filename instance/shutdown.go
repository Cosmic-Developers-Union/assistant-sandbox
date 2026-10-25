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

	// 2) 协作退出:终止全部正在执行的客体程序。
	for _, pid := range v.kernel.ActiveProcesses() {
		_ = v.kernel.Signal(pid, signal.TERM)
	}
	// 3) 等待退出:受上下文截止时间约束。
	waitErr := v.kernel.AwaitExit(ctx)
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

// ErrShutdownTimeout 表示协作退出未在截止时间内完成,已转入强制
// 终止流程。
var ErrShutdownTimeout = errno.TimedOut
