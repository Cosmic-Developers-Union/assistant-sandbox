package ed

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/internal/editor"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "ed")
	flags.SetInterspersed(false)
	prompt := flags.StringP("prompt", "p", "", "用 STRING 作为命令提示符")
	silent := flags.BoolP("silent", "s", false, "抑制 e/E/r/w 的字节数与 ! 回显")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) > 1 {
		_, _ = fmt.Fprintln(h.Stderr, "ed: 至多一个文件操作数")
		return interp.ExitStatus(2)
	}
	eng := &editor.Engine{
		Out: h.Stdout, Diag: h.Stderr,
		Silent: *silent, Prompt: *prompt,
		Files: guestFiles(ctx),
		Shell: shellEscape(ctx),
	}
	if len(operands) == 1 {

		count, err := eng.Load(operands[0])
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "ed: %v\n", err)
			return interp.ExitStatus(1)
		}

		if !*silent {
			_, _ = fmt.Fprintln(h.Stdout, count)
		}
	}
	if code := eng.Run(h.Stdin); code != 0 {
		return interp.ExitStatus(code)
	}
	return nil
}

func guestFiles(ctx context.Context) editor.Files {
	return editor.Files{
		Read: func(name string) (closeResult0 string, closeResult1 error) {
			file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), name), vmfs.ReadOnly, 0)
			if err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			defer func() {
				closeResult1 = errors.Join(closeResult1, file.Close())
			}()

			data, err := commandio.ReadText(ctx, file)
			if err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			return data, nil
		},
		Write: func(name string, data string) error {
			file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), name), vmfs.Create|vmfs.WriteOnly|vmfs.Truncate, 0o666)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if _, err := io.WriteString(file, data); err != nil {
				err = errors.Join(err, file.Close())
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return nil
		},
		Append: func(name string, data string) error {
			file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), name), vmfs.Create|vmfs.WriteOnly|vmfs.Append, 0o666)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if _, err := io.WriteString(file, data); err != nil {
				err = errors.Join(err, file.Close())
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return nil
		},
	}
}

func shellEscape(ctx context.Context) func(string) error {
	return func(command string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		if p.Control == nil {
			_, _ = fmt.Fprintln(commandio.Context(ctx).Stderr, "ed: 缺少进程控制端口，无法执行 shell 逃逸")
			return fmt.Errorf("缺少进程控制端口")
		}
		h := commandio.Context(ctx)
		_, err = p.Control.Run(ctx, proc.RunSpec{
			Parent: p.ID, Name: "bash", Args: []string{"-c", command}, Dir: p.Dir, Env: p.Env,
			Streams: fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr},
		})
		if err != nil {
			return fmt.Errorf("shell 逃逸: %w", err)
		}
		return nil
	}
}
