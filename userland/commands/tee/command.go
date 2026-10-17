package tee

import (
	"context"
	"io"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "tee")
	appendMode := false
	flags.BoolVarP(&appendMode, "append", "a", false, "追加文件")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()

	input, err := commandio.ReadText(ctx, h.Stdin)
	if err != nil {
		return err
	}
	if _, err = io.WriteString(h.Stdout, input); err != nil {
		return err
	}
	for _, file := range files {
		mode := vmfs.Create | vmfs.WriteOnly | vmfs.Truncate
		if appendMode {
			mode = vmfs.Create | vmfs.WriteOnly | vmfs.Append
		}
		f, err := commandio.Open(ctx, commandio.Path(h.Dir, file), mode, 0o666)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, input)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
