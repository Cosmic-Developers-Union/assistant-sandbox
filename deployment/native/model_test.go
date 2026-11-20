package native_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// TestModelVMQueuedRunCancelsWithoutExecuting 验证 P04:同一 Console
// 的多个句柄并发提交命令时串行执行;排队中的请求取消后不会在之后
// 执行,正在运行的命令与 Shell 状态不受影响。
func TestModelVMQueuedRunCancelsWithoutExecuting(t *testing.T) {
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
	first, err := machine.Console().Attach(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := machine.Console().Attach(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	running := make(chan model.Result, 1)
	runErr := make(chan error, 1)
	go func() {
		result, err := first.Bash().Run(context.Background(), "printf started > p04-started; sleep 0.4; printf first")
		if err != nil {
			runErr <- err
			return
		}
		running <- result
	}()
	// 等待前台命令确实占住执行权(标记文件出现)。
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := machine.Filesystem().ReadFile(context.Background(), "p04-started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("前台命令未开始")
		}
		time.Sleep(time.Millisecond)
	}

	queued, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := second.Bash().Run(queued, "printf never > p04-queued"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("排队请求 err=%v want context.DeadlineExceeded", err)
	}
	select {
	case err := <-runErr:
		t.Fatalf("前台命令失败: %v", err)
	case result := <-running:
		if result.Output != "first" || result.ExitCode != 0 {
			t.Fatalf("前台结果=%+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("前台命令未完成")
	}
	// 被取消的排队请求没有执行。
	if _, err := machine.Filesystem().ReadFile(context.Background(), "p04-queued"); err == nil {
		t.Fatal("取消的排队请求被执行了")
	}
	// 同一 Console 的 Shell 状态仍可用。
	result, err := second.Bash().Run(context.Background(), "printf alive")
	if err != nil || result.Output != "alive" {
		t.Fatalf("取消排队后 Console 应可用: %+v, %v", result, err)
	}
}

// TestModelVMInterruptForegroundKeepsBash 验证 P05:发送中断只终止当前
// 前台命令(SIGINT),目录与变量保留,不重建 Bash。
func TestModelVMInterruptForegroundKeepsBash(t *testing.T) {
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
	interrupter, ok := terminal.Bash().(model.Interrupter)
	if !ok {
		t.Fatal("Bash 未实现 model.Interrupter")
	}

	results := make(chan model.Result, 1)
	runErr := make(chan error, 1)
	go func() {
		result, err := terminal.Bash().Run(context.Background(),
			"mkdir -p p05; cd p05; export K=keep; printf started > marker; sleep 5; printf after")
		if err != nil {
			runErr <- err
			return
		}
		results <- result
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := machine.Filesystem().ReadFile(context.Background(), "p05/marker"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("前台命令未开始")
		}
		time.Sleep(time.Millisecond)
	}
	if err := interrupter.Interrupt(context.Background()); err != nil {
		t.Fatalf("发送中断: %v", err)
	}
	select {
	case err := <-runErr:
		t.Fatalf("中断后 Run 返回错误: %v", err)
	case result := <-results:
		if result.ExitCode == 0 {
			t.Fatalf("被中断的命令应非零退出: %+v", result)
		}
		if strings.Contains(result.Output, "after") {
			t.Fatalf("中断后仍执行了后续命令: %q", result.Output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("中断未结束前台命令")
	}
	// 不静默重建 Bash:目录、变量与函数保持。
	result, err := terminal.Bash().Run(context.Background(),
		"printf '%s:%s' \"$K\" \"${PWD##*/}\"")
	if err != nil || result.Output != "keep:p05" {
		t.Fatalf("中断后会话状态丢失: %+v, %v", result, err)
	}
}

// TestModelVMCancelWaitShutdownAndInterruptDiffer 验证 P05:取消等待、
// 发送中断与关闭 VM 三种终止路径可分别观察,均不静默重建 Bash。
func TestModelVMCancelWaitShutdownAndInterruptDiffer(t *testing.T) {
	machine, err := native.NewModelVM(context.Background(), native.Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := machine.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	interrupter, ok := terminal.Bash().(model.Interrupter)
	if !ok {
		t.Fatal("Bash 未实现 model.Interrupter")
	}

	// 取消等待:调用方超时返回,VM 与会话仍可用。
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := terminal.Bash().Run(waitCtx, "sleep 5; printf canceled"); !errors.Is(err, context.DeadlineExceeded) && err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("取消等待 err=%v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("取消等待耗时=%v", elapsed)
	}
	if result, err := terminal.Bash().Run(context.Background(), "printf cancelled-alive"); err != nil || result.Output != "cancelled-alive" {
		t.Fatalf("取消等待后会话不可用: %+v, %v", result, err)
	}

	// 关闭 VM:进行中的 Run 有界结束,之后全部入口 ErrClosed。
	results := make(chan error, 1)
	go func() {
		_, runErr := terminal.Bash().Run(context.Background(), "sleep 5")
		results <- runErr
	}()
	time.Sleep(50 * time.Millisecond)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	if err := machine.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-results:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown 后运行中的 Run 未结束")
	}
	if _, err := terminal.Bash().Run(context.Background(), "printf x"); !errors.Is(err, model.ErrClosed) {
		t.Fatalf("关闭后 Run err=%v want ErrClosed", err)
	}
	if err := interrupter.Interrupt(context.Background()); !errors.Is(err, model.ErrClosed) {
		t.Fatalf("关闭后 Interrupt err=%v want ErrClosed", err)
	}
}

// TestModelVMBashExitStateAndRestart 验证 P14:Console 的 Bash 执行
// exit 后状态可查询,后续 Run 明确返回 ErrBashExited,重新连接观察
// 同一状态;显式 Restart 后才恢复执行且不继承旧变量。
func TestModelVMBashExitStateAndRestart(t *testing.T) {
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
	lifecycle, ok := terminal.Bash().(model.StatefulBash)
	if !ok {
		t.Fatal("Bash 未实现 model.StatefulBash")
	}
	result, err := terminal.Bash().Run(context.Background(), "export KEEP=old; exit 7")
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("exit 结果=%+v err=%v", result, err)
	}
	state, err := lifecycle.State(context.Background())
	if err != nil || !state.Exited || state.ExitCode != 7 {
		t.Fatalf("State=%+v err=%v", state, err)
	}
	if _, err := terminal.Bash().Run(context.Background(), "printf must-not-run"); !errors.Is(err, model.ErrBashExited) {
		t.Fatalf("退出后 Run err=%v want ErrBashExited", err)
	}
	// 重新连接看到同一 Console 的退出状态(不静默重建)。
	reconnected, err := machine.Console().Attach(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reconnectedState, err := reconnected.Bash().(model.StatefulBash).State(context.Background())
	if err != nil || reconnectedState != state {
		t.Fatalf("重连状态=%+v err=%v want %+v", reconnectedState, err, state)
	}
	// 显式重启:目录回到工作区、旧变量不继承。
	if err := lifecycle.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = lifecycle.State(context.Background())
	if err != nil || state.Exited {
		t.Fatalf("Restart 后 State=%+v err=%v", state, err)
	}
	result, err = reconnected.Bash().Run(context.Background(),
		"printf '%s:%s' \"${KEEP-unset}\" \"${PWD##*/}\"")
	if err != nil || result.Output != "unset:work" {
		t.Fatalf("重启后会话=%+v err=%v", result, err)
	}
}
