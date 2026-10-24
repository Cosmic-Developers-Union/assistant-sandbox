package model

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件迁入原项目 examples/agent/sandbox-contract 的参考实现(A06):
// 以宿主 bash + os.Root 驱动的新模型实现,不访问原项目目录。
// 它与真实 VM 实现共用 Suite 的全部断言(A07)。

type shellModel struct {
	gate     chan struct{}
	mu       sync.Mutex
	input    io.WriteCloser
	done     chan struct{}
	finished chan int
	output   strings.Builder
	lines    [][]rune
	cursor   Position
}

func startShell(dir string) (*shellModel, error) {
	command := exec.Command("bash", "--noprofile", "--norc")
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, errors.Join(err, input.Close())
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		return nil, errors.Join(err, input.Close(), output.Close())
	}
	shell := &shellModel{
		input: input, done: make(chan struct{}), finished: make(chan int, 1),
		gate:  make(chan struct{}, 1),
		lines: [][]rune{make([]rune, 0)},
	}
	go func() {
		defer close(shell.done)
		reader := bufio.NewReader(output)
		for {
			character, _, readErr := reader.ReadRune()
			if readErr != nil {
				break
			}
			if character == '\x1e' {
				status, markerErr := reader.ReadString('\x1f')
				if markerErr != nil {
					break
				}
				code, parseErr := strconv.Atoi(strings.TrimSuffix(status, "\x1f"))
				if parseErr != nil {
					break
				}
				shell.finished <- code
				continue
			}
			shell.render(character)
		}
		_ = command.Wait()
	}()
	return shell, nil
}

func (s *shellModel) render(character rune) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.output.WriteRune(character)
	if character == '\r' {
		s.cursor.Column = 0
		return
	}
	if character == '\n' || s.cursor.Column == 80 {
		s.lines = append(s.lines, []rune{})
		s.cursor = Position{Row: len(s.lines) - 1}
		if len(s.lines) > 24 {
			s.lines = s.lines[1:]
			s.cursor.Row--
		}
		if character == '\n' {
			return
		}
	}
	row := &s.lines[s.cursor.Row]
	if s.cursor.Column == len(*row) {
		*row = append(*row, character)
	} else {
		(*row)[s.cursor.Column] = character
	}
	s.cursor.Column++
}

func (s *shellModel) run(ctx context.Context, script string) (Result, error) {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	release := true
	defer func() {
		if release {
			<-s.gate
		}
	}()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	select {
	case <-s.done:
		return Result{}, ErrClosed
	default:
	}
	s.mu.Lock()
	start := s.output.Len()
	s.mu.Unlock()
	if _, err := fmt.Fprintf(s.input, "%s\nprintf '\\036%%s\\037' \"$?\"\n", script); err != nil {
		return Result{}, err
	}
	select {
	case code := <-s.finished:
		s.mu.Lock()
		defer s.mu.Unlock()
		return Result{Output: s.output.String()[start:], ExitCode: code}, nil
	case <-s.done:
		return Result{}, ErrClosed
	case <-ctx.Done():
		release = false
		go func() {
			select {
			case <-s.finished:
			case <-s.done:
			}
			<-s.gate
		}()
		return Result{}, ctx.Err()
	}
}

func (s *shellModel) screen() Screen {
	s.mu.Lock()
	defer s.mu.Unlock()
	frame := Screen{Cursor: s.cursor}
	for _, line := range s.lines {
		frame.Lines = append(frame.Lines, string(line))
	}
	return frame
}

func (s *shellModel) stop(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	default:
	}
	if err := s.input.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type vmModel struct {
	mu       sync.Mutex
	dir      string
	root     *os.Root
	console  *consoleModel
	sessions []*shellModel
	closed   atomic.Bool
}

func newModelVM(t *testing.T) (VM, error) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	shell, err := startShell(dir)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	vm := &vmModel{dir: dir, root: root}
	vm.console = &consoleModel{vm: vm, shell: shell}
	return vm, nil
}

func (v *vmModel) Console() Console { return v.console }

func (v *vmModel) Connect(ctx context.Context) (Terminal, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed.Load() {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shell, err := startShell(v.dir)
	if err != nil {
		return nil, err
	}
	v.sessions = append(v.sessions, shell)
	return &terminalModel{vm: v, shell: shell}, nil
}

func (v *vmModel) Filesystem() Filesystem { return v }

func (v *vmModel) ReadFile(ctx context.Context, path string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed.Load() {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return v.root.ReadFile(path)
}

func (v *vmModel) WriteFile(ctx context.Context, path string, data []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed.Load() {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.root.WriteFile(path, data, 0o600)
}

func (v *vmModel) Shutdown(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed.Swap(true) {
		return nil
	}
	for _, shell := range append(slices.Clone(v.sessions), v.console.shell) {
		if err := shell.stop(ctx); err != nil {
			return err
		}
	}
	if err := v.root.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return nil
}

type consoleModel struct {
	vm    *vmModel
	shell *shellModel
}

func (c *consoleModel) Attach(ctx context.Context) (Terminal, error) {
	if c.vm.closed.Load() {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &terminalModel{vm: c.vm, shell: c.shell, console: true}, nil
}

func (c *consoleModel) Screen(ctx context.Context) (Screen, error) {
	if c.vm.closed.Load() {
		return Screen{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Screen{}, err
	}
	return c.shell.screen(), nil
}

type terminalModel struct {
	vm      *vmModel
	shell   *shellModel
	console bool
	closed  atomic.Bool
}

func (t *terminalModel) Bash() Bash { return t }

func (t *terminalModel) Run(ctx context.Context, script string) (Result, error) {
	if t.closed.Load() || t.vm.closed.Load() {
		return Result{}, ErrClosed
	}
	return t.shell.run(ctx, script)
}

func (t *terminalModel) Disconnect(ctx context.Context) error {
	t.closed.Store(true)
	if t.console {
		return nil
	}
	return t.shell.stop(ctx)
}

// TestSuitePassesAgainstReferenceModel:A07——迁入的参考实现通过
// 全部行为场景(与真实实现共用同一 Suite)。
func TestSuitePassesAgainstReferenceModel(t *testing.T) {
	Suite(t, func() (VM, error) {
		return newModelVM(t)
	})
}

// TestReferenceModelIsolatesRootAndRejectsClosedState:A06——参考
// 实现不访问授权根之外的宿主路径,Shutdown 后入口全部 ErrClosed。
func TestReferenceModelIsolatesRootAndRejectsClosedState(t *testing.T) {
	vm, err := newModelVM(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := vm.Filesystem().WriteFile(t.Context(), "in.txt", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.Filesystem().ReadFile(t.Context(), "../outside.txt"); err == nil {
		t.Fatal("越出授权根的读取必须失败")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := vm.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := vm.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown 应幂等: %v", err)
	}
	if _, err := vm.Filesystem().ReadFile(t.Context(), "in.txt"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := vm.Console().Screen(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
