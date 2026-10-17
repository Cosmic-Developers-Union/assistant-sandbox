package env

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "env")

	flags.SetInterspersed(false)
	ignore := flags.BoolP("ignore-environment", "i", false, "忽略继承的环境")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	environment := map[string]string{}
	if !*ignore {
		if p, err := proc.FromContext(ctx); err == nil {
			for name, value := range p.Env {
				environment[name] = value
			}
		}
	}
	rest := flags.Args()
	for len(rest) > 0 && strings.Contains(rest[0], "=") && !strings.HasPrefix(rest[0], "=") {
		assignment := rest[0]
		rest = rest[1:]
		name, value, _ := strings.Cut(assignment, "=")
		if name == "" {
			return fmt.Errorf("赋值无效: %s", assignment)
		}
		environment[name] = value
	}
	if len(rest) > 0 {
		return execute(ctx, rest[0], rest[1:], environment)
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintf(h.Stdout, "%s=%s\n", name, environment[name]); err != nil {
			return err
		}
	}
	return nil
}

func execute(ctx context.Context, name string, args []string, environment map[string]string) error {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	if p.Control == nil {
		return fmt.Errorf("执行 %s: 缺少进程控制端口", name)
	}
	h := commandio.Context(ctx)
	terminal, err := p.Control.Run(ctx, proc.RunSpec{
		Parent: p.ID, Name: name, Args: args, Dir: p.Dir, Env: environment,
		Streams: fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr},
	})
	if err != nil {
		switch {
		case errors.Is(err, errno.NotFound):

			_, _ = fmt.Fprintf(h.Stderr, "未注册沙箱命令: %s\n", name)
			return interp.ExitStatus(127)
		case errors.Is(err, errno.NoExec), errors.Is(err, errno.Permission):

			_, _ = fmt.Fprintf(h.Stderr, "%s: 无法执行\n", name)
			return interp.ExitStatus(126)
		}
		return fmt.Errorf("执行 %s: %w", name, err)
	}
	if terminal.ExitCode == 0 {
		return nil
	}

	return interp.ExitStatus(terminal.ExitCode)
}
