package stty

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)

	var all, save bool
	var operands []string
	for _, arg := range args {
		switch arg {
		case "-a":
			all = true
		case "-g":
			save = true
		default:
			if strings.HasPrefix(arg, "-") && arg != "-" && !isOperand(arg) {
				_, _ = fmt.Fprintf(h.Stderr, "stty: 未知选项 %s\n", arg)
				return interp.ExitStatus(2)
			}
			operands = append(operands, arg)
		}
	}
	if all && save {
		_, _ = fmt.Fprintln(h.Stderr, "stty: -a 与 -g 互斥")
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
	info, err := findTerminal(ctx, descriptors, p.ID)
	if err != nil {
		return err
	}
	if !info.TTY {
		_, _ = fmt.Fprintln(h.Stderr, "stty: 不是终端")
		return interp.ExitStatus(1)
	}
	if len(operands) == 0 || all {
		return report(h.Stdout, info, save)
	}

	setter, ok := descriptors.(fd.TerminalSetter)
	if !ok {
		_, _ = fmt.Fprintln(h.Stderr, "stty: 客体没有终端设置能力")
		return interp.ExitStatus(1)
	}
	state := info.State
	failures := 0
	for index := 0; index < len(operands); index++ {
		operand := operands[index]
		consumed, err := applyOperand(operand, operands[index+1:], &state, h.Stdout)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "stty: %v\n", err)
			failures++
			continue
		}
		index += consumed
	}
	if failures > 0 {
		return interp.ExitStatus(1)
	}
	return setter.SetTerminalState(ctx, uint64(p.ID), 0, state)
}

func isOperand(arg string) bool {
	switch arg {
	case "-icanon", "-echo", "-isig", "-mesg", "-a", "-g":
		return true
	}
	return false
}

func report(h interface{ Write([]byte) (int, error) }, info fd.Terminal, save bool) error {
	if save {
		_, err := fmt.Fprintf(h, ":%t:%t:%t:%t:%d:%d\n",
			info.State.Canonical, info.State.Echo, info.State.ISIG, info.State.Mesg,
			info.Rows, info.Columns)
		return err
	}

	if _, err := fmt.Fprintf(h, "speed 0 baud; rows %d; columns %d;", info.Rows, info.Columns); err != nil {
		return err
	}
	for _, entry := range []struct {
		name string
		on   bool
	}{
		{"icanon", info.State.Canonical},
		{"echo", info.State.Echo},
		{"isig", info.State.ISIG},
		{"mesg", info.State.Mesg},
	} {
		if !entry.on {
			continue
		}
		if _, err := fmt.Fprintf(h, " %s", entry.name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(h)
	return err
}

func applyOperand(operand string, rest []string, state *fd.TerminalState, stdout interface{ Write([]byte) (int, error) }) (int, error) {
	switch operand {
	case "icanon":
		state.Canonical = true
	case "-icanon":
		state.Canonical = false
	case "echo":
		state.Echo = true
	case "-echo":
		state.Echo = false
	case "isig":
		state.ISIG = true
	case "-isig":
		state.ISIG = false
	case "mesg":
		state.Mesg = true
	case "-mesg":
		state.Mesg = false
	case "sane":

		*state = fd.TerminalState{Canonical: true, Echo: true, ISIG: true, Mesg: true}
	case "rows":
		value, err := takeNumber(rest, "rows")
		if err != nil {
			return 0, err
		}
		state.Rows = value
		return 1, nil
	case "cols", "columns":
		value, err := takeNumber(rest, "cols")
		if err != nil {
			return 0, err
		}
		state.Columns = value
		return 1, nil
	case "size":

		_, err := fmt.Fprintf(stdout, "%d %d\n", state.Rows, state.Columns)
		return 0, err
	default:
		return 0, fmt.Errorf("%s: 客体终端没有这个事实", operand)
	}
	return 0, nil
}

func takeNumber(rest []string, name string) (uint16, error) {
	if len(rest) == 0 {
		return 0, fmt.Errorf("%s: 需要数字参数", name)
	}
	value, err := strconv.ParseUint(rest[0], 10, 16)
	if err != nil {
		return 0, fmt.Errorf("%s: %s 不是有效数字", name, rest[0])
	}
	return uint16(value), nil
}

func findTerminal(ctx context.Context, descriptors fd.Descriptors, pid proc.PID) (fd.Terminal, error) {
	for _, id := range []uint32{0, 1, 2} {
		info, err := descriptors.Terminal(ctx, uint64(pid), id)
		if err != nil {
			return fd.Terminal{}, err
		}
		if info.TTY {
			return info, nil
		}
	}
	return fd.Terminal{}, nil
}
