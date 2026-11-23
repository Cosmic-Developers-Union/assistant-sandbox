package main

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	vmsignal "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"golang.org/x/term"
)

func terminalPair(stdin io.Reader, stdout io.Writer) bool {
	input, inputOK := stdin.(*os.File)
	output, outputOK := stdout.(*os.File)
	return inputOK && outputOK &&
		term.IsTerminal(int(input.Fd())) &&
		term.IsTerminal(int(output.Fd()))
}

// runPTY 把宿主终端桥接到客体 /dev/ptmx。Bash 的 stdin/stdout/stderr
// 都是 PTY 从端，因此 isatty、终端回显、控制字符与作业控制可用。
func runPTY(
	ctx context.Context,
	machine *instance.VM,
	connection *instance.Connection,
	shell nativeshell.Shell,
	environment map[string]string,
	stdin, stdout *os.File,
) (int, error) {
	fs := machine.Filesystem()
	master, err := fs.Open(ctx, "/dev/ptmx", vmfs.ReadWrite, 0)
	if err != nil {
		return 1, err
	}
	defer func() { _ = master.Close() }()
	slave, err := fs.Open(ctx, "/dev/pts/0", vmfs.ReadWrite, 0)
	if err != nil {
		return 1, err
	}
	defer func() { _ = slave.Close() }()

	if columns, rows, sizeErr := term.GetSize(int(stdout.Fd())); sizeErr == nil {
		if terminal, ok := slave.(interface {
			State() fd.TerminalState
			SetState(fd.TerminalState) error
		}); ok {
			state := terminal.State()
			state.Rows = terminalDimension(rows, 24)
			state.Columns = terminalDimension(columns, 80)
			_ = terminal.SetState(state)
		}
	}
	args := []string{"-i"}
	if shell == nativeshell.Bash {
		args = []string{"--noprofile", "--norc", "--noediting", "-i"}
	}
	process, err := connection.Exec(ctx, instance.ExecSpec{
		Program: shell.Program(),
		Args:    args,
		Dir:     instance.WorkRoot,
		Env:     environment,
		Streams: fd.Streams{Input: slave, Output: slave, Error: slave},
	})
	if err != nil {
		return 1, err
	}
	hostState, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return 1, err
	}
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() { _ = term.Restore(int(stdin.Fd()), hostState) })
	}
	defer restore()
	stopRestore := context.AfterFunc(ctx, restore)
	defer stopRestore()

	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		_, _ = io.Copy(&crlfWriter{writer: stdout}, master)
	}()
	go func() {
		if shell == nativeshell.Bash {
			_ = bridgeInput(stdin, master)
			return
		}
		_, _ = io.Copy(master, stdin)
	}()

	state, err := connection.Wait(ctx, process.ID)
	if err != nil {
		// 宿主收到终止信号时取消 Wait；客体进程仍可能活着，先杀掉
		// 前台进程组，避免 VM.Close 无限等待。
		_ = machine.Kernel().SignalGroup(process.PGID, vmsignal.KILL)
		killCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = connection.Wait(killCtx, process.ID)
		cancel()
	}
	_ = master.Close()
	select {
	case <-outputDone:
	case <-time.After(time.Second):
		// PTY master 已关闭；若客体输出线程仍未退出，不让终端会话无限卡死。
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 1, nil
		}
		return 1, err
	}
	return int(state.ExitCode), nil
}

func terminalDimension(value, fallback int) uint16 {
	if value < 1 {
		value = fallback
	}
	if value < 1 {
		value = 1
	}
	if value > int(^uint16(0)) {
		return ^uint16(0)
	}
	return uint16(value) //nolint:gosec // 已限制到 uint16 范围
}

// crlfWriter 补足客体 TTY 尚未实现的 ONLCR：宿主终端处于 raw 模式时
// 必须显式把 LF 写成 CRLF，否则输出会逐行向右偏移。
type crlfWriter struct {
	writer  io.Writer
	afterCR bool
}

func (w *crlfWriter) Write(buffer []byte) (int, error) {
	output := make([]byte, 0, len(buffer)+8)
	for _, b := range buffer {
		if b == '\n' && !w.afterCR {
			output = append(output, '\r')
		}
		output = append(output, b)
		w.afterCR = b == '\r'
	}
	if _, err := w.writer.Write(output); err != nil {
		return 0, err
	}
	return len(buffer), nil
}
