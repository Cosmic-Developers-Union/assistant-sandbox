package chown

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

type spec struct {
	uid, gid *int64
}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "chown")
	recursive := flags.BoolP("recursive", "R", false, "递归处理目录")
	followCLI := flags.BoolP("follow-cli", "H", false, "递归时跟随命令行符号链接")
	followAll := flags.BoolP("follow-all", "L", false, "递归时跟随全部符号链接")
	quiet := flags.BoolP("quiet", "f", false, "静默诊断（退出码不变）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) < 2 {
		_, _ = fmt.Fprintln(h.Stderr, "chown: 需要 owner[:group] 与 file 操作数")
		return interp.ExitStatus(2)
	}
	want, err := parseSpec(operands[0])
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "chown: %s: %v\n", operands[0], err)
		return interp.ExitStatus(1)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	ownership, ok := system.(vmfs.Ownership)
	if !ok {

		_, _ = fmt.Fprintln(h.Stderr, "chown: 客体文件系统没有所有权能力")
		return interp.ExitStatus(1)
	}
	failures := 0
	for _, name := range operands[1:] {
		target := commandio.Path(h.Dir, name)
		apply := func(ctx context.Context, p string) error {
			uid := vmfs.ChownUnchanged
			gid := vmfs.ChownUnchanged
			if want.uid != nil {
				uid = *want.uid
			}
			if want.gid != nil {
				gid = *want.gid
			}
			return ownership.Chown(ctx, p, uid, gid)
		}
		var ferr error
		if *recursive {
			ferr = walkChange(ctx, system, target, apply, *followCLI, *followAll)
		} else {

			ferr = apply(ctx, target)
		}
		if ferr != nil {
			if !*quiet {
				_, _ = fmt.Fprintf(h.Stderr, "chown: %s: %v\n", name, ferr)
			}
			failures++
		}
	}
	if failures > 0 {
		return interp.ExitStatus(1)
	}
	return nil
}

func parseSpec(text string) (spec, error) {
	ownerPart, groupPart, hasColon := cut(text, ":")
	result := spec{}
	if ownerPart != "" {
		uid, err := parseID(ownerPart)
		if err != nil {
			return result, fmt.Errorf("属主 %s: %w", ownerPart, err)
		}
		result.uid = &uid
	}
	if hasColon && groupPart != "" {
		gid, err := parseID(groupPart)
		if err != nil {
			return result, fmt.Errorf("属组 %s: %w", groupPart, err)
		}
		result.gid = &gid
	}
	if result.uid == nil && result.gid == nil {
		return result, fmt.Errorf("需要数字属主或 :数字属组（客体没有用户/组数据库，名字不可解析）")
	}
	return result, nil
}

func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

func parseID(text string) (int64, error) {
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("只接受数字 ID（名字没有解析途径）")
	}
	if value < 0 || value > 0xFFFF_FFFF {
		return 0, fmt.Errorf("ID 越界")
	}
	return value, nil
}

func walkChange(ctx context.Context, system vmfs.System, root string, apply func(context.Context, string) error, _, followAll bool) error {
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
