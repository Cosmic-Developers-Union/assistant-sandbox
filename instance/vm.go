// Package instance 装配一台虚拟机(C02/C03/C04):VM 拥有独立的内核
// 实例、根文件系统与挂载表、时钟、随机源与设备注册表。同一台 VM 的
// 多个连接共享这些资源(同一进程表、同一文件系统、同一设备),不同
// VM 之间互不可见;关闭一条连接只释放该连接,不销毁 VM 的设备。
package instance

import (
	"context"
	"io"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/devfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/mountfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/shell"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	vmentropy "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

// 客体目录布局:工作区、临时目录与设备挂载点。
const (
	WorkRoot = "/work"
	TempRoot = "/tmp"
	DevRoot  = "/dev"
)

const defaultMaxProcesses = 128

// Options 是 VM 的构造参数。
type Options struct {
	// MaxProcesses 是进程表上限;非正时使用默认值。
	MaxProcesses int

	// Seed 是随机源种子;相同种子产生相同序列(确定性模式)。
	Seed uint64

	// Deterministic 为真时使用虚拟时钟,便于测试与重放;
	// 否则使用宿主生产时钟。
	Deterministic bool

	// TraceLimit 是系统调用轨迹保留上限;非正时使用默认值。
	TraceLimit int

	// Programs 是追加到内置 Shell 程序表之上的客体程序;同名条目
	// 覆盖内置实现。
	Programs map[string]executable.Program

	// Binaries 是客体二进制工厂;为空时使用 RV64 ELF 工厂。
	Binaries executable.BinaryFactory

	// faults 是初始化失败注入点(仅供同包测试)。
	faults initFaults
}

// VM 是一台虚拟机:拥有自己的内核实例、根文件系统与挂载表、时钟、
// 随机源与设备注册表。连接只是 VM 的句柄,不拥有资源。
type VM struct {
	kernel *vm.Kernel
	// session 是 VM 的常驻客体会话(Shell):连接的子进程都挂在它
	// 下面,退出后保持僵尸态由等待方回收(Linux 语义)。
	session proc.Proc
	root    *memfs.FS
	mounts  *mountfs.Mounts
	devices *devfs.FS
	clock   clock.Source
	entropy vmentropy.Source

	mu          sync.Mutex
	closed      bool
	nextID      uint64
	connections map[uint64]*Connection

	// running 记录正在执行的客体程序及其取消函数:关闭时统一取消,
	// 不依赖执行器是否已完成登记。
	running map[proc.PID]context.CancelFunc

	// executions 统计尚未结束的客体执行,关闭时等待其全部结束。
	executions sync.WaitGroup
}

// New 装配一台 VM:建立根文件系统与挂载表、时钟、随机源与设备注册
// 表,并创建独立的内核实例。
func New(ctx context.Context, opts Options) (*VM, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source := clockFor(opts)
	root, err := memfs.New(source, memfs.Options{})
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{WorkRoot, TempRoot} {
		if err := root.Mkdir(ctx, dir, 0o755); err != nil {
			return nil, err
		}
	}
	return newVM(ctx, opts, root, source)
}

// clockFor 按选项选择 VM 的时钟来源:确定性模式用虚拟时钟,否则用
// 宿主生产时钟。
func clockFor(opts Options) clock.Source {
	if opts.Deterministic {
		return &clock.Virtual{}
	}
	return clock.NewReal()
}

// entropyFor 按选项选择 VM 的随机源:确定性模式或指定种子时用可重放
// 的种子源,否则用生产熵源。
func entropyFor(opts Options) vmentropy.Source {
	if opts.Deterministic || opts.Seed != 0 {
		return vmentropy.NewSeeded(opts.Seed)
	}
	return entropy.NewProduction()
}

// newVM 用给定的根文件系统装配 VM。初始化任一步骤失败即回滚:
// 已创建的内核被关闭、句柄释放,不留半成品(C09)。
func newVM(ctx context.Context, opts Options, root *memfs.FS, source clock.Source) (*VM, error) {
	if opts.MaxProcesses <= 0 {
		opts.MaxProcesses = defaultMaxProcesses
	}
	random := entropyFor(opts)
	mounts := &mountfs.Mounts{}
	if err := mounts.Mount("/", root); err != nil {
		return nil, err
	}
	if err := opts.faults.afterMounts(); err != nil {
		return nil, err
	}
	devices, err := devfs.New(devfs.Options{Entropy: random})
	if err != nil {
		return nil, err
	}
	if err := mounts.Mount(DevRoot, devices); err != nil {
		return nil, err
	}
	if err := opts.faults.afterDevices(); err != nil {
		return nil, err
	}

	machine := &VM{
		root: root, mounts: mounts, devices: devices,
		clock: source, entropy: random,
		connections: map[uint64]*Connection{},
		running:     map[proc.PID]context.CancelFunc{},
	}
	var parent proc.Proc
	factory := func(dir string, environment []string, in io.Reader, out, diagnostic io.Writer) (*interp.Runner, error) {
		return shell.NewRunner(machine.kernel, parent, dir, environment, in, out, diagnostic)
	}
	shellPrograms, err := shell.Programs(factory)
	if err != nil {
		return nil, err
	}
	registry := shellPrograms
	if len(opts.Programs) > 0 {
		extra, err := executable.New(opts.Programs)
		if err != nil {
			return nil, err
		}
		merged, err := executable.Merge(shellPrograms, extra)
		if err != nil {
			return nil, err
		}
		registry = merged
	}
	binaries := opts.Binaries
	if binaries == nil {
		binaries = elf.Factory{}
	}
	kernel, err := vm.New(vm.Services{
		Set: capability.Set{
			FS:       mounts,
			FD:       fd.New(),
			Identity: identity.SingleUser{},
			Clock:    source,
			Entropy:  random,
		},
		Programs: registry,
		Binaries: binaries,
	}, opts.MaxProcesses)
	if err != nil {
		return nil, err
	}
	machine.kernel = kernel
	if opts.faults.observe != nil {
		opts.faults.observe(kernel)
	}
	if opts.TraceLimit > 0 {
		kernel.SetTraceLimit(opts.TraceLimit)
	}
	if err := opts.faults.afterKernel(); err != nil {
		_ = kernel.Close(ctx)
		return nil, err
	}
	// 客体 Shell 会话:作为内核的常驻父进程,连接的子进程都挂在它下面。
	parent, err = kernel.Begin(0, "bash", nil, WorkRoot, map[string]string{"HOME": WorkRoot, "TMPDIR": TempRoot}, fd.Streams{})
	if err != nil {
		_ = kernel.Close(ctx)
		return nil, err
	}
	machine.session = parent
	if err := opts.faults.afterSession(); err != nil {
		_ = kernel.Close(ctx)
		return nil, err
	}
	return machine, nil
}

// Kernel 返回 VM 的内核实例:同一台 VM 的连接共享它。
func (v *VM) Kernel() *vm.Kernel { return v.kernel }

// Filesystem 返回 VM 的根文件系统(经挂载表解析)。
func (v *VM) Filesystem() vmfs.System { return v.mounts }

// Mounts 返回 VM 的挂载表。
func (v *VM) Mounts() *mountfs.Mounts { return v.mounts }

// Root 返回 VM 的根文件系统本体(内存介质)。
func (v *VM) Root() *memfs.FS { return v.root }

// Devices 返回 VM 的设备注册表(devfs)。
func (v *VM) Devices() *devfs.FS { return v.devices }

// Clock 返回 VM 的时钟来源。
func (v *VM) Clock() clock.Source { return v.clock }

// Entropy 返回 VM 的随机源。
func (v *VM) Entropy() vmentropy.Source { return v.entropy }

// Connect 建立一条到 VM 的连接:连接共享 VM 的内核、文件系统与设备。
func (v *VM) Connect(ctx context.Context) (*Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, errno.Cancelled
	}
	v.nextID++
	connection := &Connection{vm: v, id: v.nextID}
	v.connections[v.nextID] = connection
	return connection, nil
}

