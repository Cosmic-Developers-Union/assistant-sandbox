package getconf

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
	flags := commandio.Flags(ctx, "getconf")
	all := flags.BoolP("all", "a", false, "列出客体已声明的全部配置事实")
	_ = flags.StringP("specification", "v", "", "实现规范名（客体只有一套规格，接受并忽略）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if *all && len(operands) > 0 {
		_, _ = fmt.Fprintln(h.Stderr, "getconf: -a 不接受变量名")
		return interp.ExitStatus(2)
	}
	if len(operands) == 0 && !*all {
		_, _ = fmt.Fprintln(h.Stderr, "用法: getconf system_var | getconf -a | getconf -v specification system_var")
		return interp.ExitStatus(2)
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	control, ok := p.Control.(proc.SystemControl)
	if !ok {
		_, _ = fmt.Fprintln(h.Stderr, "getconf: 客体没有系统配置事实")
		return interp.ExitStatus(1)
	}
	config, err := control.SystemConfiguration()
	if err != nil {
		return err
	}

	facts := []struct {
		name   string
		value  int64
		filled bool
	}{
		{"CHILD_MAX", config.ChildMax, config.ChildMax > 0},
		{"ARG_MAX", config.ArgMax, config.ArgMax > 0},
	}
	if *all {
		for _, fact := range facts {
			if !fact.filled {
				continue
			}
			if _, err := fmt.Fprintf(h.Stdout, "%s %s\n", fact.name, strconv.FormatInt(fact.value, 10)); err != nil {
				return err
			}
		}
		return nil
	}
	name := operands[0]
	if len(operands) > 1 {

		return noFact(h, name)
	}
	for _, fact := range facts {
		if fact.name == name && fact.filled {
			_, err := fmt.Fprintln(h.Stdout, strconv.FormatInt(fact.value, 10))
			return err
		}
	}
	return noFact(h, name)
}

func noFact(h interp.HandlerContext, name string) error {
	_, _ = fmt.Fprintf(h.Stderr, "getconf: %s: 客体没有该配置事实\n", name)
	return interp.ExitStatus(1)
}
