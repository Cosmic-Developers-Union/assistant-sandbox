package model

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Factory 创建一个待测 VM;套件负责在向量结束时 Shutdown。
// 模型实现(A06 迁入的参考实现)与真实实现(P15 的 VM 工厂)
// 注入同一 Suite,共用下列全部断言(A07):未满足项以失败显式
// 暴露,套件本身不含任何跳过逻辑。
type Factory func() (VM, error)

// Vector 是一个行为场景;名称与原始 sandbox-contract 一一对应。
type Vector struct {
	Name string
	Run  func(t Reporter, factory Factory)
}

// Reporter 与旧契约(contract.Reporter)同形并要求 Context:
// 向量只依赖 Error/Fatal 与可取消的根上下文,便于在非 testing.T
// 环境(未来的验收门禁)复用;*testing.T 自 Go 1.24 起满足。
type Reporter interface {
	Helper()
	Error(args ...any)
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Context() context.Context
}

func Vectors() []Vector {
	return []Vector{
		{"ConsoleBashSurvivesDisconnect", vectorConsoleBashSurvivesDisconnect},
		{"SSHStyleConnectionsHaveIndependentBash", vectorSSHStyleConnectionsHaveIndependentBash},
		{"ConsoleDisplaysCurrentScreenAfterReconnect", vectorConsoleDisplaysCurrentScreenAfterReconnect},
		{"ConsoleAndSSHShareFilesystemButNotBash", vectorConsoleAndSSHShareFilesystemButNotBash},
		{"ShutdownEndsConsoleAndSSH", vectorShutdownEndsConsoleAndSSH},
		{"NonzeroExitDoesNotEndBash", vectorNonzeroExitDoesNotEndBash},
	}
}

// Suite 依次运行全部行为场景;每个场景独立创建 VM 并在结束时
// 以有界超时 Shutdown(失败仅记录,不掩盖场景本身的断言失败)。
func Suite(t *testing.T, factory Factory) {
	t.Helper()
	for _, vector := range Vectors() {
		t.Run(vector.Name, func(t *testing.T) {
			vector.Run(t, factory)
		})
	}
}

func create(t Reporter, factory Factory) VM {
	t.Helper()
	vm, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	tester, ok := t.(*testing.T)
	if !ok {
		// 非 testing.T 的宿主在调用方负责 Shutdown。
		return vm
	}
	tester.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := vm.Shutdown(ctx); err != nil {
			tester.Errorf("场景收尾 Shutdown: %v", err)
		}
	})
	return vm
}

func connect(t Reporter, vm VM, console bool) Terminal {
	t.Helper()
	var terminal Terminal
	var err error
	if console {
		terminal, err = vm.Console().Attach(t.Context())
	} else {
		terminal, err = vm.Connect(t.Context())
	}
	if err != nil {
		t.Fatal(err)
	}
	return terminal
}

func run(t Reporter, terminal Terminal, script string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := terminal.Bash().Run(ctx, script)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("%q: %+v, %v", script, result, err)
	}
	return result.Output
}

