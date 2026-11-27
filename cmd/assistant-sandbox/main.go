package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/guest/devroot"
	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

var version = "dev"

type nativeCLIConfig struct {
	shell          nativeshell.Shell
	environment    map[string]string
	command        string
	session        bool
	interactive    bool
	workspace      string
	mountWorkspace bool
	stdin          io.Reader
	stdout         io.Writer
	stderr         io.Writer
}

// run 只调用公开 VM API(instance/guest native):单次执行在同一
// VM 内核中运行真实客体 shell,不再创建第二套运行时。
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) > 0 && args[0] == "bash" {
		args = args[1:]
	}
	flags := pflag.NewFlagSet("assistant-sandbox", pflag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "", "要挂载到 /work 的宿主目录；为空时使用临时内存 /work")
	command := flags.StringP("command", "c", "", "shell 脚本（默认 Bash）；未指定时读取标准输入")
	shellName := flags.String("shell", "bash", "原生客体 shell: bash 或 sh（zsh 暂不支持）")
	tools := flags.String("tools", "dev", "开发工具包: dev 或 minimal")
	cpus := flags.Int("cpus", 2, "客体 CPU 数 (1..8)")
	memory := flags.String("memory", "4g", "客体内存 (256m..16g，例如 300m/1.5g/16g)")
	noPTY := flags.Bool("no-pty", false, "禁用宿主 PTY，仅用于排查终端兼容问题")
	readOnly := flags.Bool("read-only", false, "工作区只读（当前原生 VM 明确拒绝）")
	allowGuestRWX := flags.Bool("allow-guest-rwx", true, "允许开发环境中的客体 JIT 使用可写可执行页；设为 false 启用严格 W^X")
	session := flags.Bool("session", false, "从 stdin 运行 shell 并保持同一 VM 会话")
	dryRun := flags.Bool("dry-run", false, "显示执行配置")
	debug := flags.Bool("debug", false, "输出调试信息")
	verbose := flags.Bool("verbose", false, "显示执行信息")
	backendName := flags.String("backend", "auto", "执行后端: auto、riscv64 或 linux/amd64")
	showVersion := flags.BoolP("version", "V", false, "显示版本")
	help := flags.BoolP("help", "h", false, "显示帮助")
	if err := flags.Parse(args); err != nil {
		return 1, err
	}
	if *help {
		flags.PrintDefaults()
		return 0, nil
	}
	if *showVersion {
		_, _ = fmt.Fprintf(stdout, "assistant-sandbox %s\n", version)
		return 0, nil
	}
	if flags.NArg() != 0 {
		return 1, fmt.Errorf("不支持位置参数: %v", flags.Args())
	}
	if *readOnly {
		return 1, errors.New("原生 VM 尚未接入只读工作区")
	}
	if *tools != "dev" && *tools != "minimal" {
		return 1, fmt.Errorf("不支持的开发工具包 %q（可用: dev, minimal）", *tools)
	}
	requestedBackend := instance.BackendAuto
	switch *backendName {
	case "auto":
	case "riscv64":
		requestedBackend = instance.BackendRISC64
	case "linux/amd64":
		requestedBackend = instance.BackendNativeAMD64
	default:
		return 1, fmt.Errorf("不支持的后端 %q（可用: auto, riscv64, linux/amd64）", *backendName)
	}
	memoryBytes, err := parseMemory(*memory)
	if err != nil {
		return 1, err
	}
	selectedShell, err := nativeshell.Parse(*shellName)
	if err != nil {
		return 1, err
	}
	workspace := "未挂载（临时内存 /work）"
	mountWorkspace := *dir != ""
	if mountWorkspace {
		absolute, err := filepath.Abs(*dir)
		if err != nil {
			return 1, err
		}
		workspace = absolute
	}
	if *verbose || *debug || *dryRun {
		_, _ = fmt.Fprintf(stderr, "工作区=%s shell=%s 客体=原生VM 只读=%t cpus=%d memory=%s\n",
			workspace, selectedShell, *readOnly, *cpus, *memory)
	}
	if *dryRun {
		return 0, nil
	}
	// 终端里的裸 `assistant-sandbox` 不能把 stdin 读到 EOF:用户会
	// 输入命令但永远没有 EOF,表现为卡住。交互终端自动进入逐行会话;
	// 管道/文件输入仍保留“无 -c 时读取完整脚本”的语义。
	if !*session && *command == "" && stdinIsTerminal(stdin) {
		*session = true
		_, _ = fmt.Fprintln(stderr, "进入交互会话；输入命令后回车，Ctrl-D 退出")
	}
	if *command == "" && !*session {
		data, err := io.ReadAll(io.LimitReader(stdin, (64<<10)+1))
		if err != nil {
			return 1, err
		}
		*command = string(data)
	}
	interactive := *session && !*noPTY && terminalPair(stdin, stdout)
	terminalName := "dumb"
	if interactive {
		terminalName = os.Getenv("TERM")
		if terminalName == "" || terminalName == "dumb" {
			terminalName = "xterm-256color"
		}
	}
	options := instance.Options{Backend: requestedBackend}
	options.MemoryBytes = memoryBytes
	options.CPUs = *cpus
	options.AllowGuestRWX = *allowGuestRWX && (*tools == "dev" || flags.Changed("allow-guest-rwx"))
	if *tools == "dev" {
		options.Session = resources.Limits{Memory: memoryBytes, Work: 1 << 38}
		options.Process = resources.Limits{Memory: memoryBytes, Work: 1 << 38}
	}
	started := time.Now()
	selection, err := instance.Open(ctx, options)
	if errors.Is(err, instance.ErrNativeWorkBudgetUnsupported) && requestedBackend != instance.BackendRISC64 {
		// CLI 的 RISC-V 退休指令预算在原生后端转换为 cgroup CPU 配额；
		// 自动选择若回退 RISC-V，上一尝试仍保留原预算。
		options.Session.Work = 0
		options.Process.Work = 0
		selection, err = instance.Open(ctx, options)
	}
	if err != nil {
		return 1, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = selection.Close(closeCtx)
	}()
	if *debug {
		_, _ = fmt.Fprintf(stderr, "debug: 创建 VM 用时 %s backend=%s\n", time.Since(started), selection.Backend)
	}
	environmentMap := map[string]string{
		"HOME": instance.WorkRoot, "PATH": "/usr/local/bin:/usr/bin:/bin",
		"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "USER": "sandbox",
		"LOGNAME": "sandbox", "TMPDIR": "/tmp", "SHELL": selectedShell.Program(),
		"TERM": terminalName, "ASSISTANT_SANDBOX_BACKEND": string(selection.Backend),
	}
	if selectedShell == nativeshell.Bash {
		environmentMap["PS1"] = `sandbox:${PWD}\$ `
	}
	if *tools == "dev" {
		environmentMap["PYTHONHOME"] = "/usr"
		environmentMap["NODE_ICU_DATA"] = "/usr/share/icu/76.1"
	}
	if options.AllowGuestRWX {
		environmentMap["ASSISTANT_SANDBOX_NODE_STANDARD"] = "1"
	}
	if handled, code, nativeErr := runNativeCLI(ctx, selection.Instance, nativeCLIConfig{
		shell: selectedShell, environment: environmentMap, command: *command,
		session: *session, interactive: interactive, workspace: workspace,
		mountWorkspace: mountWorkspace, stdin: stdin, stdout: stdout, stderr: stderr,
	}); handled {
		return code, nativeErr
	}
	machine, ok := selection.Instance.(*instance.VM)
	if !ok {
		return 1, fmt.Errorf("unsupported backend instance %T", selection.Instance)
	}
	if err := nativeshell.Install(ctx, machine.Filesystem()); err != nil {
		return 1, err
	}
	if *debug {
		_, _ = fmt.Fprintf(stderr, "debug: 安装原生 shell 用时 %s\n", time.Since(started))
	}
	if *tools == "dev" {
		if err := devroot.Install(ctx, machine.Filesystem()); err != nil {
			return 1, err
		}
		if *debug {
			_, _ = fmt.Fprintf(stderr, "debug: 安装开发工具用时 %s\n", time.Since(started))
		}
	}
	if mountWorkspace {
		if err := machine.Mounts().Mount(instance.WorkRoot, filesystem.NewMountFilesystem(workspace)); err != nil {
			return 1, err
		}
	}
	connection, err := machine.Connect(ctx)
	if err != nil {
		return 1, err
	}
	if *debug {
		_, _ = fmt.Fprintf(stderr, "debug: 建立连接用时 %s\n", time.Since(started))
	}
	if *session {
		if interactive {
			return runPTY(ctx, machine, connection, selectedShell, environmentMap, stdin.(*os.File), stdout.(*os.File))
		}
		args := []string{"-s"}
		if selectedShell == nativeshell.Bash {
			args = append([]string{"--noprofile", "--norc"}, args...)
		}
		process, err := connection.Exec(ctx, instance.ExecSpec{
			Program: selectedShell.Program(),
			Args:    args,
			Dir:     instance.WorkRoot,
			Env:     environmentMap,
			Streams: fd.Streams{Input: stdin, Output: stdout, Error: stderr},
		})
		if err != nil {
			return 1, err
		}
		state, err := connection.Wait(ctx, process.ID)
		if err != nil {
			return 1, err
		}
		return int(state.ExitCode), nil
	}
	shellArgs := []string{"-c", *command}
	if selectedShell == nativeshell.Bash {
		shellArgs = []string{"--noprofile", "--norc", "-c", *command}
	}
	process, err := connection.Exec(ctx, instance.ExecSpec{
		Program: selectedShell.Program(),
		Args:    shellArgs,
		Dir:     instance.WorkRoot,
		Env:     environmentMap,
		Streams: fd.Streams{Output: stdout, Error: stderr},
	})
	if err != nil {
		return 1, err
	}
	if *debug {
		_, _ = fmt.Fprintf(stderr, "debug: 启动 shell 用时 %s\n", time.Since(started))
	}
	state, err := connection.Wait(ctx, process.ID)
	if err != nil {
		return 1, err
	}
	if *debug {
		_, _ = fmt.Fprintf(stderr, "debug: shell 退出用时 %s\n", time.Since(started))
	}
	return int(state.ExitCode), nil
}

func stdinIsTerminal(r io.Reader) bool {
	file, ok := r.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), interruptSignals()...)
	code, err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
