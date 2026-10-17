package vi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "vi")
	recoverMode := flags.BoolP("recover", "r", false, "恢复会话（客体没有恢复数据）")
	tag := flags.StringP("tag", "t", "", "tag 定位（客体没有 tag 栈）")
	window := flags.IntP("window", "w", 0, "窗口行数（以终端 Rows 为准）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if *recoverMode {
		_, _ = fmt.Fprintln(h.Stderr, "vi: 客体没有可恢复的编辑会话数据")
		return interp.ExitStatus(1)
	}
	if *tag != "" {
		_, _ = fmt.Fprintln(h.Stderr, "vi: 客体没有 tag 机制，-t 不支持")
		return interp.ExitStatus(1)
	}
	if *window != 0 {
		_, _ = fmt.Fprintln(h.Stderr, "vi: 窗口尺寸以终端 Rows 为准，-w 不支持")
		return interp.ExitStatus(1)
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return err
	}
	term, err := findTerminal(ctx, descriptors, p.ID)
	if err != nil || !term.TTY {
		_, _ = fmt.Fprintf(h.Stderr, "vi: 标准流不是终端（%v；屏幕编辑器需要终端）\n", err)
		return interp.ExitStatus(1)
	}
	setter, hasSetter := descriptors.(fd.TerminalSetter)
	if !hasSetter {
		_, _ = fmt.Fprintln(h.Stderr, "vi: 客体没有终端设置能力（无法进入非规范模式）")
		return interp.ExitStatus(1)
	}
	filesystem, err := p.Resources.Filesystem()
	if err != nil {
		return err
	}

	name := ""
	if len(operands) > 0 {
		name = operands[0]
		if len(operands) > 1 {
			_, _ = fmt.Fprintf(h.Stderr, "vi: 多文件编辑是声明子集，只编辑 %s\n", name)
		}
	}
	session, err := newSession(ctx, filesystem, name, int(term.Rows), int(term.Columns), h.Stdin, h.Stdout)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "vi: %v\n", err)
		return interp.ExitStatus(1)
	}

	saved := term.State
	restore := func() {
		_ = setter.SetTerminalState(ctx, uint64(p.ID), 0, saved)
	}
	defer restore()
	raw := saved
	raw.Canonical = false
	raw.Echo = false
	if err := setter.SetTerminalState(ctx, uint64(p.ID), 0, raw); err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "vi: 切换终端模式失败: %v\n", err)
		return interp.ExitStatus(1)
	}
	if err := session.loop(ctx); err != nil {
		if errors.Is(err, errno.Cancelled) {
			return err
		}
		_, _ = fmt.Fprintf(h.Stderr, "vi: %v\n", err)
		return interp.ExitStatus(1)
	}
	return nil
}

func findTerminal(ctx context.Context, descriptors fd.Descriptors, pid proc.PID) (fd.Terminal, error) {
	for id := uint32(0); id < 3; id++ {
		info, err := descriptors.Terminal(ctx, uint64(pid), id)
		if err == nil && info.TTY {
			return info, nil
		}
	}
	return fd.Terminal{}, errno.NoDevice
}

var _ = strings.TrimSpace
