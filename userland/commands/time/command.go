package time

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/spf13/pflag"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := pflag.NewFlagSet("time", pflag.ContinueOnError)
	flags.SetOutput(h.Stderr)

	flags.SetInterspersed(false)

	flags.BoolP("portability", "p", false, "POSIX 的 -p（客体给不出秒值，输出与默认相同）")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}

		_, _ = fmt.Fprintf(h.Stderr, "参数错误: %v\n", err)
		return interp.ExitStatus(2)
	}
	command := flags.Args()
	if len(command) == 0 {
		return fmt.Errorf("缺少要执行的 utility")
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	if p.Control == nil {
		return fmt.Errorf("执行 %s: 缺少进程控制端口", command[0])
	}

	source, err := p.Resources.Time()
	if err != nil {
		return fmt.Errorf("客体没有时钟能力: %w", err)
	}
	start, err := source.Monotonic(ctx)
	if err != nil {
		return err
	}
	before, measured := readUsage(p)
	terminal, runErr := p.Control.Run(ctx, proc.RunSpec{
		Parent: p.ID, Name: command[0], Args: command[1:], Dir: p.Dir, Env: p.Env,
		Streams: fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr},
	})
	end, err := source.Monotonic(ctx)
	if err != nil {
		return err
	}
	if runErr != nil {
		switch {
		case errors.Is(runErr, errno.NotFound):

			_, _ = fmt.Fprintf(h.Stderr, "未注册沙箱命令: %s\n", command[0])
			return interp.ExitStatus(127)
		case errors.Is(runErr, errno.NoExec), errors.Is(runErr, errno.Permission):
			_, _ = fmt.Fprintf(h.Stderr, "%s: 无法执行\n", command[0])
			return interp.ExitStatus(126)
		}
		return fmt.Errorf("执行 %s: %w", command[0], runErr)
	}
	after, measuredAfter := readUsage(p)
	work, hasWork := workDelta(before, after, measured && measuredAfter)
	if err := report(h.Stderr, elapsed(start, end), work, hasWork); err != nil {
		return err
	}
	if terminal.ExitCode == 0 {
		return nil
	}

	return interp.ExitStatus(terminal.ExitCode)
}

func readUsage(p proc.Proc) (proc.Usage, bool) {
	control, ok := p.Control.(proc.UsageControl)
	if !ok {
		return proc.Usage{}, false
	}
	value, err := control.Usage(p.ID)
	if err != nil {
		return proc.Usage{}, false
	}
	return value, true
}

func workDelta(before, after proc.Usage, measured bool) (uint64, bool) {
	if !measured || after.Self < before.Self || after.Children < before.Children {
		return 0, false
	}
	return (after.Self - before.Self) + (after.Children - before.Children), true
}

func elapsed(start, end uint64) uint64 {
	if end < start {
		return 0
	}
	return end - start
}

func report(w io.Writer, real, work uint64, hasWork bool) error {
	line := fmt.Sprintf("real %s\n", seconds(real))
	if hasWork {
		line += fmt.Sprintf("user %d（确定性工作量，不是秒）\n", work)
	} else {
		line += "user 不可测（客体读不到确定性用量）\n"
	}
	line += "sys 不可测（客体没有用户态/内核态 CPU 计时）\n"
	_, err := io.WriteString(w, line)
	return err
}

func seconds(nanoseconds uint64) string {
	return strconv.FormatFloat(float64(nanoseconds)/1e9, 'f', 2, 64)
}
