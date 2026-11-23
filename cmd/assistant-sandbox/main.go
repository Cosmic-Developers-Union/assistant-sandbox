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

	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

// run 只调用公开 VM API(instance/guest native):单次执行在同一
// VM 内核中运行真实客体 shell,不再创建第二套运行时。
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) > 0 && args[0] == "bash" {
		args = args[1:]
	}
	flags := pflag.NewFlagSet("assistant-sandbox", pflag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "", "要挂载到 /work 的宿主目录；为空时使用临时内存 /work")
	module := flags.String("module", "", "已弃用：兼容旧 WASI CLI 参数，原生 VM 忽略")
	command := flags.StringP("command", "c", "", "shell 脚本（默认 Bash）；未指定时读取标准输入")
	shellName := flags.String("shell", "bash", "原生客体 shell: bash 或 sh（zsh 暂不支持）")
	noPTY := flags.Bool("no-pty", false, "禁用宿主 PTY，仅用于排查终端兼容问题")
	readOnly := flags.Bool("read-only", false, "工作区只读（当前原生 VM 明确拒绝）")
	session := flags.Bool("session", false, "从 stdin 运行 shell 并保持同一 VM 会话")
	dryRun := flags.Bool("dry-run", false, "显示执行配置")
	debug := flags.Bool("debug", false, "输出调试信息")
	verbose := flags.Bool("verbose", false, "显示执行信息")
	help := flags.BoolP("help", "h", false, "显示帮助")
	if err := flags.Parse(args); err != nil {
		return 1, err
	}
	if *help {
		flags.PrintDefaults()
		return 0, nil
	}
	if flags.NArg() != 0 {
		return 1, fmt.Errorf("不支持位置参数: %v", flags.Args())
	}
	if *readOnly {
		return 1, errors.New("原生 VM 尚未接入只读工作区")
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
	if *module != "" && (*verbose || *debug) {
		_, _ = fmt.Fprintf(stderr, "忽略 --module=%s（原生 Linux ABI VM）\n", *module)
	}
	if *verbose || *debug || *dryRun {
		_, _ = fmt.Fprintf(stderr, "工作区=%s shell=%s 客体=原生VM 只读=%t\n", workspace, selectedShell, *readOnly)
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
	machine, err := instance.New(ctx, instance.Options{})
	if err != nil {
		return 1, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = machine.Close(closeCtx)
	}()
	if err := nativeshell.Install(ctx, machine.Filesystem()); err != nil {
		return 1, err
	}
	if mountWorkspace {
		if err := machine.Mounts().Mount(instance.WorkRoot, filesystem.NewMountFilesystem(workspace)); err != nil {
			return 1, err
		}
	}
	interactive := *session && !*noPTY && terminalPair(stdin, stdout)
	terminalName := "dumb"
	if interactive {
		terminalName = os.Getenv("TERM")
		if terminalName == "" {
			terminalName = "xterm-256color"
		}
	}
	environmentMap := map[string]string{
		"HOME": instance.WorkRoot, "PATH": "/usr/local/bin:/usr/bin:/bin",
		"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "USER": "sandbox",
		"LOGNAME": "sandbox", "TMPDIR": "/tmp", "SHELL": selectedShell.Program(),
		"TERM": terminalName,
	}
	connection, err := machine.Connect(ctx)
	if err != nil {
		return 1, err
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
	state, err := connection.Wait(ctx, process.ID)
	if err != nil {
		return 1, err
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
