package native

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/model"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/shell"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

// 屏幕尺寸:控制台的默认终端窗口。
const (
	screenRows = 24
	screenCols = 80
)

// ModelVM 是 model.VM 的真实实现(P15/R01):底座是真实的内核与客体
// 文件系统。控制台是持久会话——断开只释放句柄,会话状态(目录、变量、
// 函数)与屏幕继续存在;Connect 会话各自拥有独立的 Bash 状态;全部
// 会话共享同一份客体文件系统。
type ModelVM struct {
	runtime *Runtime
	console *modelConsole

	mu     sync.Mutex
	closed bool
}

// NewModelVM 装配一台真实 VM 并接入新模型契约。
func NewModelVM(ctx context.Context, opts Options) (*ModelVM, error) {
	if !opts.RealClock {
		// 新模型契约依赖真实时间流逝(后台任务的 sleep 等):默认使用
		// 生产时钟,虚拟时钟留给确定性测试。
		opts.RealClock = true
	}
	runtime, err := New(ctx, opts)
	if err != nil {
		return nil, err
	}
	console, err := newModelConsole(runtime)
	if err != nil {
		return nil, err
	}
	machine := &ModelVM{runtime: runtime, console: console}
	console.vm = machine
	return machine, nil
}

// Console 返回持久控制台。
func (m *ModelVM) Console() model.Console { return m.console }

// Connect 建立一条独立会话:Bash 状态与控制台隔离,文件系统共享。
func (m *ModelVM) Connect(ctx context.Context) (model.Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, model.ErrClosed
	}
	// 每个会话拥有自己的 Bash 父进程:断开一个会话只结束它自己的
	// 会话进程,不影响其他会话。
	parent, err := m.runtime.Kernel.Begin(0, "bash", nil, workRoot, map[string]string{"HOME": workRoot, "TMPDIR": workRoot}, fd.Streams{})
	if err != nil {
		return nil, err
	}
	session, err := shell.NewSlot(m.runtime.Kernel, parent, workRoot, nil)
	if err != nil {
		return nil, err
	}
	return &modelTerminal{vm: m, session: session, parent: parent}, nil
}

// Filesystem 返回共享的客体文件系统视图。
func (m *ModelVM) Filesystem() model.Filesystem { return modelFilesystem{vm: m} }

// isClosed 报告整机是否已关闭。
func (m *ModelVM) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// Shutdown 结束整机:控制台与全部会话失效,内核停止。
func (m *ModelVM) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	m.console.close()
	return m.runtime.Kernel.Close(ctx)
}

// modelConsole 是持久控制台:一个 Bash 槽位与一块屏幕。
type modelConsole struct {
	vm      *ModelVM
	runtime *Runtime
	slot    *shell.Slot
	screen  *tty.Terminal
	parent  proc.Proc

	mu     sync.Mutex
	closed bool
}

// newModelConsole 建立控制台的 Bash 槽位与屏幕:槽位的持续输出写入
// 屏幕,后台进程的输出同样落在屏幕上。
func newModelConsole(runtime *Runtime) (*modelConsole, error) {
	screen, err := tty.New(tty.Flags{}, tty.Size{Rows: screenRows, Cols: screenCols}, 1<<20, nil)
	if err != nil {
		return nil, err
	}
	// 控制台复用 VM 常驻的 Bash 父进程:它属于整机,随整机结束。
	parent := runtime.Parent
	slot, err := shell.NewSlotWithOutput(runtime.Kernel, parent, workRoot, nil, screen)
	if err != nil {
		return nil, err
	}
	return &modelConsole{runtime: runtime, slot: slot, screen: screen, parent: parent}, nil
}

// Attach 接入控制台:同一控制台的多次接入共享 Bash 状态与屏幕。
func (c *modelConsole) Attach(ctx context.Context) (model.Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, model.ErrClosed
	}
	return &modelTerminal{vm: c.vm, session: c.slot, console: c}, nil
}

// Screen 返回控制台的当前屏幕帧。
func (c *modelConsole) Screen(ctx context.Context) (model.Screen, error) {
	if err := ctx.Err(); err != nil {
		return model.Screen{}, err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return model.Screen{}, model.ErrClosed
	}
	lines, row, column := c.screen.ScreenView()
	// 屏幕按固定宽度补齐的行尾空白不属于内容:去掉后与终端实际
	// 呈现的行一致;尾部未使用的空行同样不属于内容。
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed = append(trimmed, strings.TrimRight(line, " "))
	}
	for len(trimmed) > 0 && trimmed[len(trimmed)-1] == "" {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return model.Screen{Lines: trimmed, Cursor: model.Position{Row: row, Column: column}}, nil
}

