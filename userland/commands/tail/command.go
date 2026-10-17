package tail

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "tail")
	lines := flags.StringP("lines", "n", "10", "行数；+N 表示从第 N 行开始")
	bytes := flags.Int64P("bytes", "c", -1, "字节数")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()
	if *bytes >= 0 {
		if *bytes > 1<<20 {
			return fmt.Errorf("字节数须为 0..1048576")
		}
		return commandio.Inputs(ctx, files, func(_, input string) error {
			if int64(len(input)) > *bytes {
				input = input[int64(len(input))-*bytes:]
			}
			_, err := io.WriteString(h.Stdout, input)
			return err
		})
	}
	from := int64(-1)
	count, err := strconv.ParseInt(strings.TrimPrefix(*lines, "+"), 10, 64)
	if err != nil || count < 0 || count > 1<<20 {
		return fmt.Errorf("行数须为 0..1048576，或以 + 开头的起始行")
	}
	if strings.HasPrefix(*lines, "+") {
		if count < 1 {
			return fmt.Errorf("起始行须从 1 开始")
		}
		from = count - 1
	}
	return commandio.Inputs(ctx, files, func(_, input string) error {
		rows := commandio.Rows(input)
		start := 0
		if from >= 0 {
			if from > int64(len(rows)) {
				return nil
			}
			start = int(from)
		} else if int64(len(rows)) > count {
			start = len(rows) - int(count)
		}
		for index := start; index < len(rows); index++ {
			suffix := "\n"
			if index == len(rows)-1 && !strings.HasSuffix(input, "\n") {
				suffix = ""
			}
			if _, err := io.WriteString(h.Stdout, rows[index]+suffix); err != nil {
				return err
			}
		}
		return nil
	})
}
