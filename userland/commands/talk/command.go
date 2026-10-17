package talk

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const talkChannel = "/var/spool/talk/channel"

const channelLines = 256

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "talk")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}

	if len(operands) > 0 {
		if err := checkUser(ctx, p, operands[0]); err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "talk: %v\n", err)
			return interp.ExitStatus(1)
		}
	}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return err
	}
	if _, err := terminalStream(ctx, descriptors, p.ID); err != nil {
		_, _ = fmt.Fprintln(h.Stderr, "talk: 标准流不是终端")
		return interp.ExitStatus(1)
	}
	system, err := p.Resources.Filesystem()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(h.Stdout, "[等待对端。输入一行发送；Ctrl-D 退出。]")

	offset := currentLines(ctx, system)
	var input strings.Builder
	for {
		if err := ctx.Err(); err != nil {

			return nil
		}

		lines := readChannel(ctx, system)
		for ; offset < len(lines); offset++ {
			if strings.HasPrefix(lines[offset], "[me] ") {
				continue
			}
			_, _ = fmt.Fprintf(h.Stdout, "[peer] %s\n", lines[offset])
		}

		if readLocal(ctx, descriptors, p.ID, &input) {
			line := strings.TrimSpace(input.String())
			input.Reset()
			if line == "" && localEOF(ctx, descriptors, p.ID) {
				return nil
			}
			if line != "" {
				if err := appendChannel(ctx, system, line); err != nil {
					_, _ = fmt.Fprintf(h.Stderr, "talk: %v\n", err)
					return interp.ExitStatus(1)
				}
				_, _ = fmt.Fprintf(h.Stdout, "[me] %s\n", line)

				offset = currentLines(ctx, system)
			}
		}

		if err := sleepTick(ctx, p); err != nil {
			return err
		}
	}
}

func checkUser(ctx context.Context, p proc.Proc, name string) error {
	source, err := p.Resources.Credentials()
	if err != nil {
		if name == "sandbox" {
			return nil
		}
		return errno.Wrap(errno.NotFound, name+": 没有这个用户")
	}
	account, err := source.Lookup(ctx, uint64(p.ID))
	if err != nil {
		return err
	}
	if name != account.Name && name != "sandbox" {
		return errno.Wrap(errno.NotFound, name+": 没有这个用户")
	}
	return nil
}

func terminalStream(ctx context.Context, descriptors fd.Descriptors, pid proc.PID) (fd.Stream, error) {
	for _, id := range []uint32{0, 1, 2} {
		info, err := descriptors.Terminal(ctx, uint64(pid), id)
		if err != nil {
			return nil, err
		}
		if !info.TTY {
			continue
		}
		return descriptors.Get(ctx, uint64(pid), id)
	}
	return nil, errno.NoDevice
}

func readLocal(ctx context.Context, descriptors fd.Descriptors, pid proc.PID, pending *strings.Builder) bool {
	stream, err := descriptors.Get(ctx, uint64(pid), 0)
	if err != nil {
		return false
	}
	var buffer [256]byte
	n, err := stream.Read(buffer[:])
	if err != nil {
		return false
	}

	if n == 0 {
		return pending.Len() == 0
	}
	if _, err := descriptors.Get(ctx, uint64(pid), 0); err != nil {
		return false
	}
	text := string(buffer[:n])
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		pending.WriteString(text[:index])
		return true
	}
	pending.WriteString(text)
	return false
}

func localEOF(ctx context.Context, descriptors fd.Descriptors, pid proc.PID) bool {
	stream, err := descriptors.Get(ctx, uint64(pid), 0)
	if err != nil {
		return true
	}
	var probe [1]byte
	n, err := stream.Read(probe[:])
	return n == 0 && err != nil
}

func sleepTick(ctx context.Context, p proc.Proc) error {
	clk, err := p.Resources.Time()
	if err != nil {
		return nil
	}
	if err := clk.Sleep(ctx, 10_000_000); err != nil {
		return nil
	}
	return nil
}

func readChannel(ctx context.Context, system vmfs.System) (lines []string) {
	file, err := system.Open(ctx, talkChannel, vmfs.ReadOnly, 0)
	if err != nil {
		return nil
	}
	defer func() {
		if err := file.Close(); err != nil {
			lines = nil
		}
	}()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	return splitLines(string(data))
}

func currentLines(ctx context.Context, system vmfs.System) int {
	return len(readChannel(ctx, system))
}

func ensureChannelDir(ctx context.Context, system vmfs.System) error {
	if parent := parentDir(talkChannel); parent != "/" {
		return mkdirAllVM(ctx, system, parent)
	}
	return nil
}

func parentDir(path string) string {
	if index := strings.LastIndexByte(path, '/'); index > 0 {
		return path[:index]
	}
	return "/"
}

func mkdirAllVM(ctx context.Context, system vmfs.System, name string) error {
	if _, err := system.Stat(ctx, name); err == nil {
		return nil
	}
	if parent := parentDir(name); parent != name {
		if err := mkdirAllVM(ctx, system, parent); err != nil {
			return err
		}
	}
	if err := system.Mkdir(ctx, name, 0o755); err != nil {
		if _, statErr := system.Stat(ctx, name); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func appendChannel(ctx context.Context, system vmfs.System, line string) error {
	lines := readChannel(ctx, system)
	lines = append(lines, line)
	if len(lines) > channelLines {
		lines = lines[len(lines)-channelLines:]
	}
	if err := ensureChannelDir(ctx, system); err != nil {
		return err
	}

	temp := talkChannel + ".tmp"
	file, err := system.Open(ctx, temp, vmfs.WriteOnly|vmfs.Create|vmfs.Truncate, 0o644)
	if err != nil {
		return err
	}
	_, err = file.Write([]byte(strings.Join(lines, "\n") + "\n"))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return system.Rename(ctx, temp, talkChannel)
}

func splitLines(data string) []string {
	data = strings.TrimSuffix(data, "\n")
	if data == "" {
		return nil
	}
	return strings.Split(data, "\n")
}
