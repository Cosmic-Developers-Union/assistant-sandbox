package commandio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"

	"github.com/spf13/pflag"
	"mvdan.cc/sh/v3/interp"
)

type Handler func(context.Context, []string) error

type outputStateKey struct{}

type outputState struct {
	mu        sync.Mutex
	stdoutErr error
	stderrErr error
}

func (s *outputState) record(stderr bool, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if stderr && s.stderrErr == nil {
		s.stderrErr = err
	} else if !stderr && (s.stdoutErr == nil || errors.Is(s.stdoutErr, io.ErrShortWrite) && (errors.Is(err, io.ErrClosedPipe) || errors.Is(err, errno.BrokenPipe))) {
		s.stdoutErr = err
	}
	s.mu.Unlock()
}

func (s *outputState) error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.stdoutErr, s.stderrErr)
}

func (s *outputState) handledBrokenPipe() {
	s.mu.Lock()
	if errors.Is(s.stdoutErr, io.ErrClosedPipe) || errors.Is(s.stdoutErr, errno.BrokenPipe) {
		s.stdoutErr = nil
	}
	s.mu.Unlock()
}

type checkedWriter struct {
	writer io.Writer
	state  *outputState
	stderr bool
}

func (w checkedWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if err == nil && n != len(data) {
		w.state.record(w.stderr, io.ErrShortWrite)
		return n, nil
	}
	w.state.record(w.stderr, err)
	return n, err
}

func HandledBrokenPipe(ctx context.Context) {
	if state, ok := ctx.Value(outputStateKey{}).(*outputState); ok {
		state.handledBrokenPipe()
	}
}

func Flags(ctx context.Context, name string) *pflag.FlagSet {
	f := pflag.NewFlagSet(name, pflag.ContinueOnError)
	f.SetOutput(Context(ctx).Stderr)
	f.SetInterspersed(true)
	return f
}

func Parse(f *pflag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		_, writeErr := fmt.Fprintf(f.Output(), "参数错误: %v\n", err)
		return errors.Join(interp.ExitStatus(2), err, writeErr)
	}
	return nil
}

func Execute(ctx context.Context, name string, run Handler, args []string) error {
	state := &outputState{}
	ctx = context.WithValue(ctx, outputStateKey{}, state)
	err := run(ctx, args)
	outputErr := state.error()
	err = errors.Join(err, outputErr)
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[interp.ExitStatus](err); ok {
		return err
	}
	_, writeErr := fmt.Fprintf(Context(ctx).Stderr, "%s: %v\n", name, err)
	return errors.Join(interp.ExitStatus(2), writeErr, state.error())
}

func Inputs(ctx context.Context, files []string, visit func(string, string) error) error {
	h := Context(ctx)
	if len(files) == 0 {
		files = []string{"-"}
	}
	total := 0
	for _, file := range files {
		var input string
		var err error
		if file == "-" {
			input, err = ReadText(ctx, h.Stdin)
		} else {
			var f vmfs.File
			f, err = Open(ctx, Path(h.Dir, file), vmfs.ReadOnly, 0)
			if err == nil {
				input, err = ReadText(ctx, f)
				err = errors.Join(err, f.Close())
			}
		}
		if err != nil {
			return err
		}
		total += len(input)
		if total > 4<<20 {
			return fmt.Errorf("文本命令累计输入超过 4 MiB")
		}
		if err = visit(file, input); err != nil {
			return err
		}
	}
	return nil
}

func Rows(input string) []string {
	if input == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(input, "\n"), "\n")
}

const maxStreamBytes = 4 << 20

func Lines(ctx context.Context, files []string, visit func(name, line string, hasNewline bool) (bool, error)) error {
	return eachInput(ctx, files, func(name string, reader io.Reader) (bool, error) {
		return streamLines(ctx, name, reader, visit)
	})
}

func Stream(ctx context.Context, files []string, visit func(name string, reader io.Reader) error) error {
	return eachInput(ctx, files, func(name string, reader io.Reader) (bool, error) {
		return true, visit(name, reader)
	})
}

type budgetReader struct {
	reader    io.Reader
	remaining int
}

func (b *budgetReader) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, fmt.Errorf("文本命令输入超过 4 MiB")
	}
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	b.remaining -= n
	return n, err
}

