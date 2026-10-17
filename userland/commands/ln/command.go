package ln

import (
	"context"
	"errors"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "ln")
	symbolic := flags.BoolP("symbolic", "s", false, "建立符号链接")
	force := flags.BoolP("force", "f", false, "覆盖已存在的目标")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) != 2 {
		return fmt.Errorf("需要 目标 链接名")
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	target := commandio.Path(h.Dir, operands[0])
	link := commandio.Path(h.Dir, operands[1])
	if *force {
		if err := system.Remove(ctx, link); err != nil && !errors.Is(err, errno.NotFound) {
			return err
		}
	}
	if *symbolic {

		return system.Symlink(ctx, operands[0], link)
	}
	_ = target
	return system.Link(ctx, target, link)
}
