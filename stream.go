package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
)

type StreamProcess struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	close  func() error
}

func (p *StreamProcess) Close() error { return p.close() }

func NewStreamProcess(stdin io.WriteCloser, stdout io.ReadCloser, cleanup func() error) (*StreamProcess, error) {
	if stdin == nil || stdout == nil || cleanup == nil {
		return nil, fmt.Errorf("协议进程必须提供输入、输出与清理函数")
	}
	input := &onceWriter{WriteCloser: stdin, close: streamClose(stdin)}
	output := &onceReader{ReadCloser: stdout, close: streamClose(stdout)}
	close := sync.OnceValue(func() error {
		return errors.Join(input.Close(), output.Close(), cleanup())
	})
	return &StreamProcess{Stdin: input, Stdout: output, close: close}, nil
}

func streamClose(pipe io.Closer) func() error {
	return sync.OnceValue(func() error {
		err := pipe.Close()

		if errors.Is(err, os.ErrClosed) {
			return nil
		}
		return err
	})
}

type StreamExecutor interface {
	Start(context.Context, string, string, []string, []string) (*StreamProcess, error)
}

func validateStream(command string, args, env []string) error {
	if command == "" || strings.HasPrefix(command, "-") || strings.ContainsRune(command, 0) || len(args) > 128 || len(env) > 128 {
		return fmt.Errorf("协议进程命令或参数数量无效")
	}
	for _, value := range append(slices.Clone(args), env...) {
		if strings.ContainsRune(value, 0) || len(value) > 64<<10 {
			return fmt.Errorf("协议进程参数包含 NUL 或超过 64 KiB")
		}
	}
	for _, value := range env {
		name, _, ok := strings.Cut(value, "=")
		if !ok || name == "" {
			return fmt.Errorf("协议进程环境变量无效")
		}
		for i, c := range name {
			if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (i == 0 || c < '0' || c > '9') {
				return fmt.Errorf("协议进程环境变量名称无效")
			}
		}
	}
	return nil
}

type onceWriter struct {
	io.WriteCloser
	close func() error
}

func (w *onceWriter) Close() error { return w.close() }

type onceReader struct {
	io.ReadCloser
	close func() error
}

func (r *onceReader) Close() error { return r.close() }

func (r *Runtime) Start(ctx context.Context, dir, command string, args, env []string) (*StreamProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateStream(command, args, env); err != nil {
		return nil, err
	}
	if err := validateCacheOutsideWorkspace(dir, r.cfg.CompilationCacheDir); err != nil {
		return nil, err
	}
	module := r.modules[command]
	if command == "assistant-bash" {
		module = r.shell
	}
	if module == nil {
		return nil, fmt.Errorf("未注册 WASI 模块 %s；stdio MCP 需要由宿主提供 WASI 模块", command)
	}
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(ctx)
	stopBase := context.AfterFunc(r.base, cancel)
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	stop := context.AfterFunc(ctx, func() { _, _ = inputR.Close(), outputW.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { <-r.slots }()
		defer stop()
		defer stopBase()
		defer func() { _, _ = inputR.Close(), outputW.Close() }()
		_, err := r.execute(ctx, dir, module, append([]string{command}, args...), env, inputR, outputW, io.Discard, cancel)
		outputW.CloseWithError(err)
	}()
	return NewStreamProcess(inputW, outputR, func() error { cancel(); <-done; return nil })
}
