package native_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/deployment/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/model"
)

// TestModelSuitePassesAgainstRealVM 用真实 VM 跑 A07 的全部行为契约:
// 控制台持久性与屏幕、独立连接、共享文件系统与关闭语义。
func TestModelSuitePassesAgainstRealVM(t *testing.T) {
	model.Suite(t, func() (model.VM, error) {
		return native.NewModelVM(context.Background(), native.Options{Seed: 1})
	})
}

// TestModelVMCancellationKeepsVMRunning 验证 P15:取消的等待不销毁
// VM,也不静默重建 Bash——之后的命令仍在同一状态上执行。
func TestModelVMCancellationKeepsVMRunning(t *testing.T) {
	machine, err := native.NewModelVM(context.Background(), native.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := machine.Shutdown(ctx); err != nil {
			t.Errorf("收尾 Shutdown: %v", err)
		}
	}()
	terminal, err := machine.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 已取消的上下文:请求直接失败。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := terminal.Bash().Run(cancelled, "printf never"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的运行 err=%v want context.Canceled", err)
	}
	// 执行中被取消:长命令在超时后立即结束(不等 5 秒),并报告非零
	// 退出码;取消与中断的分别语义由 P05 继续细化。
	timeout, cancelTimeout := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancelTimeout()
	start := time.Now()
	interrupted, err := terminal.Bash().Run(timeout, "sleep 5")
	if err != nil {
		t.Fatalf("超时的运行不应返回错误: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("超时的运行耗时=%v,应随取消结束", elapsed)
	}
	if interrupted.ExitCode == 0 {
		t.Fatalf("被中断的运行退出码=%d want 非零", interrupted.ExitCode)
	}
	result, err := terminal.Bash().Run(context.Background(), "printf alive")
	if err != nil || result.Output != "alive" {
		t.Fatalf("取消后 VM 应仍可用: %+v, %v", result, err)
	}
}

// TestModelVMConcurrentRunsKeepSessionsIsolated 验证 P15:并发运行的
// 会话各自独立,同一会话的并发请求不交错破坏 Shell 状态。
func TestModelVMConcurrentRunsKeepSessionsIsolated(t *testing.T) {
	machine, err := native.NewModelVM(context.Background(), native.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := machine.Shutdown(ctx); err != nil {
			t.Errorf("收尾 Shutdown: %v", err)
		}
	}()
	first, err := machine.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := machine.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Bash().Run(context.Background(), "export MARK=one"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errs := make(chan error, 32)
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			terminal := first
			if index%2 == 1 {
				terminal = second
			}
			result, err := terminal.Bash().Run(context.Background(), "printf '%s' \"${MARK-unset}\"")
			if err != nil {
				errs <- err
				return
			}
			want := "unset"
			if index%2 == 0 {
				want = "one"
			}
			if result.Output != want {
				errs <- fmt.Errorf("会话状态串扰: got %q want %q", result.Output, want)
			}
		}(index)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestModelVMFaultCommandLeavesVMRunning 验证 P15:失败与错误不致命,
// VM 在故障后仍然可用。
func TestModelVMFaultCommandLeavesVMRunning(t *testing.T) {
	machine, err := native.NewModelVM(context.Background(), native.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := machine.Shutdown(ctx); err != nil {
			t.Errorf("收尾 Shutdown: %v", err)
		}
	}()
	terminal, err := machine.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 语法错误:报告错误并给出非零退出码。
	result, err := terminal.Bash().Run(context.Background(), "if then")
	if err != nil {
		t.Fatalf("语法错误不应中断调用: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("语法错误的退出码=%d want 非零", result.ExitCode)
	}
	// 失败的命令:非零退出码,会话继续可用。
	result, err = terminal.Bash().Run(context.Background(), "no-such-command-xyz")
	if err != nil {
		t.Fatalf("失败命令不应返回错误: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("失败命令的退出码=%d want 非零", result.ExitCode)
	}
	if result, err = terminal.Bash().Run(context.Background(), "printf alive"); err != nil || result.Output != "alive" {
		t.Fatalf("故障后 VM 应仍可用: %+v, %v", result, err)
	}
}

// TestModelVMBackgroundOutputReachesScreenOnly 验证 P15:后台输出到达
// 控制台屏幕,但不混入前台命令的结果。
func TestModelVMBackgroundOutputReachesScreenOnly(t *testing.T) {
	machine, err := native.NewModelVM(context.Background(), native.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := machine.Shutdown(ctx); err != nil {
			t.Errorf("收尾 Shutdown: %v", err)
		}
	}()
	terminal, err := machine.Console().Attach(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := terminal.Bash().Run(context.Background(), "printf front; (sleep 0.02; printf back) &")
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "front" {
		t.Fatalf("前台结果=%q want \"front\"(不含后台输出)", result.Output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		screen, err := machine.Console().Screen(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(screen.Lines) > 0 && screen.Lines[0] == "frontback" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("后台输出未到达屏幕: %+v", screen)
		case <-tick.C:
		}
	}
}