// close 结束控制台。
func (c *modelConsole) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	_ = c.slot.Close(context.Background())
}

// modelTerminal 是一次会话句柄:Bash 执行与断开。
type modelTerminal struct {
	vm      *ModelVM
	session *shell.Slot
	console *modelConsole
	parent  proc.Proc

	mu     sync.Mutex
	closed bool
}

// Bash 返回该会话的 Bash 接口。
func (t *modelTerminal) Bash() model.Bash { return t }

// Run 执行一段脚本,返回输出与退出码;非零退出码不结束会话。
func (t *modelTerminal) Run(ctx context.Context, script string) (model.Result, error) {
	if err := ctx.Err(); err != nil {
		return model.Result{}, err
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed || (t.vm != nil && t.vm.isClosed()) {
		return model.Result{}, model.ErrClosed
	}
	if t.console != nil {
		t.console.mu.Lock()
		consoleClosed := t.console.closed
		t.console.mu.Unlock()
		if consoleClosed {
			return model.Result{}, model.ErrClosed
		}
	}
	output, err := t.session.Step(ctx, []byte(script))
	if err != nil {
		if errors.Is(err, shell.ErrExited) {
			return model.Result{}, model.ErrBashExited
		}
		if errors.Is(err, errno.Cancelled) {
			return model.Result{}, model.ErrClosed
		}
		return model.Result{}, err
	}
	return model.Result{Output: string(output.Data), ExitCode: output.Status}, nil
}

// State 查询 Bash 是否已执行 exit(P14)。
func (t *modelTerminal) State(ctx context.Context) (model.BashState, error) {
	if err := t.ready(ctx); err != nil {
		return model.BashState{}, err
	}
	state, err := t.session.State(ctx)
	if err != nil {
		return model.BashState{}, err
	}
	return model.BashState{Exited: state.Exited, ExitCode: state.ExitCode}, nil
}

// Restart 显式重建 Bash(P14):返回槽位初始目录与环境,不继承旧
// 会话的变量与函数;不静默发生。
func (t *modelTerminal) Restart(ctx context.Context) error {
	if err := t.ready(ctx); err != nil {
		return err
	}
	return t.session.Restart(ctx)
}

// ready 检查句柄、控制台与整机状态。
func (t *modelTerminal) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed || (t.vm != nil && t.vm.isClosed()) {
		return model.ErrClosed
	}
	if t.console != nil {
		t.console.mu.Lock()
		consoleClosed := t.console.closed
		t.console.mu.Unlock()
		if consoleClosed {
			return model.ErrClosed
		}
	}
	return nil
}

// Interrupt 向当前前台命令投递 SIGINT(P05):命令以非零退出码结束,
// 会话状态保持;无前台命令时为空操作。
func (t *modelTerminal) Interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed || (t.vm != nil && t.vm.isClosed()) {
		return model.ErrClosed
	}
	if t.console != nil {
		t.console.mu.Lock()
		consoleClosed := t.console.closed
		t.console.mu.Unlock()
		if consoleClosed {
			return model.ErrClosed
		}
	}
	return t.session.Interrupt(ctx)
}

// Disconnect 断开会话:控制台接入只释放本句柄(控制台继续可用),
// 独立会话结束自己的 Bash 状态。
func (t *modelTerminal) Disconnect(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	if t.console == nil {
		_ = t.session.Close(ctx)
	}
	return ctx.Err()
}

// modelFilesystem 是共享客体文件系统的模型视图:相对路径以工作区
// 为基准。
type modelFilesystem struct {
	vm *ModelVM
}

// ReadFile 读取客体文件。
func (f modelFilesystem) ReadFile(ctx context.Context, name string) ([]byte, error) {
	if f.vm.isClosed() {
		return nil, model.ErrClosed
	}
	target, err := f.resolve(name)
	if err != nil {
		return nil, err
	}
	file, err := f.vm.runtime.FS.Open(ctx, target, 0, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		read, err := file.Read(chunk)
		if read > 0 {
			buffer = append(buffer, chunk[:read]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	return buffer, nil
}

// WriteFile 写入客体文件。
func (f modelFilesystem) WriteFile(ctx context.Context, name string, data []byte) error {
	if f.vm.isClosed() {
		return model.ErrClosed
	}
	target, err := f.resolve(name)
	if err != nil {
		return err
	}
	file, err := f.vm.runtime.FS.Open(ctx, target, vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// resolve 把模型路径解析为客体绝对路径:相对路径以工作区为基准。
func (f modelFilesystem) resolve(name string) (string, error) {
	if name == "" {
		return "", model.ErrClosed
	}
	if strings.HasPrefix(name, "/") {
		return path.Clean(name), nil
	}
	return path.Join(workRoot, name), nil
}
