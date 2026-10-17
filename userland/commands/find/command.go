package find

import (
	"context"
	"fmt"
	"io/fs"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	root, pattern, kind := ".", "*", ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		root = args[0]
		args = args[1:]
	}
	for len(args) > 0 {
		if len(args) < 2 {
			return fmt.Errorf("find 参数缺少值")
		}
		switch args[0] {
		case "-name":
			pattern = args[1]
		case "-type":
			kind = args[1]
			if kind != "f" && kind != "d" {
				return fmt.Errorf("find 不支持该文件类型")
			}
		default:
			return fmt.Errorf("find 未支持参数%s", args[0])
		}
		args = args[2:]
	}
	h := commandio.Context(ctx)
	return commandio.Walk(ctx, commandio.Path(h.Dir, root), func(name string, e fs.DirEntry) error {
		match, err := path.Match(pattern, e.Name())
		if err != nil {
			return err
		}
		if !match || kind == "d" && !e.IsDir() || kind == "f" && e.IsDir() {
			return nil
		}
		_, err = fmt.Fprintln(h.Stdout, name)
		return err
	})
}
