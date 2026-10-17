package tabs

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "tabs")

	if err := commandio.Parse(flags, nil); err != nil {
		return err
	}
	for _, arg := range args {
		if arg == "+m" || strings.HasPrefix(arg, "+m") {
			_, _ = fmt.Fprintln(h.Stderr, "tabs: 客体终端没有边距事实，+m[n] 不支持")
			return interp.ExitStatus(1)
		}
		if arg == "--" {
			break
		}
	}
	operands := args
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return err
	}
	setter, targetID, err := tabSetter(ctx, descriptors, p.ID)
	if err != nil {
		_, _ = fmt.Fprintln(h.Stderr, "tabs: 标准流不是终端")
		return interp.ExitStatus(1)
	}
	var stops []uint16
	switch {
	case len(operands) == 0:

	case len(operands) == 1 && strings.HasPrefix(operands[0], "-") && isNumber(operands[0][1:]):

		n, err := strconv.Atoi(operands[0][1:])
		if err != nil || n < 1 || n > 62 {
			_, _ = fmt.Fprintf(h.Stderr, "tabs: 间距 %q 必须是 1..62 的整数\n", operands[0])
			return interp.ExitStatus(2)
		}

		const declaredWidth = 1024
		for col := n; col <= declaredWidth; col += n {
			stops = append(stops, uint16(col))
		}
	default:

		_, _ = fmt.Fprintf(h.Stderr, "tabs: 制表位类型 %q 不支持（客体只支持 -n 与缺省）\n", operands[0])
		return interp.ExitStatus(2)
	}
	if err := setter.SetTabStops(stops); err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "tabs: %v\n", err)
		return interp.ExitStatus(1)
	}
	_ = targetID
	return nil
}

func tabSetter(ctx context.Context, descriptors fd.Descriptors, pid proc.PID) (fd.TabStopSetter, uint32, error) {
	for _, id := range []uint32{0, 1, 2} {
		info, err := descriptors.Terminal(ctx, uint64(pid), id)
		if err != nil {
			continue
		}
		if !info.TTY {
			continue
		}

		if setter, ok := descriptors.(fd.TabStopSetter); ok {
			return setter, id, nil
		}
		return nil, 0, errno.NotSupported
	}
	return nil, 0, errno.NoDevice
}

func isNumber(text string) bool {
	if text == "" {
		return false
	}
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
