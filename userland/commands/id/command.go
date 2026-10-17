package id

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "id")
	group := flags.BoolP("group", "g", false, "只打印有效组 ID")
	user := flags.BoolP("user", "u", false, "只打印有效用户 ID")
	groups := flags.BoolP("groups", "G", false, "打印全部组 ID（客体只有一个声明组）")
	name := flags.BoolP("name", "n", false, "打印名字而不是数字（与 -u/-g/-G 同用）")

	_ = flags.BoolP("real", "r", false, "打印真实 ID（客体没有 real/effective 之分，同值）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if *groups && *group || *groups && *user || *group && *user {
		_, _ = fmt.Fprintln(h.Stderr, "id: -g、-u、-G 互斥")
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

	if len(operands) > 1 {
		_, _ = fmt.Fprintln(h.Stderr, "id: 只接受一个用户操作数")
		return interp.ExitStatus(2)
	}
	if len(operands) == 1 {
		operand := operands[0]
		if operand != identity.Name && operand != strconv.FormatUint(uint64(identity.UID), 10) {
			_, _ = fmt.Fprintf(h.Stderr, "id: %s: 没有这个用户\n", operand)
			return interp.ExitStatus(1)
		}
	}
	switch {
	case *user:
		if *name {
			_, err := fmt.Fprintf(h.Stdout, "%s\n", identity.Name)
			return err
		}
		_, err := fmt.Fprintf(h.Stdout, "%d\n", identity.UID)
		return err
	case *group:

		if *name {
			_, _ = fmt.Fprintln(h.Stderr, "id: 客体没有组数据库，组名不可得")
			return interp.ExitStatus(1)
		}
		_, err := fmt.Fprintf(h.Stdout, "%d\n", identity.GID)
		return err
	case *groups:
		if *name {
			_, _ = fmt.Fprintln(h.Stderr, "id: 客体没有组数据库，组名不可得")
			return interp.ExitStatus(1)
		}
		_, err := fmt.Fprintf(h.Stdout, "%d\n", identity.GID)
		return err
	default:
		_, err := fmt.Fprintf(h.Stdout, "uid=%d(%s) gid=%d\n", identity.UID, identity.Name, identity.GID)
		return err
	}
}
