package newgrp

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	flags := commandio.Flags(ctx, "newgrp")
	flags.BoolP("login", "l", false, "重建登录会话（客体没有登录会话，接受为 no-op）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) > 1 {
		h := commandio.Context(ctx)
		_, _ = fmt.Fprintln(h.Stderr, "newgrp: 只接受一个组操作数")
		return interp.ExitStatus(2)
	}
	identity, err := p.Resources.Credentials()
	if err != nil {
		return err
	}
	current, err := identity.Lookup(ctx, uint64(p.ID))
	if err != nil {
		return err
	}
	if len(operands) == 1 {
		gid, err := strconv.ParseInt(operands[0], 10, 64)
		if err != nil {
			h := commandio.Context(ctx)

			_, _ = fmt.Fprintf(h.Stderr, "newgrp: %s: 没有这个组\n", operands[0])
			return interp.ExitStatus(1)
		}
		if gid < 0 || gid > 0xFFFF_FFFF {
			h := commandio.Context(ctx)
			_, _ = fmt.Fprintf(h.Stderr, "newgrp: %s: 没有这个组\n", operands[0])
			return interp.ExitStatus(1)
		}
		if uint32(gid) != current.GID {
			h := commandio.Context(ctx)
			_, _ = fmt.Fprintf(h.Stderr, "newgrp: %d: 客体只有声明身份的那一个组（GID %d）\n", gid, current.GID)
			return interp.ExitStatus(1)
		}
	}

	h := commandio.Context(ctx)
	exited, runErr := p.Control.Run(ctx, proc.RunSpec{
		Parent:  p.ID,
		Name:    "bash",
		Args:    nil,
		Dir:     h.Dir,
		Env:     p.Env,
		Streams: fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr},
	})
	if runErr != nil {
		switch {
		case errors.Is(runErr, errno.NotFound):

			_, _ = fmt.Fprintf(h.Stderr, "newgrp: 未注册沙箱命令: bash\n")
			return interp.ExitStatus(127)
		case errors.Is(runErr, errno.NoExec), errors.Is(runErr, errno.Permission):
			_, _ = fmt.Fprintln(h.Stderr, "newgrp: bash: 无法执行")
			return interp.ExitStatus(126)
		}
		return fmt.Errorf("newgrp: 执行 bash: %w", runErr)
	}
	if exited.ExitCode != 0 {
		return interp.ExitStatus(int(exited.ExitCode))
	}
	return nil
}
