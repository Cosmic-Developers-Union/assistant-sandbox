package mesg

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "mesg")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) > 1 {
		_, _ = fmt.Fprintln(h.Stderr, "mesg: 只接受 y 或 n")
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
	info, id, err := findTerminal(ctx, descriptors, p.ID)
	if err != nil {
		return err
	}
	if !info.TTY {
		_, _ = fmt.Fprintln(h.Stderr, "mesg: 不是终端")
		return interp.ExitStatus(1)
	}
	if len(operands) == 0 {
		answer := "n"
		if info.State.Mesg {
			answer = "y"
		}

		_, err := fmt.Fprintf(h.Stdout, "is %s\n", answer)
		return err
	}
	setter, ok := descriptors.(fd.TerminalSetter)
	if !ok {
		_, _ = fmt.Fprintln(h.Stderr, "mesg: 客体没有终端设置能力")
		return interp.ExitStatus(1)
	}
	state := info.State
	switch operands[0] {
	case "y":
		state.Mesg = true
	case "n":
		state.Mesg = false
	default:
		_, _ = fmt.Fprintln(h.Stderr, "mesg: 只接受 y 或 n")
		return interp.ExitStatus(2)
	}
	return setter.SetTerminalState(ctx, uint64(p.ID), id, state)
}

func findTerminal(ctx context.Context, descriptors fd.Descriptors, pid proc.PID) (fd.Terminal, uint32, error) {
	for _, id := range []uint32{0, 1, 2} {
		info, err := descriptors.Terminal(ctx, uint64(pid), id)
		if err != nil {
			return fd.Terminal{}, 0, err
		}
		if info.TTY {
			return info, id, nil
		}
	}
	return fd.Terminal{}, 0, nil
}
