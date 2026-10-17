package head

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "head")
	count := flags.IntP("lines", "n", 10, "显示行数")
	bytes := flags.Int64P("bytes", "c", -1, "显示字节数")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()
	if *bytes >= 0 {
		if *bytes > 1<<20 {
			return fmt.Errorf("字节数须为 0..1048576")
		}

		return commandio.Stream(ctx, files, func(_ string, reader io.Reader) error {
			if _, err := io.CopyN(h.Stdout, reader, *bytes); err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		})
	}
	if *count < 0 || *count > 1<<20 {
		return fmt.Errorf("行数须为 0..1048576")
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	for _, file := range files {

		remaining := *count
		err := commandio.Lines(ctx, []string{file}, func(_, line string, hasNewline bool) (bool, error) {
			if remaining == 0 {

				return false, nil
			}
			suffix := "\n"
			if !hasNewline {
				suffix = ""
			}
			if _, err := io.WriteString(h.Stdout, line+suffix); err != nil {
				return false, err
			}
			remaining--
			return remaining > 0, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
