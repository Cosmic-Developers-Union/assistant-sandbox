//go:build linux && amd64

package instance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/model"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

var nextNativeModelID atomic.Uint64

// NativeModelVM 用真实 amd64 客体 Bash/PTY 实现共享 model.VM 契约。
type NativeModelVM struct {
	machine *NativeVM
	console *nativeModelConsole
	root    *os.Root
	id      string

	nextTerminal atomic.Uint64
	mu           sync.Mutex
	closed       bool
}

// NewNativeModelVM 创建原生后端模型 VM；能力缺失时返回具名错误。
func NewNativeModelVM(ctx context.Context, opts Options) (*NativeModelVM, error) {
	if _, err := probeNativeBackend(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNativeCapability, err)
	}
	return newNativeModelVM(ctx, opts)
}

func newNativeModelVM(ctx context.Context, opts Options) (*NativeModelVM, error) {
	machine, err := newNativeVM(ctx, opts)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(machine.root)
	if err != nil {
		_ = machine.Close(context.Background())
		return nil, fmt.Errorf("open native model filesystem: %w", err)
	}
	session, err := machine.StartSession(ctx, nil)
	if err != nil {
		_ = root.Close()
		_ = machine.Close(context.Background())
		return nil, err
	}
	machineVM := &NativeModelVM{
		machine: machine, root: root, id: fmt.Sprintf("native-vm-%d", nextNativeModelID.Add(1)),
	}
	machineVM.console = &nativeModelConsole{vm: machineVM, session: session}
	return machineVM, nil
}

// ID 返回进程内稳定的 VM 标识。
func (v *NativeModelVM) ID() string { return v.id }

// Console 返回持久控制台。
func (v *NativeModelVM) Console() model.Console { return v.console }

// Connect 建立拥有独立 Bash 状态的会话。
func (v *NativeModelVM) Connect(ctx context.Context) (model.Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if v.isClosed() {
		return nil, model.ErrClosed
	}
	session, err := v.machine.StartSession(ctx, nil)
	if err != nil {
		if errors.Is(err, errno.Cancelled) {
			return nil, model.ErrClosed
		}
		return nil, err
	}
	if v.isClosed() {
		_ = session.Close(context.Background())
		return nil, model.ErrClosed
	}
	return &nativeModelTerminal{vm: v, session: session, id: v.terminalID()}, nil
}

func (v *NativeModelVM) terminalID() string {
	return fmt.Sprintf("%s/terminal/%d", v.id, v.nextTerminal.Add(1))
}

// Filesystem 返回 VM 级共享客体文件系统。
func (v *NativeModelVM) Filesystem() model.Filesystem { return nativeModelFilesystem{vm: v} }

func (v *NativeModelVM) isClosed() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.closed
}

// Shutdown 结束控制台、全部连接和原生 VM。
func (v *NativeModelVM) Shutdown(ctx context.Context) error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	v.mu.Unlock()
	rootErr := v.root.Close()
	return errors.Join(rootErr, v.machine.Close(ctx))
}

type nativeModelConsole struct {
	vm      *NativeModelVM
	session *NativeSession
}

// ID 返回持久控制台标识。
func (c *nativeModelConsole) ID() string { return c.vm.id + "/console" }

// Attach 返回共享持久 Bash 与屏幕的新句柄。
func (c *nativeModelConsole) Attach(ctx context.Context) (model.Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.vm.isClosed() {
		return nil, model.ErrClosed
	}
	return &nativeModelTerminal{vm: c.vm, session: c.session, console: c, id: c.vm.terminalID()}, nil
}

// Screen 返回清除行尾空白后的当前屏幕帧。
func (c *nativeModelConsole) Screen(ctx context.Context) (model.Screen, error) {
	if err := ctx.Err(); err != nil {
		return model.Screen{}, err
	}
	if c.vm.isClosed() {
		return model.Screen{}, model.ErrClosed
	}
	lines, row, column := c.session.ScreenView()
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed = append(trimmed, strings.TrimRight(line, " "))
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return model.Screen{Lines: trimmed, Cursor: model.Position{Row: row, Column: column}}, nil
}

type nativeModelTerminal struct {
	vm      *NativeModelVM
	session *NativeSession
	console *nativeModelConsole
	id      string

	mu     sync.Mutex
	closed bool
}

// ID 返回连接句柄标识。
func (t *nativeModelTerminal) ID() string { return t.id }

// Bash 返回该句柄的 Bash 接口。
func (t *nativeModelTerminal) Bash() model.Bash { return t }

// Run 在持久 Bash 中执行脚本并返回前台输出与退出码。
func (t *nativeModelTerminal) Run(ctx context.Context, script string) (model.Result, error) {
	if err := t.ready(ctx); err != nil {
		return model.Result{}, err
	}
	output, code, err := t.session.runScript(ctx, script)
	if errors.Is(err, errNativeBashExited) {
		return model.Result{}, model.ErrBashExited
	}
	if errors.Is(err, errno.Cancelled) && t.vm.isClosed() {
		return model.Result{}, model.ErrClosed
	}
	return model.Result{Output: output, ExitCode: code}, err
}

// Interrupt 向前台客体任务投递 SIGINT。
func (t *nativeModelTerminal) Interrupt(ctx context.Context) error {
	if err := t.ready(ctx); err != nil {
		return err
	}
	return t.session.Interrupt()
}

// Disconnect 释放句柄；控制台会话保留，独立会话结束。
func (t *nativeModelTerminal) Disconnect(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	if t.console == nil {
		return t.session.Close(ctx)
	}
	return ctx.Err()
}

func (t *nativeModelTerminal) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed || t.vm.isClosed() {
		return model.ErrClosed
	}
	return nil
}

type nativeModelFilesystem struct {
	vm *NativeModelVM
}

// ReadFile 读取客体文件；相对路径以 /work 为基准。
func (f nativeModelFilesystem) ReadFile(ctx context.Context, name string) ([]byte, error) {
	if err := f.ready(ctx, name); err != nil {
		return nil, err
	}
	return f.vm.root.ReadFile(f.resolve(name))
}

// WriteFile 写入客体文件；相对路径以 /work 为基准。
func (f nativeModelFilesystem) WriteFile(ctx context.Context, name string, data []byte) error {
	if err := f.ready(ctx, name); err != nil {
		return err
	}
	return f.vm.root.WriteFile(f.resolve(name), data, 0o644)
}

func (f nativeModelFilesystem) ready(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" || f.vm.isClosed() {
		return model.ErrClosed
	}
	return nil
}

func (f nativeModelFilesystem) resolve(name string) string {
	if path.IsAbs(name) {
		return strings.TrimPrefix(path.Clean(name), "/")
	}
	return path.Join("work", name)
}
