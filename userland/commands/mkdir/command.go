package mkdir

import (
	"context"
	"fmt"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func Run(ctx context.Context, args []string) error {
	flags := commandio.Flags(ctx, "mkdir")
	parents := flags.BoolP("parents", "p", false, "创建父目录")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) == 0 {
		return fmt.Errorf("mkdir 缺少目录")
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	for _, name := range flags.Args() {
		name = commandio.Path(commandio.Directory(ctx), name)
		if err := create(ctx, system, name, *parents); err != nil {
			return err
		}
	}
	return nil
}
func create(ctx context.Context, system vmfs.System, name string, parents bool) error {
	if !parents {
		return system.Mkdir(ctx, name, 0755)
	}
	if info, err := system.Stat(ctx, name); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("mkdir 路径不是目录")
		}
		return nil
	}
	parent := path.Dir(name)
	if parent == name {
		return fmt.Errorf("mkdir 根目录不存在")
	}
	if err := create(ctx, system, parent, true); err != nil {
		return err
	}
	return system.Mkdir(ctx, name, 0755)
}
