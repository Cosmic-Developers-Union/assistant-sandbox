package nice

import (
	"context"
	"errors"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "nice")
	adjustment := flags.StringP("adjustment", "n", "", "优先级增量（相对父进程）")

	flags.SetInterspersed(false)
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	control, ok := p.Control.(proc.PriorityControl)
	if !ok {
		_, _ = fmt.Fprintln(h.Stderr, "nice: 客体没有优先级控制能力")
		return interp.ExitStatus(1)
	}
	if len(operands) == 0 {

		current, err := control.Nice(p.ID)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "nice: 读取优先级失败: %v\n", err)
			return interp.ExitStatus(1)
		}
		_, _ = fmt.Fprintln(h.Stdout, current)
		return nil
	}
	increment, err := parseIncrement(*adjustment)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "nice: %v\n", err)
		return interp.ExitStatus(2)
	}
	spec := proc.RunSpec{
		Parent:     p.ID,
		Name:       operands[0],
		Args:       operands[1:],
		Dir:        h.Dir,
		Streams:    fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr},
		NiceAdjust: &increment,
	}
	if _, err := p.Control.Run(ctx, spec); err != nil {
		switch {
		case errors.Is(err, errno.NotFound):
			_, _ = fmt.Fprintf(h.Stderr, "未注册沙箱命令: %s\n", operands[0])
			return interp.ExitStatus(127)
		case errors.Is(err, errno.NoExec), errors.Is(err, errno.Permission):
			_, _ = fmt.Fprintf(h.Stderr, "%s: 无法执行\n", operands[0])
			return interp.ExitStatus(126)
		}
		return fmt.Errorf("执行 %s: %w", operands[0], err)
	}
	return nil
}

func parseIncrement(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	switch text[0] {
	case '-':
		value, err := parseIncrement(text[1:])
		if err != nil {
			return 0, err
		}
		return -value, nil
	case '+':
		return parseIncrement(text[1:])
	}
	value := 0
	for i := 0; i < len(text); i++ {
		digit := text[i]
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("增量 %s 不是整数", text)
		}
		value = value*10 + int(digit-'0')
		if value > 1<<20 {
			return 0, fmt.Errorf("增量 %s 超界", text)
		}
	}
	return value, nil
}
