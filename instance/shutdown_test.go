package instance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// shutdownVM 建立一台带协作退出与顽固程序的 VM。
func shutdownVM(t *testing.T) *VM {
	t.Helper()
	machine, err := New(t.Context(), Options{
		Deterministic: true,
		Programs: map[string]executable.Program{
			"hold": executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			}),
			"stubborn": executable.Func(func(context.Context, proc.Proc) (uint8, error) {
				// 忽略协作退出:无视取消,占用一段时间后自行结束。
				time.Sleep(200 * time.Millisecond)
				return 0, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return machine
}

// waitActive 等待指定数量的客体程序真正开始执行。
func waitActive(t *testing.T, machine *VM, count int) {
	t.Helper()
	for range 200 {
		if len(machine.Kernel().ActiveProcesses()) == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待 %d 个客体程序开始执行超时", count)
}

// TestShutdownIsIdempotentAndReleasesInOrder 验证 C06:关闭幂等,重复
// 关闭不重复释放;客体进程、句柄与后台工作最终全部退出。
func TestShutdownIsIdempotentAndReleasesInOrder(t *testing.T) {
	machine := shutdownVM(t)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := connection.Exec(t.Context(), ExecSpec{Program: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	waitActive(t, machine, 1)
	if err := machine.Shutdown(t.Context()); err != nil {
		t.Fatalf("关闭: %v", err)
	}
	// 正在执行的客体程序已全部退出。
	if active := machine.Kernel().ActiveProcesses(); len(active) != 0 {
		t.Fatalf("仍有客体程序在执行: %+v", active)
	}
	// 连接句柄失效。
	if !connection.Closed() {
		t.Fatal("关闭后连接应失效")
	}
	// 内核进入终态。
	closed, err := machine.Kernel().Closed()
	if err != nil || !closed {
		t.Fatalf("内核未关闭: %v %v", closed, err)
	}
	// 重复关闭幂等。
	if err := machine.Shutdown(t.Context()); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	if machine.Connections() != 0 {
		t.Fatalf("连接数=%d want 0", machine.Connections())
	}
	// 关闭后拒绝新连接与新任务。
	if _, err := machine.Connect(t.Context()); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后应拒绝新连接: %v", err)
	}
	_ = process
}

// TestShutdownWithDeadlineEscalatesToTermination 验证 C07:协作退出超时
// 后进入明确终止流程,接口在截止时间附近有界返回,不留存宿主 goroutine。
func TestShutdownWithDeadlineEscalatesToTermination(t *testing.T) {
	machine := shutdownVM(t)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(t.Context(), ExecSpec{Program: "stubborn"}); err != nil {
		t.Fatal(err)
	}
	waitActive(t, machine, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = machine.Shutdown(ctx)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时关闭 err=%v want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("关闭耗时=%v want 有界", elapsed)
	}
	// 强制终止后 VM 仍进入终态,连接失效。
	if !machine.Closed() {
		t.Fatal("超时关闭后 VM 应进入终态")
	}
	if !connection.Closed() {
		t.Fatal("超时关闭后连接应失效")
	}
	// 后台工作最终退出:顽固程序自行结束后不再有运行中的实例。
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer waitCancel()
	if err := machine.Kernel().AwaitExit(waitCtx); err != nil {
		t.Fatalf("客体程序未退出: %v", err)
	}
}
