package more

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

type quitRequested struct{}

func (quitRequested) Error() string { return "more: 退出" }

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "more")
	pages := flags.IntP("lines", "n", 0, "每页行数（覆盖终端 Rows）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		return err
	}
	info, err := descriptors.Terminal(ctx, uint64(p.ID), 1)
	if err != nil {
		return err
	}
	if info.TTY {

		rows := 24
		if info.Rows > 0 {
			rows = int(info.Rows)
		}
		if *pages > 0 {
			rows = *pages
		}
		return pageInputs(ctx, h, operands, rows)
	}

	return copyInputs(ctx, h, operands)
}

func inputs(operands []string) []string {
	if len(operands) == 0 {
		return []string{"-"}
	}
	return operands
}

func openInput(ctx context.Context, h interp.HandlerContext, operands []string, index int) (io.Reader, io.Closer, string, error) {
	name := inputs(operands)[index]
	if name == "-" {
		return h.Stdin, nil, name, nil
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return nil, nil, name, err
	}
	file, err := system.Open(ctx, commandio.Path(h.Dir, name), vmfs.ReadOnly, 0)
	if err != nil {
		return nil, nil, name, err
	}
	return file, file, name, nil
}

func pageInputs(ctx context.Context, h interp.HandlerContext, operands []string, rows int) error {
	for index := range inputs(operands) {
		reader, closer, name, err := openInput(ctx, h, operands, index)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "more: %s: %v\n", name, err)
			continue
		}
		if err := pageOne(ctx, h, reader, rows); err != nil {
			if closer != nil {
				err = errors.Join(err, closer.Close())
			}
			var quitRequested quitRequested
			if errors.As(err, &quitRequested) {
				return nil
			}
			return err
		}
		if closer != nil {
			if err := closer.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func pageOne(ctx context.Context, h interp.HandlerContext, reader io.Reader, rows int) error {
	buffered := bufio.NewReader(reader)
	shown := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := buffered.ReadString('\n')
		if len(line) > 0 {
			if _, werr := io.WriteString(h.Stdout, line); werr != nil {
				return werr
			}
			if strings.HasSuffix(line, "\n") {
				shown++
			}
		}
		if err != nil && err != io.EOF {
			return err
		}
		if err == io.EOF {
			return nil
		}
		if shown >= rows-1 {

			if _, werr := fmt.Fprint(h.Stdout, "--More--"); werr != nil {
				return werr
			}

			answer, aerr := buffered.ReadString('\n')
			if errors.Is(aerr, io.EOF) {
				return nil
			}
			if aerr != nil {
				return aerr
			}
			answer = strings.TrimSpace(answer)
			if answer == "q" || answer == "Q" {
				return quitRequested{}
			}
			shown = 0
		}
	}
}

func copyInputs(ctx context.Context, h interp.HandlerContext, operands []string) error {
	failures := 0
	for index := range inputs(operands) {
		reader, closer, name, err := openInput(ctx, h, operands, index)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "more: %s: %v\n", name, err)
			failures++
			continue
		}
		_, err = io.Copy(h.Stdout, reader)
		if closer != nil {
			if cerr := closer.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "more: %s: %v\n", name, err)
			failures++
		}
	}
	if failures > 0 {
		return interp.ExitStatus(1)
	}
	return nil
}
