package vi

import (
	"context"
	"fmt"
	"io"
	"strings"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type editor struct {
	lines []string

	row, col int

	top int

	name   string
	dirty  bool
	status string

	lastSearch    string
	lastDirection int

	undo *snapshot

	pending int

	input  byteReader
	output byteWriter

	rows, cols int

	filesystem vmfs.System

	writeSeq int

	register register
}

type register struct {
	lines []string
	chunk string
	rows  bool
}

type (
	byteReader interface {
		ReadContext(ctx context.Context, p []byte) (int, error)
	}
	byteWriter interface {
		Write(p []byte) (int, error)
	}
)

type plainReader struct{ inner io.Reader }

func (p plainReader) ReadContext(ctx context.Context, buf []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return p.inner.Read(buf)
}

type snapshot struct {
	lines []string
	row   int
	col   int
}

func newSession(ctx context.Context, filesystem vmfs.System, name string, rows, cols int, stdin io.Reader, stdout io.Writer) (*editor, error) {
	e := &editor{name: name, rows: rows, cols: cols, filesystem: filesystem, output: stdout}

	if blocking, ok := stdin.(byteReader); ok {
		e.input = blocking
	} else {
		e.input = plainReader{stdin}
	}
	if e.rows < 4 {
		e.rows = 4
	}
	if e.cols < 8 {
		e.cols = 8
	}
	e.lines = []string{""}
	if name != "" {
		file, err := filesystem.Open(ctx, name, vmfs.ReadOnly, 0)
		if err == nil {
			data, readErr := readAll(ctx, file)
			closeErr := file.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			e.lines = splitLines(data)
		} else {

			e.status = fmt.Sprintf("[新文件] %s", name)
		}
	}
	return e, nil
}

func splitLines(data string) []string {
	trimmed := strings.TrimSuffix(data, "\n")
	if trimmed == "" {
		return []string{""}
	}
	return strings.Split(trimmed, "\n")
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n") + "\n"
}

func readAll(ctx context.Context, file vmfs.File) (string, error) {
	var builder strings.Builder
	buffer := make([]byte, 4096)
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			builder.Write(buffer[:n])
		}
		if err != nil {
			if len(builder.String()) > 4<<20 {
				return "", fmt.Errorf("文件超过 4 MiB 输入预算")
			}
			if err.Error() == "EOF" || strings.Contains(err.Error(), "EOF") {
				return builder.String(), nil
			}
			return builder.String(), err
		}
		if ctx.Err() != nil {
			return builder.String(), ctx.Err()
		}
	}
}
