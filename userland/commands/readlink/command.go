package readlink

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

func outsideBinding(err error) bool {
	return errors.Is(err, errno.Permission) || errors.Is(err, fs.ErrPermission)
}

const maxLinks = 40

type existenceMode int

const (
	existing existenceMode = iota

	followLast

	permissive
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "readlink")
	noNewline := flags.BoolP("no-newline", "n", false, "不输出末尾换行")
	canonical := flags.BoolP("canonicalize", "f", false, "规范化：除最后一个组件外都要存在")
	canonicalExisting := flags.BoolP("canonicalize-existing", "e", false, "规范化：每个组件都要存在")
	canonicalMissing := flags.BoolP("canonicalize-missing", "m", false, "规范化：不要求存在")
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
	mode := followLast
	switch {
	case *canonicalExisting:
		mode = existing
	case *canonicalMissing:
		mode = permissive
	}
	normalize := *canonical || *canonicalExisting || *canonicalMissing
	failed := false
	for _, name := range names {
		target := commandio.Path(h.Dir, name)
		if normalize {
			resolved, err := canonicalize(ctx, system, target, mode)
			if err != nil {

				failed = true
				continue
			}
			target = resolved
		} else {
			target, err = system.Readlink(ctx, target)
			if err != nil {
				if errors.Is(err, errno.Invalid) {
					failed = true
					continue
				}
				return err
			}
		}
		if _, err := fmt.Fprint(h.Stdout, target); err != nil {
			return err
		}
		if !*noNewline {
			if _, err := fmt.Fprintln(h.Stdout); err != nil {
				return err
			}
		}
	}
	if failed {
		return interp.ExitStatus(1)
	}
	return nil
}

func canonicalize(ctx context.Context, system vmfs.System, name string, mode existenceMode) (string, error) {
	if !path.IsAbs(name) {
		name = path.Join(commandio.Directory(ctx), name)
	}
	pending := strings.Split(name, "/")
	resolved := "/"
	links := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			resolved = path.Dir(resolved)
			continue
		}
		candidate := path.Join(resolved, component)
		info, err := system.Lstat(ctx, candidate)
		if err != nil {
			switch {
			case outsideBinding(err):
				resolved = candidate

			case mode == permissive || (mode == followLast && len(pending) == 0):
				resolved = candidate
			default:
				return "", err
			}
			continue
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			resolved = candidate
			continue
		}
		links++
		if links > maxLinks {
			return "", errno.Wrap(errno.LinkLoop, "符号链接层数过多")
		}
		target, err := system.Readlink(ctx, candidate)
		if err != nil {
			return "", err
		}
		rest := pending
		if path.IsAbs(target) {
			resolved = "/"
			target = strings.TrimPrefix(target, "/")
		}
		pending = append(strings.Split(target, "/"), rest...)
	}
	return path.Clean(resolved), nil
}
