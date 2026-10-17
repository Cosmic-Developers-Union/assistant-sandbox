package crontab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const spoolDir = "/var/cron/tabs"

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "crontab")
	edit := flags.BoolP("edit", "e", false, "编辑 crontab（客体没有编辑器，不支持）")
	list := flags.BoolP("list", "l", false, "列出 crontab")
	remove := flags.BoolP("remove", "r", false, "删除 crontab")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if *list && (*edit || *remove) || *edit && *remove {
		_, _ = fmt.Fprintln(h.Stderr, "crontab: -e、-l、-r 最多给一个")
		return interp.ExitStatus(2)
	}
	operands := flags.Args()
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}

	credentials, err := p.Resources.Credentials()
	if err != nil {
		return err
	}
	self, err := credentials.Lookup(ctx, uint64(p.ID))
	if err != nil {
		return err
	}
	if *edit {

		_, _ = fmt.Fprintln(h.Stderr, "crontab: 客体没有编辑器，-e 不支持")
		return interp.ExitStatus(1)
	}
	switch {
	case *list || *remove:
		if len(operands) > 1 {
			_, _ = fmt.Fprintln(h.Stderr, "crontab: 安装文件不能与 -l/-r 同给")
			return interp.ExitStatus(2)
		}
		user := self.Name
		if len(operands) == 1 {
			user = operands[0]
			if user != self.Name {
				_, _ = fmt.Fprintf(h.Stderr, "crontab: %s: 没有这个用户\n", user)
				return interp.ExitStatus(1)
			}
		}
		if *list {
			return listSpool(ctx, h, user)
		}
		return removeSpool(ctx, h, user)
	default:
		if len(operands) > 1 {
			_, _ = fmt.Fprintln(h.Stderr, "crontab: 至多一个安装文件操作数")
			return interp.ExitStatus(2)
		}
		return install(ctx, h, p, self.Name, operands)
	}
}

func install(ctx context.Context, h interp.HandlerContext, p proc.Proc, user string, operands []string) error {
	var content string
	if len(operands) == 0 || operands[0] == "-" {
		text, err := commandio.ReadText(ctx, h.Stdin)
		if err != nil {
			return err
		}
		content = text
	} else {
		file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), operands[0]), vmfs.ReadOnly, 0)
		if err != nil {
			return err
		}
		content, err = commandio.ReadText(ctx, file)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	if err := mkdirAll(ctx, system, spoolDir); err != nil {
		return err
	}

	var temporary string
	for attempt := 0; attempt < 64; attempt++ {
		candidate := path.Join(spoolDir, fmt.Sprintf(".%s.tmp.%d.%d", user, p.ID, attempt))
		file, err := system.Open(ctx, candidate, vmfs.Create|vmfs.Exclusive|vmfs.WriteOnly, 0o600)
		if err != nil {
			if exists(err) {
				continue
			}
			return err
		}
		_, err = io.WriteString(file, content)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		temporary = candidate
		break
	}
	if temporary == "" {
		return fmt.Errorf("crontab: 无法创建临时 spool 文件")
	}
	if err := system.Rename(ctx, temporary, path.Join(spoolDir, user)); err != nil {

		return errors.Join(err, system.Remove(ctx, temporary))
	}
	return nil
}

func listSpool(ctx context.Context, h interp.HandlerContext, user string) (closeResult0 error) {
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	file, err := system.Open(ctx, path.Join(spoolDir, user), vmfs.ReadOnly, 0)
	if err != nil {
		if missing(err) {
			_, _ = fmt.Fprintf(h.Stderr, "crontab: %s: 没有 crontab\n", user)
			return interp.ExitStatus(1)
		}
		return err
	}
	defer func() {
		closeResult0 = errors.Join(closeResult0, file.Close())
	}()

	_, err = io.Copy(h.Stdout, file)
	return err
}

func removeSpool(ctx context.Context, h interp.HandlerContext, user string) error {
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	if err := system.Remove(ctx, path.Join(spoolDir, user)); err != nil {
		if missing(err) {
			_, _ = fmt.Fprintf(h.Stderr, "crontab: %s: 没有 crontab\n", user)
			return interp.ExitStatus(1)
		}
		return err
	}
	return nil
}

func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, errno.NotFound)
}

func exists(err error) bool {
	return errors.Is(err, fs.ErrExist) || errors.Is(err, errno.Exist)
}

func mkdirAll(ctx context.Context, system vmfs.System, name string) error {
	info, err := system.Stat(ctx, name)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("crontab: %s: 不是目录", name)
		}
		return nil
	}
	if !missing(err) {
		return err
	}
	if parent := path.Dir(name); parent != name {
		if err := mkdirAll(ctx, system, parent); err != nil {
			return err
		}
	}
	if err := system.Mkdir(ctx, name, 0o755); err != nil {

		if info, statErr := system.Stat(ctx, name); statErr == nil && info.IsDir() {
			return nil
		}
		return err
	}
	return nil
}
