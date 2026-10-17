package rmdir

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "rmdir")
	parents := flags.BoolP("parents", "p", false, "逐级删除父目录")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	names := flags.Args()
	if len(names) == 0 {
		return fmt.Errorf("缺少操作数")
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		current := commandio.Path(h.Dir, name)
		for {
			if err := system.Remove(ctx, current); err != nil {
				if errors.Is(err, errno.NotDirectory) {
					return fmt.Errorf("%s: 不是目录", current)
				}
				return err
			}
			if !*parents {
				break
			}
			parent := path.Dir(current)
			if parent == current || parent == "/" || parent == "." {
				break
			}
			current = parent
		}
	}
	return nil
}
