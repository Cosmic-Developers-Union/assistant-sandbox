//go:build linux && amd64

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"golang.org/x/term"
)

func runNativeCLI(ctx context.Context, backend instance.BackendInstance, config nativeCLIConfig) (bool, int, error) {
	machine, ok := backend.(*instance.NativeVM)
	if !ok {
		return false, 0, nil
	}
	if config.mountWorkspace {
		if err := machine.Mount(config.workspace, instance.WorkRoot); err != nil {
			return true, 1, err
		}
	}
	if config.interactive {
		code, err := runNativePTY(ctx, machine, config)
		return true, code, err
	}
	args := []string{"-c", config.command}
	if config.session {
		args = []string{"-s"}
		if config.shell == nativeshell.Bash {
			args = []string{"--noprofile", "--norc", "-s"}
		}
	}
	code, err := runNativeCommand(ctx, machine, config.shell.Program(), args, config.environment,
		config.stdin, config.stdout, config.stderr, config.session)
	return true, code, err
}

func runNativeCommand(
	ctx context.Context,
	machine *instance.NativeVM,
	program string,
	args []string,
	environment map[string]string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	withStdin bool,
) (int, error) {
	var input *os.File
	if withStdin {
		var cleanupInput func()
		var err error
		input, cleanupInput, err = nativeStreamFile(stdin, true)
		if err != nil {
			return 1, err
		}
		defer cleanupInput()
	}
	output, cleanupOutput, err := nativeStreamFile(stdout, false)
	if err != nil {
		return 1, err
	}
	defer cleanupOutput()
	diagnostic, cleanupDiagnostic, err := nativeStreamFile(stderr, false)
	if err != nil {
		return 1, err
	}
	defer cleanupDiagnostic()
	process, err := machine.Exec(ctx, instance.NativeExecSpec{
		Program: program, Args: args, Dir: instance.WorkRoot, Env: environment,
		Stdin: input, Stdout: output, Stderr: diagnostic,
	})
	if err != nil {
		return 1, err
	}
	state, waitErr := process.Wait(ctx)
	flushErr := errors.Join(nativeFlushFile(output, stdout), nativeFlushFile(diagnostic, stderr))
	if waitErr != nil {
		return 1, errors.Join(waitErr, flushErr)
	}
	return state.Code, flushErr
}

func nativeStreamFile(stream any, input bool) (*os.File, func(), error) {
	if file, ok := stream.(*os.File); ok {
		return file, func() {}, nil
	}
	if input {
		reader, ok := stream.(io.Reader)
		if !ok {
			return nil, func() {}, errors.New("native CLI input is not a reader")
		}
		return nativeTempFile(reader, true)
	}
	writer, ok := stream.(io.Writer)
	if !ok {
		return nil, func() {}, errors.New("native CLI output is not a writer")
	}
	return nativeTempFile(writer, false)
}

func nativeTempFile(stream any, input bool) (*os.File, func(), error) {
	file, err := os.CreateTemp("", "assistant-native-stream-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("create native CLI stream: %w", err)
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if input {
		if _, err := io.Copy(file, stream.(io.Reader)); err != nil {
			_ = file.Close()
			cleanup()
			return nil, func() {}, fmt.Errorf("copy native CLI input: %w", err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			cleanup()
			return nil, func() {}, fmt.Errorf("rewind native CLI input: %w", err)
		}
	}
	return file, func() {
		_ = file.Close()
		cleanup()
	}, nil
}

func nativeFlushFile(file *os.File, writer io.Writer) error {
	if destination, ok := writer.(*os.File); ok && destination == file {
		return nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind native CLI output: %w", err)
	}
	_, err := io.Copy(writer, file)
	return err
}

func runNativePTY(ctx context.Context, machine *instance.NativeVM, config nativeCLIConfig) (int, error) {
	stdin, stdinOK := config.stdin.(*os.File)
	stdout, stdoutOK := config.stdout.(*os.File)
	if !stdinOK || !stdoutOK {
		return 1, errors.New("native interactive PTY requires host terminal files")
	}
	rows, columns := uint16(24), uint16(100)
	if width, height, err := term.GetSize(int(stdout.Fd())); err == nil {
		rows = terminalDimension(height, 24)
		columns = terminalDimension(width, 100)
	}
	args := []string{"-i"}
	if config.shell == nativeshell.Bash {
		args = []string{"--noprofile", "--norc", "--noediting", "-i"}
	}
	session, err := machine.StartSessionSpec(ctx, instance.NativeSessionSpec{
		Program: config.shell.Program(), Args: args, Env: config.environment,
		Rows: rows, Cols: columns, Echo: true,
	})
	if err != nil {
		return 1, err
	}
	defer func() { _ = session.Close(context.Background()) }()
	hostState, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return 1, err
	}
	var restoreOnce sync.Once
	restore := func() { restoreOnce.Do(func() { _ = term.Restore(int(stdin.Fd()), hostState) }) }
	defer restore()
	stopRestore := context.AfterFunc(ctx, restore)
	defer stopRestore()

	resizeSignals := make(chan os.Signal, 1)
	signal.Notify(resizeSignals, syscall.SIGWINCH)
	defer signal.Stop(resizeSignals)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-resizeSignals:
				if width, height, sizeErr := term.GetSize(int(stdout.Fd())); sizeErr == nil {
					_ = session.Resize(terminalDimension(height, 24), terminalDimension(width, 80))
				}
			}
		}
	}()

	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		writer := &crlfWriter{writer: stdout}
		for {
			chunk, drainErr := session.DrainContext(ctx)
			if len(chunk) > 0 {
				_, _ = writer.Write(chunk)
			}
			if drainErr != nil {
				return
			}
		}
	}()
	go func() { _, _ = io.Copy(nativeSessionWriter{session: session}, stdin) }()

	state, waitErr := session.Wait(ctx)
	if waitErr != nil {
		_ = session.Close(context.Background())
	}
	select {
	case <-outputDone:
	case <-time.After(time.Second):
	}
	if waitErr != nil {
		if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) {
			return 1, nil
		}
		return 1, waitErr
	}
	return state.Code, nil
}

type nativeSessionWriter struct{ session *instance.NativeSession }

func (w nativeSessionWriter) Write(data []byte) (int, error) {
	if err := w.session.Write(data); err != nil {
		return 0, err
	}
	return len(data), nil
}
