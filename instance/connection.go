package instance

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// ExecSpec 是一次客体执行的请求。
type ExecSpec struct {
	Program string
	Args    []string
	Dir     string
	Env     map[string]string
	Streams fd.Streams
}

// Connection 是一条到 VM 的连接:共享 VM 的内核(同一进程表)、文件
// 系统与设备;断开只释放这条连接,不销毁 VM 的设备与其他连接(C04)。
type Connection struct {
	vm *VM
	id uint64

	mu     sync.Mutex
	closed bool
}

// ID 返回连接标识:同进程中的连接可明确寻址。
func (c *Connection) ID() uint64 { return c.id }

// VM 返回连接所属的 VM。
func (c *Connection) VM() *VM { return c.vm }

// Processes 返回 VM 当前的进程表:同一台 VM 的全部连接看到同一份
// 进程表(C02)。
func (c *Connection) Processes() []proc.Proc { return c.vm.kernel.Processes() }

// Exec 在 VM 的内核中启动一个客体程序。进程挂在 VM 的常驻会话下,
// 因此同一 VM 的其他连接也能在进程表中看到它。
func (c *Connection) Exec(ctx context.Context, spec ExecSpec) (proc.Proc, error) {
	if err := ctx.Err(); err != nil {
		return proc.Proc{}, err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return proc.Proc{}, errno.Cancelled
	}
	dir := spec.Dir
	if dir == "" {
		dir = WorkRoot
	}
	process, err := c.vm.kernel.Begin(c.vm.session.ID, spec.Program, spec.Args, dir, spec.Env, spec.Streams)
	if err != nil {
		return proc.Proc{}, err
	}
	// 执行上下文由 VM 掌握:关闭时统一取消,不依赖执行器登记时机。
	runCtx, cancel := context.WithCancel(ctx)
	if c.vm.processTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.vm.processTimeout)
	}
	c.vm.registerRunning(process.ID, cancel)
	c.vm.executions.Add(1)
	go func() {
		defer c.vm.executions.Done()
		defer cancel()
		defer c.vm.unregisterRunning(process.ID)
		// 进程退出后保持僵尸态,由等待方回收(与 Linux 的父子语义
		// 一致);此处不代替父进程回收。
		_, _ = c.vm.kernel.RunRegistered(runCtx, process)
	}()
	return process, nil
}

// Wait 等待 VM 会话下某个子进程退出并回收它,返回其终态。
func (c *Connection) Wait(ctx context.Context, pid proc.PID) (proc.ChildState, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return proc.ChildState{}, errno.Cancelled
	}
	state, _, err := c.vm.kernel.WaitChild(ctx, c.vm.session.ID, pid, false)
	return state, err
}

// Disconnect 断开连接:只释放本连接,不影响 VM 的内核、文件系统与
// 设备,也不影响同一 VM 的其他连接(C04)。重复断开幂等。
func (c *Connection) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.vm.mu.Lock()
	delete(c.vm.connections, c.id)
	c.vm.mu.Unlock()
	return ctx.Err()
}

// Closed 报告连接是否已断开。
func (c *Connection) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// markClosed 由 VM 关闭时调用:连接失效但不解绑 VM。
func (c *Connection) markClosed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}
