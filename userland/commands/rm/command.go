package rm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func Run(ctx context.Context, args []string) error {
	flags := commandio.Flags(ctx, "rm")
	recursive, force := false, false
	flags.BoolVarP(&recursive, "recursive", "r", false, "递归")
	flags.BoolVar(&recursive, "R", false, "递归")
	flags.BoolVarP(&force, "force", "f", false, "忽略不存在")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) == 0 && !force {
		return fmt.Errorf("rm 缺少文件")
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	for _, name := range flags.Args() {
		if err := remove(ctx, system, commandio.Path(commandio.Directory(ctx), name), recursive, force); err != nil {
			return err
		}
	}
	return nil
}
func remove(ctx context.Context, system vmfs.System, name string, recursive, force bool) error {
	remaining := 4096
	return removeNode(ctx, system, name, recursive, force, &remaining, 0)
}
func removeNode(ctx context.Context, system vmfs.System, name string, recursive, force bool, remaining *int, depth int) error {
	if *remaining <= 0 || depth > 128 {
		return fmt.Errorf("rm 超过 VM 递归预算")
	}
	*remaining--
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "/" {
		return fmt.Errorf("rm 拒绝删除根目录")
	}
	info, err := system.Stat(ctx, name)
	if err != nil {
		if force && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		if !recursive {
			return fmt.Errorf("rm 目录需要 -r")
		}
		entries, err := system.ReadDir(ctx, name)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := removeNode(ctx, system, path.Join(name, e.Name()), recursive, force, remaining, depth+1); err != nil {
				return err
			}
		}
	}
	return system.Remove(ctx, name)
}
