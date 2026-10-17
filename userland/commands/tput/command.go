package tput

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "tput")
	_ = flags.StringP("termtype", "T", "", "终端类型（客体没有 terminfo 数据库，不接受）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if flags.Changed("termtype") {
		_, _ = fmt.Fprintln(h.Stderr, "tput: -T 需要 terminfo 数据库，客体没有")
		return interp.ExitStatus(1)
	}
	if len(operands) < 1 {
		_, _ = fmt.Fprintln(h.Stderr, "tput: 需要 capname 操作数")
		return interp.ExitStatus(2)
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return err
	}
	info, err := descriptors.Terminal(ctx, uint64(p.ID), 0)
	if err != nil {
		return err
	}
	if !info.TTY {
		_, _ = fmt.Fprintln(h.Stderr, "tput: 不是终端")
		return interp.ExitStatus(1)
	}
	switch operands[0] {
	case "cols":
		_, err := fmt.Fprintln(h.Stdout, info.Columns)
		return err
	case "lines", "rows":
		_, err := fmt.Fprintln(h.Stdout, info.Rows)
		return err
	default:
		_, _ = fmt.Fprintf(h.Stderr, "tput: %s: 客体没有 terminfo 数据库\n", operands[0])
		return interp.ExitStatus(1)
	}
}
