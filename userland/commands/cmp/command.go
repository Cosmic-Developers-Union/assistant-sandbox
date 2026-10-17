package cmp

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "cmp")
	silent := flags.BoolP("silent", "s", false, "只返回状态")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	names := flags.Args()
	if len(names) != 2 {
		return fmt.Errorf("需要两个文件")
	}
	inputs := make([]string, 0, 2)
	for _, name := range names {
		var content string
		if err := commandio.Inputs(ctx, []string{name}, func(_, input string) error {
			content = input
			return nil
		}); err != nil {
			return err
		}
		inputs = append(inputs, content)
	}
	left, right := inputs[0], inputs[1]
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			if *silent {
				return interp.ExitStatus(1)
			}
			if _, err := fmt.Fprintf(h.Stdout, "%s %s differ: byte %d, line %d\n",
				names[0], names[1], index+1, 1+countLines(left[:index])); err != nil {
				return err
			}
			return interp.ExitStatus(1)
		}
	}
	if len(left) != len(right) {
		if *silent {
			return interp.ExitStatus(1)
		}
		shorter, longer := names[0], names[1]
		if len(right) < len(left) {
			shorter, longer = names[1], names[0]
		}
		if _, err := fmt.Fprintf(h.Stdout, "cmp: EOF on %s after byte %d, in %s\n", shorter, limit, longer); err != nil {
			return err
		}
		return interp.ExitStatus(1)
	}
	return nil
}

func countLines(text string) int {
	count := 0
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' {
			count++
		}
	}
	return count
}
