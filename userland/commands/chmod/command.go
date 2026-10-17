package chmod

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {

	if system, err := commandio.Filesystem(ctx); err == nil {
		release := vfs.Bind(system, commandio.Directory(ctx), nil)
		defer release()
	}
	flags := commandio.Flags(ctx, "chmod")
	recursive := flags.BoolP("recursive", "R", false, "递归处理目录")
	silent := flags.BoolP("silent", "f", false, "不报告大多数错误")
	verbose := flags.BoolP("verbose", "v", false, "对每个处理过的文件输出诊断")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) < 2 {
		return fmt.Errorf("chmod 需要 mode 与至少一个文件")
	}
	mode := operands[0]
	files := operands[1:]
	failed := false
	diagnostic := commandio.Context(ctx).Stdout

	umask := fs.FileMode(0)
	if p, err := proc.FromContext(ctx); err == nil {
		if current, err := p.Resources.Umask(); err == nil {
			umask = current
		}
	}
	for _, name := range files {
		if err := apply(ctx, name, mode, *recursive, *verbose, diagnostic, umask); err != nil {

			failed = true
			if !*silent {
				_, _ = fmt.Fprintf(commandio.Context(ctx).Stderr, "chmod: %s: %v\n", name, err)
			}
		}
	}
	if failed {
		return interp.ExitStatus(1)
	}
	return nil
}

func apply(ctx context.Context, name, mode string, recursive, verbose bool, diagnostic io.Writer, umask fs.FileMode) error {
	info, err := vfs.Stat(name)
	if err != nil {
		return err
	}
	if err := change(name, mode, info, verbose, diagnostic, umask); err != nil {
		return err
	}
	if !recursive || !info.IsDir() {
		return nil
	}
	return walk(ctx, name, mode, verbose, diagnostic, umask)
}

func walk(ctx context.Context, dir, mode string, verbose bool, diagnostic io.Writer, umask fs.FileMode) error {
	entries, err := vfs.ReadDir(dir)
	if err != nil {
		return err
	}
	var firstErr error
	for _, entry := range entries {
		child := dir + "/" + entry.Name()
		info, err := vfs.Lstat(child)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		if info.IsDir() {
			if err := walk(ctx, child, mode, verbose, diagnostic, umask); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := change(child, mode, info, verbose, diagnostic, umask); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func change(name, mode string, info fs.FileInfo, verbose bool, diagnostic io.Writer, umask fs.FileMode) error {
	target, err := resolveMode(mode, info, umask)
	if err != nil {
		return err
	}
	if err := vfs.Chmod(name, target); err != nil {
		return err
	}
	if verbose {
		if _, err := fmt.Fprintf(diagnostic, "mode of %s 已更改\n", name); err != nil {
			return err
		}
	}
	return nil
}

func resolveMode(text string, info fs.FileInfo, umask fs.FileMode) (fs.FileMode, error) {
	current := info.Mode()
	if isOctal(text) {
		value, err := strconv.ParseUint(text, 8, 32)
		if err != nil || value > 0o7777 {
			return 0, fmt.Errorf("模式无效: %s", text)
		}
		return octalToFileMode(value), nil
	}
	target := current
	for _, clause := range strings.Split(text, ",") {
		next, err := applyClause(clause, target, info, umask)
		if err != nil {
			return 0, err
		}
		target = next
	}
	return target, nil
}

func applyClause(clause string, current fs.FileMode, info fs.FileInfo, umask fs.FileMode) (fs.FileMode, error) {
	if clause == "" {
		return 0, fmt.Errorf("模式无效: 空子句")
	}

	index := 0
	who, whoOmitted := fs.FileMode(0), false
	for index < len(clause) && strings.ContainsRune("ugoa", rune(clause[index])) {
		switch clause[index] {
		case 'u':
			who |= 0o700 | fs.ModeSetuid
		case 'g':
			who |= 0o070 | fs.ModeSetgid
		case 'o':
			who |= 0o007 | fs.ModeSticky
		case 'a':
			who |= 0o777 | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
		}
		index++
	}
	if index == 0 {

		who, whoOmitted = (0o777|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)&^umask, true
	}
	if index >= len(clause) || !strings.ContainsRune("+-=", rune(clause[index])) {
		return 0, fmt.Errorf("模式无效: %s", clause)
	}
	op := clause[index]
	perm := clause[index+1:]
	bits, err := permissionBits(perm, current, info, who, whoOmitted)
	if err != nil {
		return 0, err
	}
	affected := bits & who
	switch op {
	case '+':
		return current | affected, nil
	case '-':
		return current &^ affected, nil
	default:
		return (current &^ who) | affected, nil
	}
}

func permissionBits(perm string, current fs.FileMode, info fs.FileInfo, who fs.FileMode, whoOmitted bool) (fs.FileMode, error) {
	var bits fs.FileMode
	executable := info.IsDir() || current.Perm()&0o111 != 0
	for _, c := range perm {
		switch c {
		case 'r':
			bits |= 0o444
		case 'w':
			bits |= 0o222
		case 'x':
			bits |= 0o111
		case 'X':

			if executable {
				bits |= 0o111
			}
		case 's':

			if who&fs.ModeSetuid != 0 {
				bits |= fs.ModeSetuid
			}
			if who&fs.ModeSetgid != 0 {
				bits |= fs.ModeSetgid
			}
		case 't':
			if whoOmitted || who&fs.ModeSticky != 0 {
				bits |= fs.ModeSticky
			}
		case 'u', 'g', 'o':
			bits |= copyBits(c, current)
		default:
			return 0, fmt.Errorf("权限字符无效: %c", c)
		}
	}
	return bits, nil
}

func copyBits(from rune, current fs.FileMode) fs.FileMode {
	perm := current.Perm()
	var source fs.FileMode
	switch from {
	case 'u':
		source = (perm >> 6) & 0o7
	case 'g':
		source = (perm >> 3) & 0o7
	default:
		source = perm & 0o7
	}
	return source<<6 | source<<3 | source
}

func isOctal(text string) bool {
	if text == "" {
		return false
	}
	for _, c := range text {
		if c < '0' || c > '7' {
			return false
		}
	}
	return true
}

func octalToFileMode(value uint64) fs.FileMode {
	mode := fs.FileMode(value & 0o777)
	if value&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	if value&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if value&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}
