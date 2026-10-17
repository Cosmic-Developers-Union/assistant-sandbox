package cat

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "cat")
	number := flags.BoolP("number", "n", false, "为所有行编号")
	numberNonBlank := flags.BoolP("number-nonblank", "b", false, "仅为非空行编号")
	squeeze := flags.BoolP("squeeze-blank", "s", false, "压缩连续空行")
	showEnds := flags.BoolP("show-ends", "E", false, "行尾显示 $")
	showTabs := flags.BoolP("show-tabs", "T", false, "制表符显示为 ^I")
	showAll := flags.BoolP("show-all", "A", false, "等价于 -vET")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()
	if len(files) == 0 {
		files = []string{"-"}
	}
	if !*number && !*numberNonBlank && !*squeeze && !*showEnds && !*showTabs && !*showAll {
		return copyAll(ctx, h, files)
	}
	if *showAll {
		*showEnds, *showTabs = true, true
	}
	counter := 0
	previousBlank := false
	return commandio.Lines(ctx, files, func(_, line string, hasNewline bool) (bool, error) {
		blank := line == ""
		if *squeeze && blank && previousBlank {
			return true, nil
		}
		previousBlank = blank
		prefix := ""
		if *number || (*numberNonBlank && !blank) {
			counter++
			prefix = fmt.Sprintf("%6d\t", counter)
		}
		if *showTabs {
			line = strings.ReplaceAll(line, "\t", "^I")
		}
		if *showEnds && hasNewline {
			line += "$"
		}
		suffix := ""
		if hasNewline {
			suffix = "\n"
		}
		if _, err := io.WriteString(h.Stdout, prefix+line+suffix); err != nil {
			return false, err
		}
		return true, nil
	})
}

func copyAll(ctx context.Context, h interp.HandlerContext, files []string) error {
	for _, name := range files {
		var input = h.Stdin
		var closer io.Closer
		if name != "-" {
			file, err := commandio.Open(ctx, commandio.Path(h.Dir, name), vmfs.ReadOnly, 0)
			if err != nil {
				return err
			}
			input, closer = file, file
		}
		_, err := io.Copy(h.Stdout, input)
		if closer != nil {
			if closeErr := closer.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
