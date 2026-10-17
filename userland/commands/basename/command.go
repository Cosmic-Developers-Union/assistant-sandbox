package basename

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "basename")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()
	if len(files) == 0 || len(files) > 2 {
		return fmt.Errorf("路径参数无效")
	}
	value := path.Base(files[0])
	if len(files) == 2 {
		value = strings.TrimSuffix(value, files[1])
	}
	_, err := fmt.Fprintln(h.Stdout, value)
	return err
}
