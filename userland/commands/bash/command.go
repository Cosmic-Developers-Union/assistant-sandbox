package bash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"

	"github.com/spf13/pflag"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type bashDepthKey struct{}

type RunnerFactory func(string, []string, io.Reader, io.Writer, io.Writer) (*interp.Runner, error)

type Command struct{ NewRunner RunnerFactory }

func processEnvironment(ctx context.Context, h interp.HandlerContext) []string {
	if p, err := proc.FromContext(ctx); err == nil && p.Env != nil {
		environment := make([]string, 0, len(p.Env))
		for key, value := range p.Env {
			environment = append(environment, key+"="+value)
		}
		sort.Strings(environment)
		return environment
	}
	var environment []string
	h.Env.Each(func(key string, v expand.Variable) bool {
		if v.IsSet() && v.Exported {
			environment = append(environment, key+"="+v.String())
		}
		return true
	})
	return environment
}

func (c Command) Run(ctx context.Context, args []string) (closeResult0 error) {
	h := commandio.Context(ctx)
	depth, _ := ctx.Value(bashDepthKey{}).(int)
	if depth >= 16 {
		_, _ = fmt.Fprintln(h.Stderr, "bash: 嵌套超过16层")
		return interp.ExitStatus(2)
	}
	flags := pflag.NewFlagSet("bash", pflag.ContinueOnError)
	flags.SetOutput(h.Stderr)
	flags.SetInterspersed(false)
	command := flags.StringP("command", "c", "", "执行脚本字符串")
	stdin := flags.BoolP("stdin", "s", false, "从 stdin 读取脚本")
	errexit := flags.BoolP("errexit", "e", false, "命令失败时退出")
	nounset := flags.BoolP("nounset", "u", false, "未设置的变量报错")
	trace := flags.BoolP("xtrace", "x", false, "显示执行步骤")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}
		return interp.ExitStatus(2)
	}
	positions := flags.Args()
	name := "bash"
	var source io.Reader
	if flags.Changed("command") {
		source = strings.NewReader(*command)
		if len(positions) > 0 {
			name = positions[0]
			positions = positions[1:]
		}
	} else if !*stdin && len(positions) > 0 {
		name = positions[0]
		positions = positions[1:]
		filename := name
		if !path.IsAbs(filename) {
			filename = path.Join(h.Dir, filename)
		}
		file, err := commandio.Open(ctx, filename, vmfs.ReadOnly, 0)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "bash: 读取脚本: %v\n", err)
			return interp.ExitStatus(1)
		}
		defer func() {
			closeResult0 = errors.Join(closeResult0, file.Close())
		}()

		source = file
	} else {
		source = h.Stdin
	}
	raw, err := io.ReadAll(io.LimitReader(source, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		_, _ = fmt.Fprintln(h.Stderr, "bash: 脚本读取失败或超过64 KiB")
		return interp.ExitStatus(2)
	}
	environment := processEnvironment(ctx, h)
	runner, err := c.NewRunner(h.Dir, environment, h.Stdin, h.Stdout, h.Stderr)
	if err != nil {
		return err
	}
	var options []string
	if *errexit {
		options = append(options, "-e")
	}
	if *nounset {
		options = append(options, "-u")
	}
	if *trace {
		options = append(options, "-x")
	}
	options = append(options, "--")
	options = append(options, positions...)
	if err := interp.Params(options...)(runner); err != nil {
		return err
	}
	tree, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(string(raw)), name)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "bash: 解析脚本: %v\n", err)
		return interp.ExitStatus(2)
	}
	syntax.Walk(tree, func(node syntax.Node) bool {
		if _, ok := node.(*syntax.ProcSubst); ok {
			err = fmt.Errorf("VM 不支持 IPC 进程替换")
		}
		if _, ok := node.(*syntax.TimeClause); ok {
			err = fmt.Errorf("VM 尚未提供语言计时接口")
		}
		if stmt, ok := node.(*syntax.Stmt); ok && stmt.Background {
			err = fmt.Errorf("Bash loop 暂不开放后台任务")
		}
		return err == nil
	})
	if err != nil {
		_, _ = fmt.Fprintln(h.Stderr, err)
		return interp.ExitStatus(2)
	}
	return runner.Run(context.WithValue(ctx, bashDepthKey{}, depth+1), tree)
}
