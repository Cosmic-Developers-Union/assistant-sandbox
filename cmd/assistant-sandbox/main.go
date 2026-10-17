package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	sandbox "github.com/Cosmic-Developers-Union/assistant-sandbox"
	"github.com/spf13/pflag"
)

func run(ctx context.Context, args []string) (int, error) {
	if len(args) > 0 && args[0] == "bash" {
		args = args[1:]
	}
	flags := pflag.NewFlagSet("assistant-sandbox", pflag.ContinueOnError)
	dir := flags.String("dir", ".", "工作区目录")
	module := flags.String("module", "", "WASI gzip 客体路径；默认使用安装目录中的客体")
	command := flags.StringP("command", "c", "", "Bash 脚本；未指定时读取标准输入")
	readOnly := flags.Bool("read-only", false, "工作区只读")
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
	if *module == "" {
		executable, err := os.Executable()
		if err != nil {
			return 1, err
		}
		*module = filepath.Join(filepath.Dir(executable), "..", "share", "assistant-sandbox", "assistant-bash.wasm.gz")
	}
	if *verbose || *debug || *dryRun {
		fmt.Fprintf(os.Stderr, "工作区=%s 客体=%s 只读=%t\n", *dir, *module, *readOnly)
	}
	if *dryRun {
		return 0, nil
	}
	if *command == "" {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, (64<<10)+1))
		if err != nil {
			return 1, err
		}
		*command = string(data)
	}
	archive, err := os.ReadFile(*module)
	if err != nil {
		return 1, err
	}
	runtime, err := sandbox.New(ctx, sandbox.Config{ShellArchive: archive, ReadOnly: *readOnly})
	if err != nil {
		return 1, err
	}
	result, err := runtime.Execute(ctx, *dir, *command, os.Stdout, os.Stderr)
	return result.ExitCode, errors.Join(err, runtime.Close())
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	code, err := run(ctx, os.Args[1:])
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
