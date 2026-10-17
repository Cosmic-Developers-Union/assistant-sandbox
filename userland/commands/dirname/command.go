package dirname

import (
	"context"
	"fmt"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "dirname")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()
	if len(files) == 0 {
		return fmt.Errorf("路径参数无效")
	}
	for _, file := range files {
		if _, err := fmt.Fprintln(h.Stdout, path.Dir(file)); err != nil {
			return err
		}
	}
	return nil
}
