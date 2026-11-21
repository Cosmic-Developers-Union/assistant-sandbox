package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/shell"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/spf13/pflag"
)

// run 只调用公开 VM API(instance/shell):单次执行在同一个 VM 内核
// 上运行 Bash 槽位,不再创建第二套运行时。
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) > 0 && args[0] == "bash" {
		args = args[1:]
	}
	flags := pflag.NewFlagSet("assistant-sandbox", pflag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", ".", "工作区目录")
	module := flags.String("module", "", "兼容旧参数；原生 VM 不使用 WASI 模块")
	command := flags.StringP("command", "c", "", "Bash 脚本；未指定时读取标准输入")
	readOnly := flags.Bool("read-only", false, "工作区只读（当前原生 VM 明确拒绝）")
	session := flags.Bool("session", false, "从 stdin 逐行执行并保持同一 VM/Bash 会话")
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
	workspace, err := filepath.Abs(*dir)
	if err != nil {
		return 1, err
	}
	if *module != "" && (*verbose || *debug) {
		_, _ = fmt.Fprintf(stderr, "忽略 --module=%s（原生 Linux ABI VM）\n", *module)
	}
	if *verbose || *debug || *dryRun {
		_, _ = fmt.Fprintf(stderr, "工作区=%s 客体=原生VM 只读=%t\n", workspace, *readOnly)
	}
	if *dryRun {
		return 0, nil
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
	defer func() { _ = machine.Close(context.Background()) }()
	if err := machine.Mounts().Mount(instance.WorkRoot, filesystem.NewMountFilesystem(workspace)); err != nil {
		return 1, err
	}
	environment := []string{
		"HOME=" + instance.WorkRoot,
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"USER=sandbox",
		"LOGNAME=sandbox",
		"TMPDIR=/tmp",
	}
	environmentMap := map[string]string{
		"HOME": instance.WorkRoot, "PATH": "/usr/local/bin:/usr/bin:/bin",
		"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "USER": "sandbox",
		"LOGNAME": "sandbox", "TMPDIR": "/tmp",
	}
	parent, err := machine.Kernel().Begin(0, "bash", nil, instance.WorkRoot, environmentMap, fd.Streams{})
	if err != nil {
		return 1, err
	}
	slot, err := shell.NewSlot(machine.Kernel(), parent, instance.WorkRoot, environment)
	if err != nil {
		return 1, err
	}
	defer func() { _ = slot.Close(context.Background()) }()
	if *session {
		scanner := bufio.NewScanner(stdin)
		scanner.Buffer(make([]byte, 0, 4096), 64<<10)
		status := 0
		for scanner.Scan() {
			output, stepErr := slot.Step(ctx, scanner.Bytes())
			if stepErr != nil {
				return 1, stepErr
			}
			if _, writeErr := stdout.Write(output.Data); writeErr != nil {
				return 1, writeErr
			}
			status = output.Status
		}
		if err := scanner.Err(); err != nil {
			return 1, err
		}
		return status, nil
	}
	output, err := slot.Step(ctx, []byte(*command))
	if err != nil {
		return 1, err
	}
	if _, err := stdout.Write(output.Data); err != nil {
		return 1, err
	}
	return output.Status, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	code, err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