// Connections 返回当前连接数。
func (v *VM) Connections() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.connections)
}

// Close 关闭 VM:内核停止、全部连接失效。重复关闭幂等。
func (v *VM) Close(ctx context.Context) error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	connections := make([]*Connection, 0, len(v.connections))
	for _, connection := range v.connections {
		connections = append(connections, connection)
	}
	v.connections = map[uint64]*Connection{}
	v.mu.Unlock()
	for _, connection := range connections {
		connection.markClosed()
	}
	return v.kernel.Close(ctx)
}

// Closed 报告 VM 是否已关闭。
func (v *VM) Closed() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.closed
}

// initFaults 是初始化过程中的失败注入点(C09):仅供同包测试逐个注入
// 失败,验证回滚不泄漏内核、句柄与文件。
type initFaults struct {
	mounts  error
	devices error
	kernel  error
	session error

	// observe 观察已创建的内核实例(测试用)。
	observe func(*vm.Kernel)
}

func (f initFaults) afterMounts() error  { return f.mounts }
func (f initFaults) afterDevices() error { return f.devices }
func (f initFaults) afterKernel() error  { return f.kernel }
func (f initFaults) afterSession() error { return f.session }

// registerRunning 登记正在执行的客体程序及其取消函数。
func (v *VM) registerRunning(pid proc.PID, cancel context.CancelFunc) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.running == nil {
		v.running = map[proc.PID]context.CancelFunc{}
	}
	v.running[pid] = cancel
}

// unregisterRunning 注销已结束的客体程序。
func (v *VM) unregisterRunning(pid proc.PID) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.running, pid)
}

// cancelRunning 取消全部正在执行的客体程序:协作退出的第一手段,
// 与执行器是否完成登记无关。
func (v *VM) cancelRunning() {
	v.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(v.running))
	for _, cancel := range v.running {
		cancels = append(cancels, cancel)
	}
	v.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
