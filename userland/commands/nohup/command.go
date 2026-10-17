package nohup

import (
	"context"
	"errors"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"mvdan.cc/sh/v3/interp"
)

const (
	outputName = "nohup.out"
	outputMode = 0o600
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	if len(args) == 0 {
		return fmt.Errorf("nohup 缺少要执行的程序")
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	if p.Control == nil {
		return fmt.Errorf("执行 %s: 缺少进程控制端口", args[0])
	}
	streams, closeOutput, err := outputRedirect(ctx, p,
		fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr})
	if err != nil {

		_, _ = fmt.Fprintf(h.Stderr, "nohup: 无法写入 %s: %v\n", outputName, err)
		return interp.ExitStatus(126)
	}
	defer closeOutput()
	terminal, err := p.Control.Run(ctx, proc.RunSpec{
		Parent: p.ID, Name: args[0], Args: args[1:], Dir: p.Dir, Env: p.Env,
		Streams: streams, IgnoreSignals: []signal.Number{signal.HUP},
	})
	if err != nil {
		switch {
		case errors.Is(err, errno.NotFound):

			_, _ = fmt.Fprintf(h.Stderr, "未注册沙箱命令: %s\n", args[0])
			return interp.ExitStatus(127)
		case errors.Is(err, errno.NoExec), errors.Is(err, errno.Permission):
			_, _ = fmt.Fprintf(h.Stderr, "%s: 无法执行\n", args[0])
			return interp.ExitStatus(126)
		}
		return fmt.Errorf("执行 %s: %w", args[0], err)
	}
	if terminal.ExitCode == 0 {
		return nil
	}
	return interp.ExitStatus(terminal.ExitCode)
}

var terminalQuery = func(ctx context.Context, descriptors fd.Descriptors, pid proc.PID, id uint32) bool {
	return isTerminal(ctx, descriptors, pid, id)
}

func outputRedirect(ctx context.Context, p proc.Proc, streams fd.Streams) (fd.Streams, func(), error) {
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return streams, func() {}, err
	}
	if !terminalQuery(ctx, descriptors, p.ID, 1) {
		return streams, func() {}, nil
	}
	file, err := openOutput(ctx, p)
	if err != nil {
		return streams, func() {}, err
	}
	streams.Output = file
	if terminalQuery(ctx, descriptors, p.ID, 2) {
		streams.Error = file
	}
	return streams, func() { _ = file.Close() }, nil
}

func openOutput(ctx context.Context, p proc.Proc) (vmfs.File, error) {
	if file, err := createOutput(ctx, p.Dir); err == nil {
		return file, nil
	}
	home := p.Env["HOME"]
	if home == "" {
		return nil, errno.Wrap(errno.NotFound, "HOME 未设置")
	}
	return createOutput(ctx, home)
}

func createOutput(ctx context.Context, dir string) (vmfs.File, error) {
	name := outputName
	if dir != "" {
		name = dir + "/" + outputName
	}
	return commandio.Open(ctx, name, vmfs.Create|vmfs.Append|vmfs.WriteOnly, outputMode)
}

func isTerminal(ctx context.Context, descriptors fd.Descriptors, pid proc.PID, id uint32) bool {
	terminal, err := descriptors.Terminal(ctx, uint64(pid), id)
	if err != nil {
		return false
	}
	return terminal.TTY
}
