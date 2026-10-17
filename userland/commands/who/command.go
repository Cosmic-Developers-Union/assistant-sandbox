package who

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const terminalPath = "/dev/tty"

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "who")
	header := flags.BoolP("header", "H", false, "输出表头行")
	quick := flags.BoolP("quick", "q", false, "只输出名字与用户数")

	_ = flags.BoolP("short", "s", false, "短格式（与缺省一致）")
	_ = flags.BoolP("mesg-mark", "m", false, "只输出与当前终端相关的行（与缺省一致的单一会话）")
	mark := flags.BoolP("tty", "T", false, "输出 mesg 标记列")
	boot := flags.BoolP("boot", "b", false, "输出开机时刻")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {

		_, _ = fmt.Fprintln(h.Stderr, "who: 客体没有 utmp 事实，不接受文件操作数")
		return interp.ExitStatus(2)
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	identitySource, err := p.Resources.Credentials()
	if err != nil {
		return err
	}
	identity, err := identitySource.Lookup(ctx, uint64(p.ID))
	if err != nil {
		return err
	}
	control, ok := p.Control.(proc.SystemControl)
	var config proc.SystemConfiguration
	if ok {
		config, err = control.SystemConfiguration()
		if err != nil {
			return err
		}
	}
	if *quick {
		_, err := fmt.Fprintf(h.Stdout, "%s\n# users=1\n", identity.Name)
		return err
	}
	if *boot {
		if !ok || !config.BootDeclared {

			_, _ = fmt.Fprintln(h.Stderr, "who: 客体没有声明开机时刻")
			return interp.ExitStatus(1)
		}
		_, err := fmt.Fprintf(h.Stdout, "system boot  %s\n", formatTime(config.BootTime))
		return err
	}
	if *header {
		if *mark {
			_, _ = fmt.Fprintln(h.Stdout, "NAME     LINE     TIME         MESSAGE")
		} else {
			_, _ = fmt.Fprintln(h.Stdout, "NAME     LINE     TIME")
		}
	}
	terminal := terminalFacts(ctx, p)
	name := identity.Name
	if *mark {
		name = markColumn(terminal) + " " + identity.Name
	}
	line := fmt.Sprintf("%-8s %-8s %s", name, terminal.path, terminal.login(config))
	_, err = fmt.Fprintln(h.Stdout, line)
	return err
}

func terminalFacts(ctx context.Context, p proc.Proc) terminalInfo {
	info := terminalInfo{path: "?"}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return info
	}
	var state fd.Terminal
	for _, id := range []uint32{0, 1, 2} {
		terminal, err := descriptors.Terminal(ctx, uint64(p.ID), id)
		if err != nil || !terminal.TTY {
			continue
		}
		state = terminal
		break
	}
	if !state.TTY {

		return info
	}
	info.hasTerminal = true
	info.path = terminalPath
	info.mesg = state.State.Mesg
	return info
}

type terminalInfo struct {
	hasTerminal bool
	mesg        bool
	path        string
}

func (t terminalInfo) login(config proc.SystemConfiguration) string {
	if !config.BootDeclared {
		return "-"
	}
	return formatTime(config.BootTime)
}

func markColumn(t terminalInfo) string {
	switch {
	case !t.hasTerminal:
		return "?"
	case t.mesg:
		return "+"
	default:
		return "-"
	}
}

func formatTime(nanoseconds int64) string {
	return clock.Unix(0, nanoseconds).UTC().Format("2006-01-02 15:04")
}
