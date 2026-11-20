package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/contract"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/deployment"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/guest/rootfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/inspect"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/shell"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/storage"
	"mvdan.cc/sh/v3/interp"
)

type Options struct {
	Seed uint64

	MaxProcesses int

	Session resources.Limits

	Process resources.Limits

	// RealClock 为真时使用宿主生产时钟(真实时间流逝);默认使用虚拟
	// 时钟,便于确定性测试与重放。
	RealClock bool
}

const (
	defaultMaxProcesses = 128
	defaultMemory       = 256 << 20
	defaultWork         = 4 << 20

	workRoot = "/work"
)

type Runtime struct {
	Kernel    *vm.Kernel
	FS        *memfs.FS
	Store     *storage.Memory
	Clock     clock.Source
	Entropy   *entropy.Seeded
	Sequencer *scheduler.Sequencer
	Meters    *resources.Meters

	// Env 是客体会话的默认环境(HOME/PATH/LANG 等,N04)。
	Env map[string]string

	// Parent 是常驻客体会话(Bash):控制台与连接的子进程都挂在它下面。
	Parent proc.Proc
}

func New(ctx context.Context, opts Options) (*Runtime, error) {
	var clk clock.Source = &clock.Virtual{}
	if opts.RealClock {
		clk = clock.NewReal()
	}
	layout, err := rootfs.Build(ctx, rootfs.Options{Clock: clk, Workdir: workRoot})
	if err != nil {
		return nil, err
	}
	filesystem := layout.System
	if opts.MaxProcesses <= 0 {
		opts.MaxProcesses = defaultMaxProcesses
	}
	if opts.Session.Memory == 0 {
		opts.Session.Memory = defaultMemory
	}
	if opts.Session.Work == 0 {
		opts.Session.Work = defaultWork
	}
	if opts.Process.Memory == 0 {
		opts.Process.Memory = defaultMemory / 4
	}
	if opts.Process.Work == 0 {
		opts.Process.Work = defaultWork / 4
	}
	meters, err := resources.NewSession(opts.Session, opts.Process)
	if err != nil {
		return nil, err
	}
	store := storage.New(0)
	sequencer := scheduler.New(0)
	seeded := entropy.NewSeeded(opts.Seed)

	var kernel *vm.Kernel
	var parent proc.Proc
	factory := func(dir string, env []string, in io.Reader, out, diagnostic io.Writer) (*interp.Runner, error) {
		return shell.NewRunner(kernel, parent, dir, env, in, out, diagnostic)
	}
	registry, err := shell.Programs(factory)
	if err != nil {
		return nil, err
	}
	fixtures, err := executable.New(fixtures())
	if err != nil {
		return nil, err
	}
	merged, err := executable.Merge(registry, fixtures)
	if err != nil {
		return nil, err
	}
	kernel, err = vm.New(vm.Services{
		Set: capability.Set{
			FS:       filesystem,
			FD:       fd.New(),
			Identity: identity.SingleUser{},
			Clock:    clk,
			Entropy:  seeded,

			Resources: meters,
		},
		Programs:  merged,
		Meters:    meters,
		Sequencer: sequencer,

		Binaries: elf.Factory{},
	}, opts.MaxProcesses)
	if err != nil {
		return nil, err
	}
	parent, err = kernel.Begin(0, "bash", nil, workRoot, layout.Environment(), fd.Streams{})
	if err != nil {
		return nil, err
	}
	return &Runtime{Kernel: kernel, FS: filesystem, Store: store, Clock: clk, Entropy: seeded, Sequencer: sequencer, Meters: meters, Env: layout.Environment(), Parent: parent}, nil
}

func (r *Runtime) Session() contract.Session {
	return &session{runtime: r}
}

