package mv

import (
	"context"
	"fmt"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	flags := commandio.Flags(ctx, "mv")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	a := flags.Args()
	if len(a) < 2 {
		return fmt.Errorf("mv 需要源和目标")
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	dir := commandio.Directory(ctx)
	target := commandio.Path(dir, a[len(a)-1])
	info, statErr := system.Stat(ctx, target)
	if len(a) > 2 && (statErr != nil || !info.IsDir()) {
		return fmt.Errorf("mv 多个源需要目标目录")
	}
	for _, source := range a[:len(a)-1] {
		from := commandio.Path(dir, source)
		to := target
		if statErr == nil && info.IsDir() {
			to = path.Join(target, path.Base(from))
		}
		if err := system.Rename(ctx, from, to); err != nil {
			return err
		}
	}
	return nil
}
