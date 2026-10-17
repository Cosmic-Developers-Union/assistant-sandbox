package fg

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "fg")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(h.Stderr, "fg: 客体没有作业控制（shell 不启动后台任务）")
	return interp.ExitStatus(1)
}
