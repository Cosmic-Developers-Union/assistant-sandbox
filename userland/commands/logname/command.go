package logname

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "logname")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		_, _ = fmt.Fprintln(h.Stderr, "logname: 不接受操作数")
		return interp.ExitStatus(2)
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	source, err := p.Resources.Credentials()
	if err != nil {
		return err
	}
	identity, err := source.Lookup(ctx, uint64(p.ID))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(h.Stdout, identity.Name)
	return err
}
