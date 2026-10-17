package chgrp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

var (
	walkNodes = 4096
	walkDepth = 128
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "chgrp")
	recursive := flags.BoolP("recursive", "R", false, "递归处理目录")
	followCLI := flags.BoolP("follow-cli", "H", false, "递归时跟随命令行符号链接")
	followAll := flags.BoolP("follow-all", "L", false, "递归时跟随全部符号链接")
	quiet := flags.BoolP("quiet", "f", false, "静默诊断（退出码不变）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) < 2 {
		_, _ = fmt.Fprintln(h.Stderr, "chgrp: 需要 group 与 file 操作数")
		return interp.ExitStatus(2)
	}
	gid, err := strconv.ParseInt(operands[0], 10, 64)
	if err != nil {

		_, _ = fmt.Fprintf(h.Stderr, "chgrp: %s: 没有这个组（客体只接受数字 GID）\n", operands[0])
		return interp.ExitStatus(1)
	}
	if gid < 0 || gid > 0xFFFF_FFFF {
		_, _ = fmt.Fprintf(h.Stderr, "chgrp: %d: 属组越界\n", gid)
		return interp.ExitStatus(1)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	ownership, ok := system.(vmfs.Ownership)
	if !ok {

		_, _ = fmt.Fprintln(h.Stderr, "chgrp: 客体文件系统没有所有权能力")
		return interp.ExitStatus(1)
	}
	failures := 0
	for _, name := range operands[1:] {
		target := commandio.Path(h.Dir, name)
		var ferr error
		if *recursive {
			ferr = walkChange(ctx, system, ownership, target, func(ctx context.Context, p string) error {
				return ownership.Chown(ctx, p, vmfs.ChownUnchanged, gid)
			}, *followCLI, *followAll)
		} else {

			ferr = ownership.Chown(ctx, target, vmfs.ChownUnchanged, gid)
		}
		if ferr != nil {
			if !*quiet {
				_, _ = fmt.Fprintf(h.Stderr, "chgrp: %s: %v\n", name, ferr)
			}
			failures++
		}
	}
	if failures > 0 {
		return interp.ExitStatus(1)
	}
	return nil
}

func walkChange(ctx context.Context, system vmfs.System, _ vmfs.Ownership, root string, apply func(context.Context, string) error, _, followAll bool) error {
	remaining := walkNodes
	return walk(ctx, system, root, apply, &remaining, 0, followAll)
}

func walk(ctx context.Context, system vmfs.System, name string, apply func(context.Context, string) error, remaining *int, depth int, followAll bool) error {
	if *remaining <= 0 || depth > walkDepth {
		return fmt.Errorf("递归超过 %d 节点或 %d 层预算", walkNodes, walkDepth)
	}
	*remaining--
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := system.Lstat(ctx, name)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 && !followAll {

		return nil
	}
	if err := apply(ctx, name); err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := system.ReadDir(ctx, name)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if err := walk(ctx, system, path.Join(name, entry.Name()), apply, remaining, depth+1, followAll); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