func (r *Runtime) Matrix() deployment.Matrix {
	return deployment.Matrix{
		Name:   "native",
		Deploy: "in-process",
		Executors: map[string]deployment.Support{
			"builtin-go": deployment.Supported,
			"shebang":    deployment.Partial,
			"wasi":       deployment.Unsupported,

			"elf": deployment.Partial,
		},
		Signals:  deployment.Partial,
		TTY:      deployment.Partial,
		Snapshot: deployment.Supported,
		Replay:   deployment.Supported,
		Network:  deployment.Unsupported,

		AddressSpace: deployment.Partial,
		Notes: []string{
			"终止类信号以协作方式投递；SIGSTOP/SIGCONT 无抢占语义，明确 ENOTSUP",
			"shebang 仅支持注册表内解释器，不做宿主 PATH 搜索",
			"TTY 行规程完整，但 ISIG 挂起（TSTP）依赖的抢占不可用",
			"快照仅限静止安全点：无活动子进程且无执行中程序",
			"管道由解释器协作驱动；确定性虚拟调度器尚待实现",
			"静态 Linux ELF：映像从客体文件系统读取，由 RV64IM 解释执行、ECALL 交给 Linux ABI 适配层；不支持动态链接、TLS 与线程，没有 mmap（brk 可用，按页增长 program break）",
			"ELF 程序的 openat 走作用域视图透出的安装能力（描述符归进程、cloexec 生效），能自己打开并读取文件；地址空间按进程登记到内核（inspect 与审计能读到，越权映射由地址空间本身拒绝）；指令数与映射字节都按进程扣减，死循环在预算内停下",
			"文件模式位（chmod）由宿主适配层提供（vm/fs.Modes，经 os.Root 解析，逃不出已授权根）；客体是单用户模型，没有 uid/gid，setuid/setgid 只作为存储位保留、没有特权语义，类型位不能靠改模式来改",
		},
	}
}

type session struct {
	runtime *Runtime
	mu      sync.Mutex
	closed  bool
}

func (s *session) Exec(ctx context.Context, spec contract.ExecSpec) (contract.Process, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errno.Cancelled
	}
	s.mu.Unlock()
	dir := spec.Dir
	if dir == "" {
		dir = workRoot
	}
	proc_, err := s.runtime.Kernel.Begin(0, spec.Program, spec.Args, dir, spec.Env, fd.Streams{
		Input: spec.Stdin, Output: spec.Stdout, Error: spec.Stderr,
	})
	if err != nil {
		return nil, err
	}
	handle := &process{id: uint64(proc_.ID), kernel: s.runtime.Kernel, done: make(chan struct{})}
	go func() {
		defer close(handle.done)
		status, runErr := s.runtime.Kernel.RunRegistered(ctx, proc_)
		finishErr := s.runtime.Kernel.Finish(0, proc_.ID, status)
		handle.mu.Lock()
		handle.state = toExitState(status, runErr)
		handle.err = errors.Join(runErr, finishErr)
		handle.mu.Unlock()
	}()
	return handle, nil
}

func (s *session) Inspect(ctx context.Context) (contract.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, err
	}
	snapshot, err := inspect.Kernel(s.runtime.Kernel)
	if err != nil {
		return contract.Snapshot{}, err
	}
	out := contract.Snapshot{}
	for _, view := range snapshot.Processes {
		out.Processes = append(out.Processes, contract.ProcessView{
			ID:      view.ID,
			Parent:  view.Parent,
			Program: view.Program,
			State:   view.State,
			OpenFDs: len(view.OpenFDs),
		})
	}
	return out, nil
}

func (s *session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.runtime.Kernel.Close(ctx)
}

type process struct {
	id     uint64
	kernel *vm.Kernel
	done   chan struct{}
	mu     sync.Mutex
	state  contract.ExitState
	err    error
}

func (p *process) ID() uint64 { return p.id }

func (p *process) Wait(ctx context.Context) (contract.ExitState, error) {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.state, p.err
	case <-ctx.Done():
		return contract.ExitState{}, ctx.Err()
	}
}

func (p *process) Signal(ctx context.Context, name string) error {
	n, err := signal.ByName(name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.kernel.Signal(proc.PID(p.id), n)
}

func toExitState(status vm.ExitStatus, runErr error) contract.ExitState {
	state := contract.ExitState{Code: status.Code, Reason: contract.ReasonExit}
	if status.Signal != 0 {
		state.Reason = contract.ReasonSignaled
		state.Signal = status.Signal.Name()
		return state
	}
	if runErr != nil {
		state.Reason = contract.ReasonFault
	}
	return state
}

func fixtures() map[string]executable.Program {
	return map[string]executable.Program{
		contract.FixtureEcho: executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			descriptors, err := p.Resources.Descriptors()
			if err != nil {
				return 0, err
			}
			out, err := descriptors.Get(ctx, uint64(p.ID), 1)
			if err != nil {
				return 0, err
			}
			_, err = fmt.Fprintln(out, joinArgs(p.Args))
			return 0, err
		}),
		contract.FixtureFail: executable.Func(func(context.Context, proc.Proc) (uint8, error) {
			return 3, nil
		}),
		contract.FixtureSleep: executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			<-ctx.Done()
			return 0, nil
		}),
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, arg := range args {
		if i > 0 {
			out += " "
		}
		out += arg
	}
	return out
}
