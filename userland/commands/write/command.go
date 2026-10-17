package write

import (
	"context"
	"fmt"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const terminalPath = "/dev/tty"

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "write")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) < 1 || len(operands) > 2 {
		_, _ = fmt.Fprintln(h.Stderr, "write: 需要 user 操作数（可带 tty）")
		return interp.ExitStatus(2)
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	identity, err := p.Resources.Credentials()
	if err != nil {
		return err
	}
	current, err := identity.Lookup(ctx, uint64(p.ID))
	if err != nil {
		return err
	}
	if operands[0] != current.Name {

		_, _ = fmt.Fprintf(h.Stderr, "write: %s: 没有登录\n", operands[0])
		return interp.ExitStatus(1)
	}
	term := terminalPath
	if len(operands) == 2 {
		if operands[1] != terminalPath {
			_, _ = fmt.Fprintf(h.Stderr, "write: %s: 客体只有这一个终端路径\n", operands[1])
			return interp.ExitStatus(1)
		}
	}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return err
	}
	terminal, err := terminalStream(ctx, descriptors, p.ID)
	if err != nil {
		return err
	}
	if terminal == nil {
		_, _ = fmt.Fprintf(h.Stderr, "write: %s: 终端不可用\n", current.Name)
		return interp.ExitStatus(1)
	}
	if _, err := fmt.Fprintf(terminal, "Message from %s on %s...\n", current.Name, term); err != nil {
		return err
	}

	if _, err := io.Copy(terminal, io.LimitReader(h.Stdin, 4<<20+1)); err != nil {
		return err
	}
	return nil
}

func terminalStream(ctx context.Context, descriptors fd.Descriptors, pid proc.PID) (io.WriteCloser, error) {
	for _, id := range []uint32{0, 1, 2} {
		info, err := descriptors.Terminal(ctx, uint64(pid), id)
		if err != nil {
			return nil, err
		}
		if !info.TTY {
			continue
		}
		stream, err := descriptors.Get(ctx, uint64(pid), id)
		if err != nil {
			return nil, err
		}
		return stream, nil
	}
	return nil, nil
}
