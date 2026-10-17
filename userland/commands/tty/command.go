package tty

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const terminalPath = "/dev/tty"

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "tty")
	silent := flags.BoolP("silent", "s", false, "静默：不打印路径，只用退出码回答")
	if err := commandio.Parse(flags, args); err != nil {
		return err
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

		if !*silent {
			_, _ = fmt.Fprintln(h.Stderr, "tty: 不是终端")
		}
		return interp.ExitStatus(1)
	}
	if !*silent {
		if _, err := fmt.Fprintln(h.Stdout, terminalPath); err != nil {
			return err
		}
	}
	return nil
}
