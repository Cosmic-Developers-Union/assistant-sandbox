package jobs

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "jobs")
	flags.BoolP("list", "l", false, "逐项列出（与缺省一致：没有作业）")
	flags.BoolP("pid", "p", false, "只列进程 ID（与缺省一致：没有作业）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) == 0 {
		return nil
	}
	for _, operand := range operands {
		_, _ = fmt.Fprintf(h.Stderr, "jobs: %s: 没有这样的作业\n", operand)
	}
	return interp.ExitStatus(1)
}