func disconnect(t Reporter, terminal Terminal) {
	t.Helper()
	if err := terminal.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// vectorConsoleBashSurvivesDisconnect:Console 的 Bash 状态跨断开
// 保留——cwd、导出变量与函数在重连后仍然可见。
func vectorConsoleBashSurvivesDisconnect(t Reporter, factory Factory) {
	vm := create(t, factory)
	first := connect(t, vm, true)
	run(t, first, "mkdir project; cd project; export TASK=review; function task() { printf '%s' \"$TASK\"; }")
	disconnect(t, first)
	if _, err := first.Bash().Run(t.Context(), "pwd"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	second := connect(t, vm, true)
	if output := run(t, second, "printf '%s:' \"${PWD##*/}\"; task"); output != "project:review" {
		t.Fatal(output)
	}
}

// vectorSSHStyleConnectionsHaveIndependentBash:Connect 会话各自独立,
// 互不继承变量与目录;断开一个不影响其他会话。
func vectorSSHStyleConnectionsHaveIndependentBash(t Reporter, factory Factory) {
	vm := create(t, factory)
	first, second := connect(t, vm, false), connect(t, vm, false)
	run(t, first, "mkdir project; cd project; export TASK=review")
	if output := run(t, second, "printf '%s:%s' \"${TASK-unset}\" \"${PWD##*/}\""); strings.HasPrefix(output, "review:") || strings.HasSuffix(output, ":project") {
		t.Fatal(output)
	}
	disconnect(t, first)
	third := connect(t, vm, false)
	if output := run(t, third, "printf '%s' \"${TASK-unset}\""); output != "unset" {
		t.Fatal(output)
	}
	if output := run(t, second, "printf alive"); output != "alive" {
		t.Fatal(output)
	}
}

// vectorConsoleDisplaysCurrentScreenAfterReconnect:断开后后台进程的
// \r 屏幕更新仍持续到达;重连读取当前帧,且新输出在帧上继续。
func vectorConsoleDisplaysCurrentScreenAfterReconnect(t Reporter, factory Factory) {
	vm := create(t, factory)
	terminal := connect(t, vm, true)
	run(t, terminal, "printf before; (sleep 0.02; printf '\\rafter') &")
	disconnect(t, terminal)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		screen, err := vm.Console().Screen(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Equal(screen.Lines, []string{"aftere"}) {
			if screen.Cursor != (Position{Column: 5}) {
				t.Fatal(screen.Cursor)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err(), screen)
		case <-tick.C:
		}
	}
	reconnected := connect(t, vm, true)
	if output := run(t, reconnected, "printf '!'"); output != "!" {
		t.Fatal(output)
	}
	screen, err := vm.Console().Screen(t.Context())
	if err != nil || !slices.Equal(screen.Lines, []string{"after!"}) {
		t.Fatalf("%+v, %v", screen, err)
	}
}

// vectorConsoleAndSSHShareFilesystemButNotBash:文件系统在 Console、
// Connect 会话与 API 之间共享;Bash 状态严格隔离。
func vectorConsoleAndSSHShareFilesystemButNotBash(t Reporter, factory Factory) {
	vm := create(t, factory)
	console, ssh := connect(t, vm, true), connect(t, vm, false)
	if err := vm.Filesystem().WriteFile(t.Context(), "task.txt", []byte("from-api")); err != nil {
		t.Fatal(err)
	}
	for _, terminal := range []Terminal{console, ssh} {
		if output := run(t, terminal, "cat task.txt"); output != "from-api" {
			t.Fatal(output)
		}
	}
	run(t, console, "export TASK=console; printf from-console > task.txt")
	if output := run(t, ssh, "printf '%s:' \"${TASK-unset}\"; cat task.txt"); output != "unset:from-console" {
		t.Fatal(output)
	}
	run(t, ssh, "printf from-ssh > task.txt")
	data, err := vm.Filesystem().ReadFile(t.Context(), "task.txt")
	if err != nil || string(data) != "from-ssh" {
		t.Fatalf("%q, %v", data, err)
	}
}

// vectorShutdownEndsConsoleAndSSH:Shutdown 后全部入口返回 ErrClosed,
// 不再接受连接、执行或文件访问。
func vectorShutdownEndsConsoleAndSSH(t Reporter, factory Factory) {
	vm := create(t, factory)
	console, ssh := connect(t, vm, true), connect(t, vm, false)
	run(t, console, "printf ready")
	// 本向量自行关闭;create 的收尾 Shutdown 幂等。
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := vm.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, terminal := range []Terminal{console, ssh} {
		if _, err := terminal.Bash().Run(t.Context(), "printf alive"); !errors.Is(err, ErrClosed) {
			t.Fatal(err)
		}
	}
	if _, err := vm.Connect(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := vm.Console().Attach(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := vm.Console().Screen(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := vm.Filesystem().ReadFile(t.Context(), "task.txt"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

// vectorNonzeroExitDoesNotEndBash:命令非零退出码只作为 Result 返回,
// 会话继续可用。
func vectorNonzeroExitDoesNotEndBash(t Reporter, factory Factory) {
	vm := create(t, factory)
	terminal := connect(t, vm, true)
	result, err := terminal.Bash().Run(t.Context(), "false")
	if err != nil || result.ExitCode != 1 {
		t.Fatalf("%+v, %v", result, err)
	}
	if output := run(t, terminal, "printf alive"); output != "alive" {
		t.Fatal(output)
	}
}