func eachInput(ctx context.Context, files []string, visit func(name string, reader io.Reader) (bool, error)) error {
	h := Context(ctx)
	if len(files) == 0 {
		files = []string{"-"}
	}
	for _, name := range files {
		var reader io.Reader
		var closer io.Closer
		if name == "-" {
			reader = h.Stdin
		} else {
			file, err := Open(ctx, Path(h.Dir, name), vmfs.ReadOnly, 0)
			if err != nil {
				return err
			}
			reader, closer = file, file
		}
		if reader == nil {
			continue
		}

		more, err := visit(name, &budgetReader{reader: reader, remaining: maxStreamBytes + 1})
		if closer != nil {
			if closeErr := closer.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return nil
}

func streamLines(ctx context.Context, name string, reader io.Reader, visit func(name, line string, hasNewline bool) (bool, error)) (bool, error) {
	if reader == nil {
		return true, nil
	}
	buffered := bufio.NewReader(reader)
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		line, err := buffered.ReadString('\n')
		hasNewline := strings.HasSuffix(line, "\n")
		if hasNewline {
			line = strings.TrimSuffix(line, "\n")
		}

		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if line != "" || hasNewline {
			total += len(line) + 1
			if total > maxStreamBytes {
				return false, fmt.Errorf("文本命令输入超过 4 MiB")
			}
			more, visitErr := visit(name, line, hasNewline)
			if visitErr != nil {
				return false, visitErr
			}
			if !more {
				return false, nil
			}
		}
		if errors.Is(err, io.EOF) {

			return true, nil
		}
	}
}

func Path(dir, name string) string {
	if path.IsAbs(name) {
		return name
	}
	return path.Join(dir, name)
}

func ReadText(ctx context.Context, r io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r == nil {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(r, 4<<20+1))
	if err != nil {
		return "", err
	}
	if len(data) > 4<<20 {
		return "", fmt.Errorf("文本命令输入超过 4 MiB")
	}
	return string(data), nil
}

func Filesystem(ctx context.Context) (vmfs.System, error) {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	return p.Resources.Filesystem()
}

func Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	system, err := Filesystem(ctx)
	if err != nil {
		return nil, err
	}
	return system.Open(ctx, name, flags, mode)
}

func Walk(ctx context.Context, name string, visit func(string, fs.DirEntry) error) error {
	remaining := 4096
	return walk(ctx, name, visit, &remaining, 0)
}
func walk(ctx context.Context, name string, visit func(string, fs.DirEntry) error, remaining *int, depth int) error {
	if *remaining <= 0 || depth > 128 {
		return fmt.Errorf("递归搜索超过 4096 节点或 128 层 VM 预算")
	}
	*remaining--
	if err := ctx.Err(); err != nil {
		return err
	}
	system, err := Filesystem(ctx)
	if err != nil {
		return err
	}
	info, err := system.Stat(ctx, name)
	if err != nil {
		return err
	}
	if err := visit(name, fs.FileInfoToDirEntry(info)); err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := system.ReadDir(ctx, name)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := walk(ctx, path.Join(name, entry.Name()), visit, remaining, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func Directory(ctx context.Context) string { return Context(ctx).Dir }

func Context(ctx context.Context) interp.HandlerContext {
	h := interp.HandlerCtx(ctx)
	p, err := proc.FromContext(ctx)
	if err != nil {
		h.Stdin = failedIO{err}
		h.Stdout = failedIO{err}
		h.Stderr = failedIO{err}
		return h
	}
	descriptors, err := p.Resources.Descriptors()
	if err != nil {
		h.Stdin = failedIO{err}
		h.Stdout = failedIO{err}
		h.Stderr = failedIO{err}
		return h
	}
	input, err := descriptors.Get(ctx, uint64(p.ID), 0)
	if err != nil {
		h.Stdin = failedIO{err}
	} else {
		h.Stdin = input
	}
	output, err := descriptors.Get(ctx, uint64(p.ID), 1)
	if err != nil {
		h.Stdout = failedIO{err}
	} else {
		h.Stdout = output
	}
	diagnostic, err := descriptors.Get(ctx, uint64(p.ID), 2)
	if err != nil {
		h.Stderr = failedIO{err}
	} else {
		h.Stderr = diagnostic
	}
	if state, ok := ctx.Value(outputStateKey{}).(*outputState); ok {
		h.Stdout = checkedWriter{writer: h.Stdout, state: state}
		h.Stderr = checkedWriter{writer: h.Stderr, state: state, stderr: true}
	}
	return h
}

type failedIO struct{ err error }

func (f failedIO) Read([]byte) (int, error)  { return 0, f.err }
func (f failedIO) Write([]byte) (int, error) { return 0, f.err }
