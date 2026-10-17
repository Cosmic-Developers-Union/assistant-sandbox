package man

import (
	"context"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "man")
	keyword := flags.BoolP("keyword", "k", false, "按关键字检索 whatis（apropos 惯例）")
	what := flags.BoolP("whatis", "f", false, "按名字检索 whatis（whatis 惯例）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()

	for len(operands) > 1 && isSection(operands[0]) {
		operands = operands[1:]
	}
	if len(operands) == 0 {
		_, _ = fmt.Fprintln(h.Stderr, "man: 需要手册页名字")
		return interp.ExitStatus(2)
	}
	if *keyword || *what {
		return whatis(ctx, h, operands, *keyword)
	}
	return display(ctx, h, operands)
}

func display(ctx context.Context, h interp.HandlerContext, names []string) error {
	missing := false
	for _, name := range names {
		page, ok := lookup(name)
		if !ok {
			_, _ = fmt.Fprintf(h.Stderr, "man: %s: 没有手册页条目\n", name)
			missing = true
			continue
		}
		_, _ = fmt.Fprint(h.Stdout, page.render(currentWidth(ctx)))
	}
	if missing {
		return interp.ExitStatus(1)
	}
	return nil
}

func whatis(ctx context.Context, h interp.HandlerContext, names []string, keyword bool) error {
	found := false
	for _, name := range names {
		for _, entry := range whatisEntries() {
			var matched bool
			if keyword {
				matched = strings.Contains(strings.ToLower(entry.Name), strings.ToLower(name)) ||
					strings.Contains(strings.ToLower(entry.Summary), strings.ToLower(name))
			} else {
				matched = entry.Name == name
			}
			if matched {
				_, _ = fmt.Fprintf(h.Stdout, "%s (%s) - %s\n", entry.Name, entry.Section, entry.Summary)
				found = true
			}
		}
	}
	if !found {
		return interp.ExitStatus(1)
	}
	return nil
}

func isSection(text string) bool {
	for _, c := range text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return text != ""
}

func currentWidth(ctx context.Context) int {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return 66
	}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return 66
	}
	for _, id := range []uint32{0, 1, 2} {
		info, err := descriptors.Terminal(ctx, uint64(p.ID), id)
		if err == nil && info.TTY && info.Columns > 20 {
			return int(info.Columns)
		}
	}
	return 66
}
