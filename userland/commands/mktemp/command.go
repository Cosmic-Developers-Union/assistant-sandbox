package mktemp

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func Run(ctx context.Context, args []string) error {
	flags := commandio.Flags(ctx, "mktemp")
	directory := flags.BoolP("directory", "d", false, "创建目录")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("mktemp 尚未支持模板参数")
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	random, err := p.Resources.Random()
	if err != nil {
		return err
	}
	system, err := p.Resources.Filesystem()
	if err != nil {
		return err
	}
	h := commandio.Context(ctx)
	dir := h.Env.Get("TMPDIR").String()
	if dir == "" {
		dir = h.Dir
	}
	for attempt := 0; attempt < 64; attempt++ {
		raw := make([]byte, 8)
		if n, err := random.Read(ctx, raw); err != nil {
			return err
		} else if n != len(raw) {
			return io.ErrUnexpectedEOF
		}
		name := path.Join(dir, "tmp."+hex.EncodeToString(raw))
		if *directory {
			err = system.Mkdir(ctx, name, 0700)
		} else {
			var file vmfs.File
			file, err = system.Open(ctx, name, vmfs.Create|vmfs.Exclusive|vmfs.WriteOnly, 0600)
			if err == nil {
				err = file.Close()
			}
		}
		if err == nil {
			_, err = fmt.Fprintln(h.Stdout, name)
			return err
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return fmt.Errorf("mktemp 无法创建排他路径")
}
