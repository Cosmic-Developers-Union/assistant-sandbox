package yes

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const defaultLine = "y"

const writeChunk = 256

func Run(ctx context.Context, args []string) error {
	line := defaultLine
	if len(args) > 0 {
		line = strings.Join(args, " ")
	}
	h := commandio.Context(ctx)
	block := []byte(strings.Repeat(line+"\n", writeChunk))
	for {

		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeBlock(h.Stdout, block); err != nil {

			if isBrokenPipe(err) {
				commandio.HandledBrokenPipe(ctx)
				return nil
			}
			return err
		}
	}
}

func writeBlock(writer io.Writer, block []byte) error {
	for len(block) > 0 {
		written, err := writer.Write(block)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		block = block[written:]
	}
	return nil
}

func isBrokenPipe(err error) bool {
	return errors.Is(err, errno.BrokenPipe) || errors.Is(err, io.ErrClosedPipe)
}
