package lp

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "lp")
	destination := flags.StringP("destination", "d", "lp0", "打印目的地")
	copies := flags.IntP("copies", "n", 1, "份数")
	mail := flags.BoolP("mail", "m", false, "完成后邮件通知")
	quiet := flags.BoolP("quiet", "s", false, "静默（不打印请求号）")
	title := flags.StringP("title", "t", "", "作业标题")
	notify := flags.BoolP("notify", "w", false, "完成后写终端通知")
	copyContent := flags.BoolP("copy", "c", false, "复制内容而不是引用文件（POSIX -c）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	scheduler, ok := p.Control.(proc.Scheduler)
	if !ok {
		_, _ = fmt.Fprintln(h.Stderr, "lp: 客体没有打印队列")
		return interp.ExitStatus(1)
	}
	spec := proc.PrintSpec{
		Destination: *destination,
		Copies:      *copies,
		Title:       *title,
		Copy:        *copyContent,
		Notify:      *mail,
		Write:       *notify,
	}
	if len(operands) == 0 {

		data, err := io.ReadAll(h.Stdin)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "lp: %v\n", err)
			return interp.ExitStatus(1)
		}
		spec.Copy = true
		spec.Body = string(data)
	} else if len(operands) == 1 && *copyContent {

		system, err := p.Resources.Filesystem()
		if err != nil {
			return err
		}
		file, err := system.Open(ctx, commandio.Path(h.Dir, operands[0]), vmfs.ReadOnly, 0)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "lp: %s: 读不到\n", operands[0])
			return interp.ExitStatus(1)
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			_, _ = fmt.Fprintf(h.Stderr, "lp: %v\n", readErr)
			return interp.ExitStatus(1)
		}
		if closeErr != nil {
			_, _ = fmt.Fprintf(h.Stderr, "lp: %v\n", closeErr)
			return interp.ExitStatus(1)
		}
		spec.Body = string(data)
	} else if len(operands) == 1 {

		spec.Body = commandio.Path(h.Dir, operands[0])
	} else {
		_, _ = fmt.Fprintln(h.Stderr, "lp: 一次一个文件（多文件是声明子集之外的形态）")
		return interp.ExitStatus(2)
	}
	id, err := scheduler.QueuePrint(ctx, spec)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "lp: %v\n", err)
		switch {
		case errors.Is(err, errno.NotFound):
			return interp.ExitStatus(1)
		default:
			return interp.ExitStatus(1)
		}
	}
	if !*quiet {
		_, _ = fmt.Fprintf(h.Stdout, "request id is %s\n", id)
	}
	return nil
}
