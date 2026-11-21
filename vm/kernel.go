package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/bridge"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/futex"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/inotify"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/ipc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/job"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/locks"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/pipe"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signalfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/trace"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

type Services struct {
	capability.Set
	Programs executable.Registry

	Meters *resources.Meters

	Sequencer *scheduler.Sequencer

	Journal *replay.Memory

	Identity replay.Header

	Binaries executable.BinaryFactory

	// NetworkBudget 是每 VM 网络连接/流量预算(M12);零值不限制。
	NetworkBudget net.Budget
}

const spawnWorkUnits = 1

type interrupt struct{ n signal.Number }

func (i interrupt) Error() string { return fmt.Sprintf("信号 %v 终止", i.n) }

type ExitStatus struct {
	Code   uint8
	Signal signal.Number
}

type Kernel struct {
	mu       sync.Mutex
	table    *proc.Table
	services Services
	meters   *resources.Meters

	childrenWork map[proc.PID]uint64

	budgetFunc BudgetFunc
	sequencer  *scheduler.Sequencer
	signals    *signal.Table
	fds        *fd.Table
	init       proc.PID

	bootTime     int64
	bootDeclared bool

	executing bool
	failed    bool
	closed    bool

	life lifecycle

	children  wait.Queue
	instances map[proc.PID]executable.Instance
	resolver  *executable.Resolver
	auditSeq  uint64

	spaces map[proc.PID]memory.Space

	// prepared 暂存 vfork 子进程 exec 的待提交换绑表(H12):装载
	// 成功后 Commit 接管,失败 Discard 丢弃。
	prepared map[proc.PID]*memory.Table

	// notifies 是 inotify 的事件扇出注册表(I20):文件类系统调用
	// 成功后事件送达全部命中监视,跨进程可见。
	notifies *inotify.Broker

	// network 是 VM 独立的网络命名空间(M01):回环撮合与端口
	// 注册表随内核创建与关闭,两台 VM 互不可见。
	network *net.Namespace

	jobsMu      sync.Mutex
	jobsRunning bool

	// queue 是 H02/H03 的可运行队列:执行权令牌实现单核语义,
	// 时间片与系统调用边界让出,驻停与退出归还。
	queue *runQueue

	// futexes 是 futex 的等待表(K05):按 {所有者,客体地址} 键控。
	futexes *futex.Table

	// tidAddresses 是各进程登记的 clear_child_tid 地址(K03):线程/
	// 进程退出时该处写零并唤醒 futex;fork 不继承,exec 复位。
	tidAddresses map[proc.PID]uint64

	// robustLists 是各线程登记的 robust futex 链表头与长度(K07):
	// set_robust_list 登记,线程退出时清算;fork 不继承,exec 复位。
	robustLists map[proc.PID]robustList

	// locks 是字节范围文件锁表(I14):fcntl F_SETLK/F_GETLK 键控,
	// 进程退出统一释放。
	locks *locks.Table

	// trace 收集受限的系统调用轨迹(G08):有界,不记录缓冲区内容。
	trace *trace.Recorder

	// crashes 保留最近的致命故障报告(T02):有界,值类型快照。
	crashes []CrashReport

	// credentials 是每进程 UID/GID 表(O01):与宿主身份完全解耦。
	credentials *identity.Table

	// gaps 是运行时启动缺口(Q02):每进程保留首个缺口。
	gaps map[proc.PID]GapReport
}

// robustList 是 set_robust_list 登记的用户态链表描述(K07):
// head 指向 linked list 节点(每节点含 next 指针与 futex 字),len
// 为链表大小上界(本实现按节点遍历,不依赖 len 精确值)。
type robustList struct {
	head uint64
	len  uint64
}

// futexTidMask/futexWaitersBit/futexOwnerDiedBit 是 Linux futex 字的
// 常量(robust 清算语义)。
const (
	futexTidMask      = uint32(0x3fffffff)
	futexWaitersBit   = uint32(1) << 31
	futexOwnerDiedBit = uint32(1) << 30
)

func New(services Services, maxProcesses int) (*Kernel, error) {
	if _, err := capability.Require(services.Programs); err != nil {
		return nil, fmt.Errorf("缺少客体程序注册表: %w", errno.Invalid)
	}
	table, err := proc.New(maxProcesses)
	if err != nil {
		return nil, err
	}
	if _, err := capability.Require(services.Net); err != nil {
		services.Net = net.Denied{}
	}
	if _, err := capability.Require(services.IPC); err != nil {
		services.IPC = ipc.Denied{}
	}

	var kernel *Kernel
	if _, err := capability.Require(services.Clock); err == nil {
		kernel = &Kernel{}
		wrapped := yieldClock{inner: services.Clock, k: kernel}
		services.Clock = wrapped
		services.Clock = wrapped
	}

	if services.Journal != nil {
		if source, err := capability.Require(services.Entropy); err == nil {
			services.Entropy = entropy.NewRecording(source, services.Journal, 0)
		}
		if host, err := capability.Require(services.Bridge); err == nil {
			services.Bridge = bridge.NewRecording(host, services.Journal)
		}

		if services.Journal.Mode() == replay.ModeRecord {
			if _, ok := services.Journal.Header(); !ok {
				header := services.Identity
				header.Format = replay.HeaderFormat
				header.Mode = replay.ModeRecord
				if err := services.Journal.SetHeader(context.Background(), header); err != nil {
					return nil, err
				}
			}
		}
	}

	var bootTimeValue int64
	var bootTimeDeclared bool
	if clk, err := capability.Require(services.Clock); err == nil {
		if now, wallErr := clk.Wall(context.Background()); wallErr == nil {
			bootTimeValue, bootTimeDeclared = now, true
		}
	}
	root, err := table.Create(0, "init", nil, "/", nil)
	if err != nil {
		return nil, err
	}
	if err := table.Start(root.ID); err != nil {
		return nil, err
	}
	k := kernel
	if k == nil {
		k = &Kernel{}
	}
	k.bootTime = bootTimeValue
	k.bootDeclared = bootTimeDeclared
	k.table = table
	k.trace = trace.New(trace.DefaultLimit)
	if services.CredentialTable == nil {
		services.CredentialTable = identity.NewTable(identity.Identity{Name: "sandbox"})
	}
	k.credentials = services.CredentialTable
	k.services = services
	k.meters = services.Meters
	k.sequencer = services.Sequencer
	k.signals = signal.New()
	k.instances = map[proc.PID]executable.Instance{}
	k.queue = newRunQueue()
	k.futexes = futex.New()
	k.tidAddresses = map[proc.PID]uint64{}
	k.robustLists = map[proc.PID]robustList{}
	k.prepared = map[proc.PID]*memory.Table{}
	k.notifies = inotify.NewBroker()
	k.network = net.New()
	k.network.SetBudget(services.NetworkBudget)
	if policy := services.Egress; policy != nil {
		if host, err := capability.Require(services.Net); err == nil {
			k.network.SetEgress(host, policy)
		}
	}
	k.locks = locks.New()
	k.init = root.ID
	k.signals.Spawn(uint64(root.ID))
	if fdt, ok := services.FD.(*fd.Table); ok {
		k.fds = fdt
		// timerfd(L10)的时钟端口随描述符表装配。
		if clk, err := capability.Require(services.Clock); err == nil {
			k.fds.SetTimeSource(clk)
		}
		// O05:全部描述符创建路径共享预算,由表内钩子计费/归还。
		if k.meters != nil {
			k.fds.SetBudget(metersBudget{meters: k.meters})
		}
	}

	k.services.Spaces = kernelSpaces{kernel: k}

	k.services.UmaskSource = kernelUmask{kernel: k}
	if _, err := capability.Require(services.FS); err == nil {
		resolver := executable.NewResolver(services.Programs, services.FS)

		if services.Binaries != nil {
			resolver = resolver.WithBinaries(services.Binaries)
		}
		k.resolver = resolver
	}
	if err := k.life.enter(StateRunning); err != nil {
		return nil, err
	}
	return k, nil
}

// NetworkNamespace 返回本 VM 的网络命名空间(M01/M13),供部署层显式
// 发布客体端口。
func (k *Kernel) NetworkNamespace() *net.Namespace { return k.network }

func (k *Kernel) Processes() []proc.Proc { k.mu.Lock(); defer k.mu.Unlock(); return k.table.List() }

// Trace 返回本 VM 的受限系统调用轨迹记录器(G08):只含 PID/TID、
// 调用编号、结果与耗时,条数有界。
func (k *Kernel) Trace() *trace.Recorder { return k.trace }

// SetTraceLimit 调整轨迹保留上限(缩小会丢弃最旧条目)。
func (k *Kernel) SetTraceLimit(limit int) {
	if k.trace == nil {
		return
	}
	k.trace.SetLimit(limit)
}

func (k *Kernel) SystemConfiguration() (proc.SystemConfiguration, error) {
	return proc.SystemConfiguration{
		BootTime:     k.bootTime,
		BootDeclared: k.bootDeclared,
		ChildMax:     int64(k.table.Limit()),
		ArgMax:       proc.ArgMax,
	}, nil
}

func (k *Kernel) HasProgram(name string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return false
	}
	_, err := k.resolve(name)
	return err == nil
}

func (k *Kernel) Init() proc.PID { return k.table.Init() }

type Feature uint8

const (
	ScheduledJobs Feature = iota

	PackageManagement
	Sudo
	MultiUser
)

func (k *Kernel) CheckFeature(feature Feature) error {
	switch feature {
	case ScheduledJobs:
		return nil
	case PackageManagement, Sudo, MultiUser:
		return errno.NotSupported
	default:
		return errno.Invalid
	}
}

func (k *Kernel) Clock() (clock.Source, error) {
	return capability.Require(k.services.Clock)
}

func (k *Kernel) Exec(ctx context.Context, name string, args []string) (proc.Proc, error) {
	if err := ctx.Err(); err != nil {
		return proc.Proc{}, err
	}
	k.mu.Lock()
	if k.executing || k.failed {
		k.mu.Unlock()
		return proc.Proc{}, errno.Busy
	}
	k.executing = true
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.executing = false
		if r := recover(); r != nil {
			k.failed = true
			_ = k.life.enter(StateFailed)
			_ = k.life.enter(StateFailed)
			panic(r)
		}
	}()

	p, err := k.Begin(0, name, args, "/", nil, fd.Streams{})
	if err != nil {
		return proc.Proc{}, err
	}
	status, runErr := k.RunRegistered(ctx, p)
	if ctxErr := ctx.Err(); ctxErr != nil {
		runErr = errors.Join(runErr, ctxErr)
		if status.Signal == 0 {
			status.Code = 125
		}
	}

	k.mu.Lock()
	exited, getErr := k.table.Get(p.ID)
	k.mu.Unlock()
	if getErr != nil {

		return proc.Proc{
			ID: p.ID, Program: name, State: proc.Zombie,
			ExitCode: status.Code, ExitSignal: uint8(status.Signal),
		}, runErr
	}
	if runErr != nil {
		return exited, fmt.Errorf("执行客体程序%s: %w", name, runErr)
	}
	return exited, nil
}

func (k *Kernel) Reap(id proc.PID) (proc.Proc, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, err := k.table.Reap(k.init, id)
	if err == nil {
		err = k.afterReapLocked(p)
	}
	return p, err
}

// exitResourcesLocked 在进程退出(转僵尸)时清算资源:信号状态、
// 计量、描述符与地址空间绑定。僵尸不持有任何资源(H10:回收只
// 移除表项;K10:fork 子进程的僵尸由父进程 wait*,不再于 Finish
// 自动回收——资源必须在退出点释放而非回收点)。
func (k *Kernel) exitResourcesLocked(p proc.Proc) error {
	if k.locks != nil {
		k.locks.ReleaseHolder(uint64(p.ID))
	}
	k.signals.Discard(uint64(p.ID))
	if k.meters != nil {

		if view, err := k.meters.View(context.Background(), uint64(p.ID)); err == nil && view.WorkUsed > 0 {
			if k.childrenWork == nil {
				k.childrenWork = map[proc.PID]uint64{}
			}
			k.childrenWork[p.Parent] += view.WorkUsed
		}
		k.meters.Discard(uint64(p.ID))
	}
	delete(k.childrenWork, p.ID)
	k.credentials.Remove(uint64(p.ID))
	var closeErr error
	if k.fds != nil {
		closeErr = k.fds.Detach(uint64(p.ID))
	}

	delete(k.spaces, p.ID)
	return closeErr
}

func (k *Kernel) afterReapLocked(p proc.Proc) error {
	k.emitLocked(scheduler.KindReap, p.ID, scheduler.ReapDetail(p.Program))
	return nil
}

func (k *Kernel) Begin(parent proc.PID, name string, args []string, dir string, env map[string]string, streams fd.Streams) (proc.Proc, error) {
	resolved, err := k.resolve(name)
	if err != nil {
		return proc.Proc{}, err
	}
	if len(resolved.Args) > 0 {
		args = append(slices.Clone(resolved.Args), args...)
	}
	if parent == 0 {
		parent = k.init
	}

	requested := k.budget(proc.Proc{Parent: parent, Program: resolved.Name})
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failed {
		return proc.Proc{}, errno.Busy
	}
	if k.closed {
		return proc.Proc{}, errno.Cancelled
	}
	p, err := k.table.Create(parent, resolved.Name, args, dir, env)
	if err != nil {
		return proc.Proc{}, err
	}
	if err := k.credentials.Inherit(uint64(parent), uint64(p.ID)); err != nil {
		return proc.Proc{}, err
	}
	if err := k.table.Start(p.ID); err != nil {
		return proc.Proc{}, err
	}
	if k.meters != nil {

		if err := k.meters.Spawn(uint64(p.ID), requested); err != nil {
			_ = k.table.Exit(p.ID, 125, 0)
			_, _ = k.table.Reap(parent, p.ID)
			return proc.Proc{}, err
		}

		if err := k.meters.Charge(context.Background(), uint64(p.ID), spawnWorkUnits); err != nil {
			_ = k.table.Exit(p.ID, 125, 0)
			_, _ = k.table.Reap(parent, p.ID)
			return proc.Proc{}, err
		}

		const standardDescriptors = 3
		if err := k.meters.ChargeKind(context.Background(), uint64(p.ID), resources.KindDescriptors, standardDescriptors); err != nil {
			_ = k.table.Exit(p.ID, 125, 0)
			_, _ = k.table.Reap(parent, p.ID)
			return proc.Proc{}, err
		}
	}
	k.signals.Spawn(uint64(p.ID))
	p.State = proc.Running
	p.Resources = capability.Scope(uint64(p.ID), k.services.Set, k.meters)
	p.Control = procControl{k: k, self: p.ID}
	if k.meters != nil {
		// T03:标准输出/错误按字节计入 Output 预算;超限由 write
		// 如实返回 EAGAIN,指标与配额扣减同源。
		if streams.Output != nil {
			streams.Output = meteredStream{inner: streams.Output, meters: k.meters, pid: uint64(p.ID)}
		}
		if streams.Error != nil {
			streams.Error = meteredStream{inner: streams.Error, meters: k.meters, pid: uint64(p.ID)}
		}
	}
	if k.fds != nil {
		k.fds.Attach(uint64(p.ID), streams)
	}
	k.emitLocked(scheduler.KindSpawn, p.ID, scheduler.SpawnDetail(resolved.Name))
	if k.failed {

		return proc.Proc{}, errno.Busy
	}
	return p, nil
}

// Fork 以 Linux fork/clone(SIGCHLD) 语义复制调用进程:进程表条目
// (含程序、参数、目录、环境、umask)、描述符表(共享底层句柄)、
// 信号处置与屏蔽(挂起集除外)与地址空间(E10 克隆,违例上报按子进程
// 重新装配)。返回子进程与子地址空间;执行续接由调用方完成。
func (k *Kernel) Fork(parent proc.PID) (proc.Proc, *memory.Table, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failed {
		return proc.Proc{}, nil, errno.Busy
	}
	if k.closed {
		return proc.Proc{}, nil, errno.Cancelled
	}
	source, err := k.table.Get(parent)
	if err != nil {
		return proc.Proc{}, nil, err
	}
	requested := k.budget(proc.Proc{Parent: source.Parent, Program: source.Program})
	// fork 的子进程父是调用进程本身(Linux 语义)。
	child, err := k.table.Create(parent, source.Program, source.Args, source.Dir, source.Env)
	if err != nil {
		return proc.Proc{}, nil, err
	}
	if err := k.credentials.Inherit(uint64(parent), uint64(child.ID)); err != nil {
		return proc.Proc{}, nil, err
	}
	if err := k.table.Start(child.ID); err != nil {
		return proc.Proc{}, nil, err
	}
	_ = k.table.SetUmask(child.ID, source.Umask)
	if k.meters != nil {
		if err := k.meters.Spawn(uint64(child.ID), requested); err != nil {
			_ = k.table.Exit(child.ID, 125, 0)
			_, _ = k.table.Reap(parent, child.ID)
			return proc.Proc{}, nil, err
		}
		if err := k.meters.Charge(context.Background(), uint64(child.ID), spawnWorkUnits); err != nil {
			_ = k.table.Exit(child.ID, 125, 0)
			_, _ = k.table.Reap(parent, child.ID)
			return proc.Proc{}, nil, err
		}
	}
	if err := k.signals.Fork(uint64(parent), uint64(child.ID)); err != nil {
		return proc.Proc{}, nil, err
	}
	// 描述符:fork 不重挂标准流,直接共享复制父进程全部条目。
	if k.fds != nil {
		count, err := k.fds.Fork(uint64(parent), uint64(child.ID))
		if err != nil {
			return proc.Proc{}, nil, err
		}
		if k.meters != nil && count > 0 {
			if err := k.meters.ChargeKind(context.Background(), uint64(child.ID), resources.KindDescriptors, uint64(count)); err != nil {
				_ = k.table.Exit(child.ID, 125, 0)
				_, _ = k.table.Reap(parent, child.ID)
				return proc.Proc{}, nil, err
			}
		}
	}
	// 地址空间:E10 克隆;无绑定地址空间时明确失败。
	bound, ok := k.spaces[parent]
	if !ok {
		return proc.Proc{}, nil, errno.Wrap(errno.Invalid, "进程未绑定地址空间,不能 fork")
	}
	guard, ok := bound.(*memory.Guard)
	if !ok {
		return proc.Proc{}, nil, errno.Wrap(errno.Invalid, "进程地址空间不支持 fork 克隆")
	}
	pid := child.ID
	cloned, err := guard.Clone(func(ctx context.Context, violation memory.Violation) {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.emitAddressViolationLocked(pid, violation)
	})
	if err != nil {
		return proc.Proc{}, nil, err
	}
	// E15:fork 为子进程的私有页副本计费(物理页口径——文件共享
	// 映射直通后备不落页表,不重复扣费);预算不足 fork 失败且
	// 子进程回收。
	if private := cloned.PrivateBytes(); k.meters != nil && private > 0 {
		if err := k.meters.ChargeKind(context.Background(), uint64(child.ID), resources.KindMemory, private); err != nil {
			delete(k.spaces, child.ID)
			_ = k.table.Exit(child.ID, 125, 0)
			_, _ = k.table.Reap(parent, child.ID)
			return proc.Proc{}, nil, err
		}
	}
	k.spaces[child.ID] = cloned
	child.State = proc.Running
	child.Resources = capability.Scope(uint64(child.ID), k.services.Set, k.meters)
	child.Control = procControl{k: k, self: child.ID}
	k.emitLocked(scheduler.KindSpawn, child.ID, scheduler.SpawnDetail(source.Program))
	return child, cloned.Inner(), nil
}

// Vfork 以 Linux vfork(clone(CLONE_VM|CLONE_VFORK|SIGCHLD)) 语义创建
// 子进程(H12):子进程是独立进程(新 PID,描述符表与信号处置按 fork
// 复制),地址空间与父进程共享——不克隆、不重复计页费,计费归属
// 持有空间的父进程。父进程的暂停与子进程的执行续接由调用方完成。
func (k *Kernel) Vfork(parent proc.PID) (proc.Proc, *memory.Table, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failed {
		return proc.Proc{}, nil, errno.Busy
	}
	if k.closed {
		return proc.Proc{}, nil, errno.Cancelled
	}
	source, err := k.table.Get(parent)
	if err != nil {
		return proc.Proc{}, nil, err
	}
	requested := k.budget(proc.Proc{Parent: source.Parent, Program: source.Program})
	// vfork 的子进程父是调用进程本身(Linux 语义)。
	child, err := k.table.Create(parent, source.Program, source.Args, source.Dir, source.Env)
	if err != nil {
		return proc.Proc{}, nil, err
	}
	if err := k.credentials.Inherit(uint64(parent), uint64(child.ID)); err != nil {
		return proc.Proc{}, nil, err
	}
	if err := k.table.Start(child.ID); err != nil {
		_ = k.table.Exit(child.ID, 125, 0)
		_, _ = k.table.Reap(parent, child.ID)
		return proc.Proc{}, nil, err
	}
	_ = k.table.SetUmask(child.ID, source.Umask)
	if k.meters != nil {
		if err := k.meters.Spawn(uint64(child.ID), requested); err != nil {
			_ = k.table.Exit(child.ID, 125, 0)
			_, _ = k.table.Reap(parent, child.ID)
			return proc.Proc{}, nil, err
		}
		if err := k.meters.Charge(context.Background(), uint64(child.ID), spawnWorkUnits); err != nil {
			k.meters.Discard(uint64(child.ID))
			_ = k.table.Exit(child.ID, 125, 0)
			_, _ = k.table.Reap(parent, child.ID)
			return proc.Proc{}, nil, err
		}
	}
	if err := k.signals.Fork(uint64(parent), uint64(child.ID)); err != nil {
		return proc.Proc{}, nil, err
	}
	// 描述符:CLONE_VM 不含 CLONE_FILES,描述符表按 fork 复制
	// (打开文件描述仍共享)。
	if k.fds != nil {
		count, err := k.fds.Fork(uint64(parent), uint64(child.ID))
		if err != nil {
			return proc.Proc{}, nil, err
		}
		if k.meters != nil && count > 0 {
			if err := k.meters.ChargeKind(context.Background(), uint64(child.ID), resources.KindDescriptors, uint64(count)); err != nil {
				_ = k.table.Exit(child.ID, 125, 0)
				_, _ = k.table.Reap(parent, child.ID)
				return proc.Proc{}, nil, err
			}
		}
	}
	// 地址空间:CLONE_VM 共享,不克隆(E15 页费归属持有者)。
	bound, ok := k.spaces[parent]
	if !ok {
		return proc.Proc{}, nil, errno.Wrap(errno.Invalid, "进程未绑定地址空间,不能 vfork")
	}
	if _, guarded := bound.(*memory.Guard); !guarded {
		return proc.Proc{}, nil, errno.Wrap(errno.Invalid, "进程地址空间不支持 vfork 共享")
	}
	k.spaces[child.ID] = bound
	child.State = proc.Running
	child.Resources = capability.Scope(uint64(child.ID), k.services.Set, k.meters)
	child.Control = procControl{k: k, self: child.ID}
	k.emitLocked(scheduler.KindSpawn, child.ID, scheduler.SpawnDetail(source.Program))
	return child, bound.(*memory.Guard).Inner(), nil
}

// PrepareSpace 创建全新空表并暂存为调用进程的待提交绑定,返回新表
// (H12)。上限沿用进程当前绑定的表;违例上报在 Commit 时按进程装配。
func (k *Kernel) PrepareSpace(pid proc.PID) (*memory.Table, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failed {
		return nil, errno.Busy
	}
	if k.closed {
		return nil, errno.Cancelled
	}
	if _, err := k.table.Get(pid); err != nil {
		return nil, err
	}
	bound, ok := k.spaces[pid]
	if !ok {
		return nil, errno.Wrap(errno.Invalid, "进程未绑定地址空间")
	}
	limit := uint64(0)
	if guard, guarded := bound.(*memory.Guard); guarded {
		limit = guard.Inner().Limit()
	}
	if k.prepared == nil {
		k.prepared = map[proc.PID]*memory.Table{}
		k.notifies = inotify.NewBroker()
	}
	fresh := memory.New(limit)
	k.prepared[pid] = fresh
	return fresh, nil
}

// CommitSpace 把待提交的表接管为进程当前空间(守卫的违例上报按本
// 进程装配,H12)。无准备时报 EINVAL。
func (k *Kernel) CommitSpace(pid proc.PID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	fresh, ok := k.prepared[pid]
	if !ok {
		return errno.Invalid
	}
	delete(k.prepared, pid)
	k.spaces[pid] = memory.NewGuard(fresh, func(ctx context.Context, violation memory.Violation) {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.emitAddressViolationLocked(pid, violation)
	})
	return nil
}

// DiscardSpace 丢弃待提交的准备(H12);无准备时为空操作。
func (k *Kernel) DiscardSpace(pid proc.PID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.prepared, pid)
}

// CloneThread 创建同组线程(K01/K02):登记线程条目并共享地址空间、
// 描述符集合与信号处置;不克隆地址空间、不重复计页费(共享映射的
// 计费归属组长;线程栈是客体 mmap,已在映射时计费,E15 备注)。
// 线程计入进程数量上限(O02)。clearTID 登记 CLONE_CHILD_CLEARTID
// (K03):线程退出时写零并唤醒 futex 等待者。
func (k *Kernel) CloneThread(parent proc.PID, clearTID uint64) (proc.Proc, *memory.Table, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failed {
		return proc.Proc{}, nil, errno.Busy
	}
	if k.closed {
		return proc.Proc{}, nil, errno.Cancelled
	}
	source, err := k.table.Get(parent)
	if err != nil {
		return proc.Proc{}, nil, err
	}
	requested := k.budget(proc.Proc{Parent: source.Parent, Program: source.Program})
	child, err := k.table.CreateThread(parent, source.Program)
	if err != nil {
		return proc.Proc{}, nil, err
	}
	if err := k.credentials.Inherit(uint64(parent), uint64(child.ID)); err != nil {
		return proc.Proc{}, nil, err
	}
	if err := k.table.Start(child.ID); err != nil {
		_, _ = k.table.ExitThread(child.ID, 125, 0)
		return proc.Proc{}, nil, err
	}
	if k.meters != nil {
		if err := k.meters.Spawn(uint64(child.ID), requested); err != nil {
			_, _ = k.table.ExitThread(child.ID, 125, 0)
			return proc.Proc{}, nil, err
		}
		if err := k.meters.Charge(context.Background(), uint64(child.ID), spawnWorkUnits); err != nil {
			k.meters.Discard(uint64(child.ID))
			_, _ = k.table.ExitThread(child.ID, 125, 0)
			return proc.Proc{}, nil, err
		}
	}
	// CLONE_FILES:共享打开描述符集合;任一侧关闭对双方可见。
	if k.fds != nil {
		if err := k.fds.Share(uint64(parent), uint64(child.ID)); err != nil {
			if k.meters != nil {
				k.meters.Discard(uint64(child.ID))
			}
			_, _ = k.table.ExitThread(child.ID, 125, 0)
			return proc.Proc{}, nil, err
		}
	}
	// CLONE_SIGHAND:共享信号处置集;屏蔽集继承当前值(线程私有)。
	if err := k.signals.Share(uint64(parent), uint64(child.ID)); err != nil {
		if k.fds != nil {
			k.fds.DropAlias(uint64(child.ID))
		}
		if k.meters != nil {
			k.meters.Discard(uint64(child.ID))
		}
		_, _ = k.table.ExitThread(child.ID, 125, 0)
		return proc.Proc{}, nil, err
	}
	bound, ok := k.spaces[parent]
	if !ok {
		k.fds.DropAlias(uint64(child.ID))
		k.signals.Discard(uint64(child.ID))
		if k.meters != nil {
			k.meters.Discard(uint64(child.ID))
		}
		_, _ = k.table.ExitThread(child.ID, 125, 0)
		return proc.Proc{}, nil, errno.Wrap(errno.Invalid, "进程未绑定地址空间,不能创建线程")
	}
	guard, guarded := bound.(*memory.Guard)
	if !guarded {
		k.fds.DropAlias(uint64(child.ID))
		k.signals.Discard(uint64(child.ID))
		if k.meters != nil {
			k.meters.Discard(uint64(child.ID))
		}
		_, _ = k.table.ExitThread(child.ID, 125, 0)
		return proc.Proc{}, nil, errno.Wrap(errno.Invalid, "进程地址空间不支持线程共享")
	}
	k.spaces[child.ID] = bound
	if clearTID != 0 {
		k.tidAddresses[child.ID] = clearTID
	}
	child.State = proc.Running
	child.Resources = capability.Scope(uint64(child.ID), k.services.Set, k.meters)
	child.Control = procControl{k: k, self: child.ID}
	k.emitLocked(scheduler.KindSpawn, child.ID, scheduler.SpawnDetail(source.Program))
	return child, guard.Inner(), nil
}

// FinishThread 结束单个线程(K02):非组长直接清算(线程无僵尸,
// join 依赖 clear_child_tid 唤醒);组长保留占位直到组内最后一个
// 线程退出。signal 非 0 的致命信号按 Linux 语义升级为整组结束(K08)。
// 返回组完成与否:组完成时调用方通知父进程状态变化。
func (k *Kernel) FinishThread(id proc.PID, code uint8, sig uint8) error {
	k.mu.Lock()
	if k.closed || k.failed {
		// 内核终态下线程随内核消亡,不再改动进程表。
		k.mu.Unlock()
		return errno.Cancelled
	}
	// 致命信号杀死整组(K08):线程定向的默认终止信号不遗留给
	// 无关线程。
	if sig != 0 {
		groupDone, err := k.finishGroupLocked(id, code, sig)
		k.mu.Unlock()
		if err == nil && groupDone {
			k.childStateChanged(id, true)
			k.children.WakeAll(wait.Data)
		}
		return err
	}
	groupDone, err := k.table.ExitThread(id, code, 0)
	if err != nil {
		k.mu.Unlock()
		return err
	}
	// 线程私有清算:描述符别名、信号状态、计量与地址空间绑定。
	if k.fds != nil {
		k.fds.DropAlias(uint64(id))
	}
	k.signals.Discard(uint64(id))
	k.deleteTidAddressLocked(id)
	if k.meters != nil {
		k.meters.Discard(uint64(id))
	}
	delete(k.spaces, id)
	delete(k.prepared, id)
	if !groupDone {
		k.mu.Unlock()
		return nil
	}
	k.emitLocked(scheduler.KindExit, id, scheduler.ExitDetail(code, signal.Number(0)))
	reaped, err := k.table.Get(id)
	k.mu.Unlock()
	if err != nil {
		return err
	}
	k.childStateChanged(id, true)
	k.children.WakeAll(wait.Data)
	_ = reaped
	return nil
}

// FinishGroup 结束整个线程组(exit_group 语义,H08):全组以同一
// 状态结束,组长承载退出状态转僵尸并通知父进程。
func (k *Kernel) FinishGroup(id proc.PID, code uint8, sig uint8) error {
	k.mu.Lock()
	groupDone, err := k.finishGroupLocked(id, code, sig)
	k.mu.Unlock()
	if err == nil && groupDone {
		k.childStateChanged(id, true)
		k.children.WakeAll(wait.Data)
	}
	return err
}

// finishGroupLocked 是 FinishGroup 的锁内实现:逐成员清算线程私有
// 资源并中断残留执行,组长条目承载退出状态。
func (k *Kernel) finishGroupLocked(id proc.PID, code uint8, sig uint8) (bool, error) {
	if k.failed {
		return false, errno.Busy
	}
	if k.closed {
		return false, errno.Cancelled
	}
	p, err := k.table.Get(id)
	if err != nil {
		return false, err
	}
	for _, member := range k.table.Threads(p.TGID) {
		if member.ID == p.TGID {
			continue
		}
		if k.fds != nil {
			k.fds.DropAlias(uint64(member.ID))
		}
		k.signals.Discard(uint64(member.ID))
		k.deleteTidAddressLocked(member.ID)
		if k.meters != nil {
			k.meters.Discard(uint64(member.ID))
		}
		delete(k.spaces, member.ID)
		delete(k.prepared, member.ID)
		// 残留执行流:以致命信号中断,运行器返回后其 FinishThread
		// 发现条目已消失而自然结束。
		k.signals.Interrupt(uint64(member.ID), signal.KILL)
	}
	// 信号终止的组退出状态与 Finish 约定一致(128+信号),使组长
	// 实例结算与表内终态可通过 Finish 的一致性校验。
	if sig != 0 {
		code = signal.ExitCode(signal.Number(sig))
	}
	if err := k.table.Exit(p.TGID, code, sig); err != nil {
		return false, err
	}
	// 组长的执行实例同样中断(exit_group 的正常退出无需打断;致命
	// 信号以该信号中断,实例结算出与组一致的信号终止状态)。
	if sig != 0 {
		k.signals.Interrupt(uint64(p.TGID), signal.Number(sig))
	}
	k.emitLocked(scheduler.KindExit, p.TGID, scheduler.ExitDetail(code, signal.Number(sig)))
	return true, nil
}

// Tgkill 按 tgkill(2) 语义投递线程定向信号(K08):tid 必须属于
// tgid 的线程组(单线程进程 tid==tgid),否则 ESRCH。
func (k *Kernel) Tgkill(tgid, tid proc.PID, n signal.Number) error {
	if !n.Valid() {
		return errno.Invalid
	}
	k.mu.Lock()
	member, err := k.table.Get(tid)
	if err != nil {
		k.mu.Unlock()
		return errno.NoProcess
	}
	if member.TGID != tgid {
		k.mu.Unlock()
		return errno.NoProcess
	}
	k.mu.Unlock()
	return k.Signal(tid, n)
}

// deleteTidAddressLocked 清除线程登记的 clear_child_tid 地址。
func (k *Kernel) deleteTidAddressLocked(id proc.PID) {
	delete(k.tidAddresses, id)
}

func (k *Kernel) resolve(name string) (executable.Resolved, error) {
	if program, err := k.services.Programs.Lookup(name); err == nil {

		if _, checkErr := capability.Require(program); checkErr != nil {
			return executable.Resolved{}, errno.Invalid
		}
		return executable.Resolved{Name: name, ABI: executable.ABIBuiltin, Program: program}, nil
	}
	if k.resolver == nil {
		return executable.Resolved{}, fmt.Errorf("程序%s: %w", name, errno.NotFound)
	}
	return k.resolver.Resolve(context.Background(), name)
}

func (k *Kernel) Finish(parent, id proc.PID, status ExitStatus) (err error) {
	k.mu.Lock()
	notified := false
	defer func() {
		k.mu.Unlock()
		// 通知在锁外:childStateChanged 内部重新加锁,持锁调用会死锁。
		if notified {
			k.childStateChanged(id, true)
		}
		if err == nil {
			k.children.WakeAll(wait.Data)
		}
	}()
	if k.failed {
		return errno.Busy
	}
	if parent == 0 {
		parent = k.init
	}
	p, err := k.table.Get(id)
	if err != nil {
		return err
	}
	if p.Parent != parent {
		return errno.NoChild
	}
	if len(k.table.Children(id)) > 0 {
		return errno.Busy
	}
	switch p.State {
	case proc.Running:
		if err := k.table.Exit(id, status.Code, uint8(status.Signal)); err != nil {
			return err
		}
	case proc.Zombie:
		if status.Code != p.ExitCode || status.Signal != signal.Number(p.ExitSignal) {
			return errno.Busy
		}
	}
	// H10:fork 子进程转僵尸后由父进程 wait* 回收,Finish 不代劳——
	// 否则父进程的 waitpid 永远 ECHILD(K10 探针暴露)。僵尸的清理
	// 路径:父进程 wait、SIG_IGN 自动回收(childStateChanged)、
	// 父进程退出后的 init 收养(H10)。
	final, getErr := k.table.Get(id)
	var closeErr error
	if getErr == nil {
		closeErr = k.exitResourcesLocked(final)
	} else {
		closeErr = getErr
	}
	k.emitLocked(scheduler.KindExit, id, scheduler.ExitDetail(status.Code, status.Signal))
	if closeErr == nil {
		notified = true
	}
	return closeErr
}

func (k *Kernel) Wait(ctx context.Context, parent, target proc.PID) (proc.Proc, error) {
	if parent == 0 {
		parent = k.init
	}
	for {
		k.mu.Lock()
		if k.closed {
			k.mu.Unlock()
			return proc.Proc{}, errno.Cancelled
		}
		if target != 0 {
			p, err := k.table.Get(target)
			if err != nil {
				k.mu.Unlock()
				return proc.Proc{}, errno.NoChild
			}
			if p.Parent != parent {
				k.mu.Unlock()
				return proc.Proc{}, errno.NoChild
			}
			if p.State == proc.Zombie {
				reaped, err := k.table.Reap(parent, target)
				if err != nil {
					k.mu.Unlock()
					return proc.Proc{}, err
				}
				closeErr := k.afterReapLocked(reaped)
				k.mu.Unlock()
				k.children.WakeAll(wait.Data)
				return reaped, closeErr
			}
		} else {
			if p, ok := k.table.WaitAny(parent); ok {
				reaped, err := k.table.Reap(parent, p.ID)
				if err != nil {
					k.mu.Unlock()
					return proc.Proc{}, err
				}
				closeErr := k.afterReapLocked(reaped)
				k.mu.Unlock()
				k.children.WakeAll(wait.Data)
				return reaped, closeErr
			}
			if !k.table.HasChildren(parent) {
				k.mu.Unlock()
				return proc.Proc{}, errno.NoChild
			}
		}
		// 阻塞前同样检查待投递信号(唤醒路径见条件闭包)。
		if _, _, ok := k.signals.DeliverableHandler(uint64(parent)); ok {
			k.mu.Unlock()
			return proc.Proc{}, errno.Interrupted
		}
		k.mu.Unlock()
		// H01:等待子进程期间进程处于等待状态,唤醒后恢复运行。
		k.setWaiting(parent)
		reason, err := k.children.Wait(ctx, func() (bool, wait.Reason, error) {
			k.mu.Lock()
			defer k.mu.Unlock()
			if k.closed {
				return true, wait.Shutdown, nil
			}
			// J09:登记了处理器的待投递信号打断等待(wait* 无论
			// SA_RESTART 一律 EINTR);处理器在返回后的指令边界投递。
			if _, _, ok := k.signals.DeliverableHandler(uint64(parent)); ok {
				return false, wait.Signal, errno.Interrupted
			}
			if target != 0 {
				p, err := k.table.Get(target)
				if err != nil {
					// 目标进程已被回收(例如父进程以 SIG_IGN 忽略
					// SIGCHLD 时自动回收):等待以 ECHILD 结束,
					// 不再无限等待一个已消失的进程。
					return true, wait.Data, errno.NoChild
				}
				// 组长占位僵尸(组内仍有存活线程)不唤醒等待(K02)。
				return p.Parent == parent && p.State == proc.Zombie && k.table.Reapable(target), wait.Data, nil
			}
			_, ok := k.table.WaitAny(parent)
			return ok, wait.Data, nil
		})
		k.setRunnable(parent)
		if err != nil {
			if reason == wait.Shutdown {
				return proc.Proc{}, errno.Cancelled
			}
			return proc.Proc{}, err
		}

	}
}

// setWaiting 把进程标记为阻塞等待状态(H01):进程已退出或已驻停时
// 保持原状态。
func (k *Kernel) setWaiting(id proc.PID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	_ = k.table.Transition(id, proc.Waiting)
}

// setRunnable 把阻塞等待的进程恢复为运行状态(H01):驻停或已退出
// 的进程保持原状态,不被唤醒路径误恢复。
func (k *Kernel) setRunnable(id proc.PID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	p, err := k.table.Get(id)
	if err != nil || p.State != proc.Waiting {
		return
	}
	_ = k.table.Transition(id, proc.Running)
}

// WaitChild 按 wait4/waitid 语义等待或探测子进程:
// target=0 表示任意子进程;nohang 时不阻塞,found=false 表示尚无
// 可回收的子进程。返回的状态携带退出码与终止信号。
func (k *Kernel) WaitChild(ctx context.Context, parent, target proc.PID, nohang bool) (proc.ChildState, bool, error) {
	if parent == 0 {
		parent = k.init
	}
	if nohang {
		k.mu.Lock()
		if k.closed {
			k.mu.Unlock()
			return proc.ChildState{}, false, errno.Cancelled
		}
		var found proc.PID
		if target != 0 {
			p, err := k.table.Get(target)
			if err != nil || p.Parent != parent {
				k.mu.Unlock()
				return proc.ChildState{}, false, errno.NoChild
			}
			// 组长占位僵尸(组内仍有存活线程)不可回收(K02)。
			if p.State != proc.Zombie || !k.table.Reapable(target) {
				k.mu.Unlock()
				return proc.ChildState{}, false, nil
			}
			found = target
		} else {
			p, ok := k.table.WaitAny(parent)
			if !ok {
				if !k.table.HasChildren(parent) {
					k.mu.Unlock()
					return proc.ChildState{}, false, errno.NoChild
				}
				k.mu.Unlock()
				return proc.ChildState{}, false, nil
			}
			found = p.ID
		}
		reaped, err := k.table.Reap(parent, found)
		if err != nil {
			k.mu.Unlock()
			return proc.ChildState{}, false, err
		}
		closeErr := k.afterReapLocked(reaped)
		k.mu.Unlock()
		k.children.WakeAll(wait.Data)
		state := proc.ChildState{PID: reaped.ID, ExitCode: reaped.ExitCode, ExitSignal: reaped.ExitSignal}
		return state, true, closeErr
	}
	reaped, err := k.Wait(ctx, parent, target)
	if err != nil {
		return proc.ChildState{}, true, err
	}
	state := proc.ChildState{PID: reaped.ID, ExitCode: reaped.ExitCode, ExitSignal: reaped.ExitSignal}
	return state, true, nil
}

// HasChildren 报告进程是否还有子进程。
func (k *Kernel) HasChildren(parent proc.PID) bool {
	if parent == 0 {
		parent = k.init
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return false
	}
	return k.table.HasChildren(parent)
}

func (k *Kernel) Signal(target proc.PID, n signal.Number) error {
	if !n.Valid() {
		return errno.Invalid
	}
	action, err := signal.Default(n)
	if err != nil {
		return err
	}
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return errno.Cancelled
	}
	p, err := k.table.Get(target)
	if err != nil {
		k.mu.Unlock()
		return err
	}
	if p.State == proc.Zombie && k.table.ThreadCount(p.TGID) > 1 {
		// 占位组长(组内仍有存活线程,K02):信号投递给首个存活成员。
		for _, member := range k.table.Threads(p.TGID) {
			if member.State != proc.Zombie {
				target = member.ID
				p = member
				break
			}
		}
	}
	if p.State == proc.Zombie {
		// 对僵尸的 kill 为无操作(Linux:信号不投递、不报错)。
		k.mu.Unlock()
		return nil
	}
	zombie := p.State == proc.Zombie
	replaying := k.replayingLocked()
	k.mu.Unlock()

	if journal := k.journalLocked(); journal != nil {
		if _, err := replay.RecordSchedule(context.Background(), journal, uint64(target),

			replay.ScheduleFact{Kind: replay.ScheduleSignal, Detail: uint64(n)},
			func() (uint64, bool, error) { return uint64(n), zombie, nil }); err != nil {
			return err
		}
		if replaying {

			return nil
		}
	}

	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return errno.Cancelled
	}

	ignored := k.signals.Ignored(uint64(target), n)
	if !ignored && !signal.Unmaskable(n) {
		if config, err := k.signals.Action(uint64(target), n); err == nil && config.Handler == signal.SigIgnore {
			ignored = true
		}
	}
	if !ignored {
		if err := k.signals.Raise(uint64(target), n); err != nil {
			k.mu.Unlock()
			return err
		}
		k.emitLocked(scheduler.KindSignal, target, scheduler.SignalDetail(n))
	}
	k.mu.Unlock()
	if ignored {
		return nil
	}
	if zombie {

		return nil
	}
	// 被屏蔽且命中 signalfd 掩码的信号转入 fd 队列,不再进入
	// 处理器与默认处置路径(L11);读取 fd 即消费该信号。
	if k.routeSignalfd(target, n) {
		return nil
	}
	// 登记了客户态处理器的信号保持挂起,由执行器在指令边界
	// 经信号帧投递(J03/J04);默认处置仍走终止/忽略路径。
	// 唤醒阻塞等待(wait* 等)重新检查条件与信号(J09);此处
	// 已不持有 k.mu,WakeAll 可直接调用。
	if config, err := k.signals.Action(uint64(target), n); err == nil && config.Handler > signal.SigIgnore && !signal.Unmaskable(n) {
		k.children.WakeAll(wait.Data)
		return nil
	}
	switch {
	case n == signal.CONT:
		// 恢复运行(CONT 的默认处置即继续;运行中进程视为无操作);
		// 丢弃挂起的停止族信号。
		if err := k.table.Continue(target); err != nil {
			return err
		}
		for _, stopped := range []signal.Number{signal.STOP, signal.TSTP, signal.TTIN, signal.TTOU} {
			k.signals.Consume(uint64(target), stopped)
		}
		k.signals.Interrupt(uint64(target), n)
		k.childStateChanged(target, false)
		return nil
	case action == signal.Terminate, action == signal.Core:
		// K08:线程组内任意线程的致命信号结束整组(Linux 语义)。
		k.mu.Lock()
		grouped := k.table.ThreadCount(p.TGID) > 1
		var groupErr error
		if grouped {
			_, groupErr = k.finishGroupLocked(target, 0, uint8(n))
		}
		k.mu.Unlock()
		if grouped {
			return groupErr
		}
		k.signals.Interrupt(uint64(target), n)
		return nil
	case action == signal.Stop:
		// 驻停执行(运行器在指令边界暂停)并记录进程状态;
		// 丢弃挂起的继续信号(Linux 语义)。线程组驻停整组(K08)。
		k.mu.Lock()
		members := k.table.Threads(p.TGID)
		grouped := len(members) > 1
		if grouped {
			for _, member := range members {
				if member.State == proc.Running || member.State == proc.Waiting {
					_ = k.table.Stop(member.ID)
				}
				k.signals.Consume(uint64(member.ID), signal.CONT)
				k.signals.Interrupt(uint64(member.ID), n)
			}
		}
		k.mu.Unlock()
		if grouped {
			k.childStateChanged(target, false)
			return nil
		}
		if err := k.table.Stop(target); err != nil {
			return err
		}
		k.signals.Consume(uint64(target), signal.CONT)
		k.signals.Interrupt(uint64(target), n)
		k.childStateChanged(target, false)
		return nil
	case action == signal.Ignore:
		return nil
	default:
		return errno.NotSupported
	}
}

// routeSignalfd 把被目标进程屏蔽且命中其 signalfd 掩码的信号转入
// fd 队列并从挂起集中消费(L11):读取 fd 即取得 siginfo,该信号不再
// 进入处理器与默认处置路径。ssi_pid/ssi_uid 记为 0(内核来源;
// 当前投递路径不携带发送者身份)。调用方不得持有 k.mu——Deliver
// 会唤醒等待者并触发就绪回调。
func (k *Kernel) routeSignalfd(target proc.PID, n signal.Number) bool {
	if n == 0 || n > 64 {
		return false
	}
	masked, err := k.signals.Mask(uint64(target))
	if err != nil || masked&(uint64(1)<<uint(n-1)) == 0 {
		return false
	}
	routed := false
	for _, f := range k.fds.SignalFDs(uint64(target)) {
		if !f.Masked(uint32(n)) {
			continue
		}
		if err := f.Deliver(signalfd.Signal{Signo: uint32(n), Code: signalfd.CodeUser}); err == nil {
			routed = true
		}
	}
	if routed {
		k.signals.Consume(uint64(target), n)
	}
	return routed
}

// SignalAll 向除 except 外的全部进程发送信号(kill(-1) 语义)。
func (k *Kernel) SignalAll(except proc.PID, n signal.Number) error {
	for _, p := range k.Processes() {
		if p.ID == except {
			continue
		}
		if err := k.Signal(p.ID, n); err != nil && !errors.Is(err, errno.NoProcess) {
			return err
		}
	}
	return nil
}

func (k *Kernel) SignalGroup(pgid proc.PID, n signal.Number) error {
	k.mu.Lock()
	closed := k.closed
	k.mu.Unlock()
	if closed {
		return errno.Cancelled
	}
	return job.SignalGroup(k.table, pgid, n, func(pid proc.PID, num signal.Number) error {
		return k.Signal(pid, num)
	})
}

func (k *Kernel) SetPGID(caller, target, pgid proc.PID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return errno.Cancelled
	}
	if caller == 0 {
		caller = k.init
	}
	return k.table.SetPGID(caller, target, pgid)
}

func (k *Kernel) SetSID(id proc.PID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return errno.Cancelled
	}
	return k.table.SetSID(id)
}

// GetPGID 返回目标进程的进程组号(H11);target=0 表示调用进程自身。
func (k *Kernel) GetPGID(caller, target proc.PID) (proc.PID, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return 0, errno.Cancelled
	}
	if target == 0 {
		target = caller
	}
	p, err := k.table.Get(target)
	if err != nil {
		return 0, err
	}
	return p.PGID, nil
}

// GetSID 返回目标进程的会话号(H11);target=0 表示调用进程自身。
func (k *Kernel) GetSID(caller, target proc.PID) (proc.PID, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return 0, errno.Cancelled
	}
	if target == 0 {
		target = caller
	}
	p, err := k.table.Get(target)
	if err != nil {
		return 0, err
	}
	return p.SID, nil
}

type BudgetFunc func(process proc.Proc) resources.Limits

func (k *Kernel) SetBudget(budget BudgetFunc) {
	k.budgetFunc = budget
}

func (k *Kernel) Control() []scheduler.Event {
	k.mu.Lock()
	sequencer := k.sequencer
	k.mu.Unlock()

	if sequencer == nil {
		return nil
	}
	return sequencer.Log()
}

func (k *Kernel) BindAddressSpace(pid proc.PID, inner *memory.Table) (memory.Space, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return nil, errno.Cancelled
	}
	if _, err := k.table.Get(pid); err != nil {
		return nil, err
	}
	if k.spaces == nil {
		k.spaces = map[proc.PID]memory.Space{}
	}
	guarded := memory.NewGuard(inner, func(ctx context.Context, violation memory.Violation) {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.emitAddressViolationLocked(pid, violation)
	})
	k.spaces[pid] = guarded
	return guarded, nil
}

type kernelSpaces struct{ kernel *Kernel }

// metersBudget 把资源计量器接到描述符表的预算钩子(O05)。
type metersBudget struct{ meters resources.Meter }

func (m metersBudget) ChargeDescriptors(pid uint64, count int) error {
	return m.meters.ChargeKind(context.Background(), pid, resources.KindDescriptors, uint64(count))
}

func (m metersBudget) ReleaseDescriptors(pid uint64, count int) {
	_ = m.meters.ReleaseKind(context.Background(), pid, resources.KindDescriptors, uint64(count))
}

func (s kernelSpaces) BindAddressSpace(pid uint64, inner *memory.Table) (memory.Space, error) {
	return s.kernel.BindAddressSpace(proc.PID(pid), inner)
}

func (k *Kernel) AddressSpace(pid proc.PID) (memory.Space, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return nil, errno.Cancelled
	}
	space, ok := k.spaces[pid]
	if !ok {
		return nil, errno.Wrap(errno.NotFound, "进程未绑定地址空间")
	}
	return space, nil
}

func (k *Kernel) emitAddressViolationLocked(pid proc.PID, violation memory.Violation) {
	k.emitLocked(scheduler.KindMemory, pid, memoryDetail(violation))
}

func utoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}

func memoryDetail(violation memory.Violation) string {
	return "fault=" + violation.Fault.String() +
		" start=" + utoa(violation.Start) +
		" end=" + utoa(violation.End) +
		" prot=" + violation.Requested.String()
}

func (k *Kernel) Pending(pid proc.PID) ([]signal.Number, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return nil, errno.Cancelled
	}
	if _, err := k.table.Get(pid); err != nil {
		return nil, err
	}
	return k.signals.Pending(uint64(pid))
}

func (k *Kernel) journalLocked() *replay.Memory { return k.services.Journal }

func (k *Kernel) replayingLocked() bool {
	if k.services.Journal == nil {
		return false
	}
	return !k.services.Journal.Recording()
}

func (k *Kernel) budget(process proc.Proc) resources.Limits {
	if k.meters == nil || k.budgetFunc == nil {
		return resources.Limits{}
	}
	return k.budgetFunc(process)
}

func (k *Kernel) InstallFD(pid proc.PID, id uint32, handle fd.Stream) error {
	if handle == nil {
		return errno.Invalid
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return errno.Cancelled
	}
	if k.fds == nil {
		return errno.NotImplemented
	}
	if _, err := k.table.Get(pid); err != nil {
		return err
	}

	// 替换已占用槽位:表内预算钩子按"释放旧槽+占用新槽"净额零。
	if k.fds.IsOpen(uint64(pid), id) {
		if err := k.fds.Close(context.Background(), uint64(pid), id); err != nil {
			return err
		}
	}
	return k.fds.Install(uint64(pid), id, handle)
}

func (k *Kernel) ReadFD(ctx context.Context, pid proc.PID, id uint32, buffer []byte) (int, error) {
	stream, err := k.streamFor(ctx, pid, id)
	if err != nil {
		return 0, err
	}
	return k.readViaJournal(ctx, pid, id, buffer, func() (int, error) { return stream.Read(buffer) })
}

func (k *Kernel) ReadContextFD(ctx context.Context, pid proc.PID, id uint32, buffer []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	stream, err := k.streamFor(ctx, pid, id)
	if err != nil {
		return 0, err
	}
	read := func() (int, error) {
		if blocking, ok := stream.(interface {
			ReadContext(context.Context, []byte) (int, error)
		}); ok {
			return blocking.ReadContext(ctx, buffer)
		}
		return stream.Read(buffer)
	}
	return k.readViaJournal(ctx, pid, id, buffer, read)
}

func (k *Kernel) WriteFD(ctx context.Context, pid proc.PID, id uint32, data []byte) (int, error) {
	stream, err := k.streamFor(ctx, pid, id)
	if err != nil {
		return 0, err
	}
	return stream.Write(data)
}

func (k *Kernel) streamFor(ctx context.Context, pid proc.PID, id uint32) (fd.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return nil, errno.Cancelled
	}
	fds := k.fds
	k.mu.Unlock()
	if fds == nil {
		return nil, errno.NotImplemented
	}
	return fds.Get(ctx, uint64(pid), id)
}

func (k *Kernel) CloseFD(pid proc.PID, id uint32) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return errno.Cancelled
	}
	if k.fds == nil {
		return errno.NotImplemented
	}
	if _, err := k.table.Get(pid); err != nil {
		return err
	}

	// 归还由表内预算钩子完成(O05:所有创建路径共享同一限额)。
	return k.fds.Close(context.Background(), uint64(pid), id)
}

func (k *Kernel) Pipe(owner proc.PID, capacity int) (uint32, uint32, error) {
	if k.fds == nil {
		return 0, 0, errno.NotImplemented
	}
	readEnd, writeEnd, err := pipe.Pair(capacity)
	if err != nil {
		return 0, 0, err
	}
	readFD, err := k.fds.InstallNext(uint64(owner), readEnd)
	if err != nil {
		return 0, 0, err
	}
	writeFD, err := k.fds.InstallNext(uint64(owner), writeEnd)
	if err != nil {
		_ = k.fds.Close(context.Background(), uint64(owner), readFD)
		return 0, 0, err
	}
	readEnd.OnBroken(func() {
		_ = k.Signal(owner, signal.PIPE)
	})
	return readFD, writeFD, nil
}

func (k *Kernel) RunRegistered(ctx context.Context, p proc.Proc) (ExitStatus, error) {
	// 启动前失败也必须把已登记进程终结为僵尸,否则 wait 永久挂起
	// (取消与启动竞争的回归由 O08 覆盖)。
	abort := func(err error) (ExitStatus, error) {
		_ = k.Finish(p.Parent, p.ID, ExitStatus{Code: 125})
		return ExitStatus{Code: 125}, err
	}
	if err := ctx.Err(); err != nil {
		return abort(err)
	}
	k.mu.Lock()
	actual, err := k.table.Get(p.ID)
	if err != nil {
		k.mu.Unlock()
		return ExitStatus{Code: 125}, err
	}
	if actual.State != proc.Running || actual.Program != p.Program {
		k.mu.Unlock()
		return ExitStatus{Code: 125}, errno.Invalid
	}
	pid := p.ID
	k.mu.Unlock()

	resolved, err := k.resolve(actual.Program)
	if err != nil {
		return abort(err)
	}

	actual.Resources = capability.Scope(uint64(actual.ID), k.services.Set, k.meters)
	actual.Control = procControl{k: k, self: actual.ID}
	runCtx, cancel := context.WithCancelCause(ctx)

	instance, err := executable.NewBuiltin().Start(proc.WithContext(runCtx, actual), resolved.Program, actual)
	if err != nil {
		cancel(nil)
		return abort(err)
	}
	k.mu.Lock()
	k.instances[pid] = instance
	k.signals.BindInterrupt(uint64(pid), func(n signal.Number) {
		_ = instance.Interrupt(n)
	})

	// 驻停观察(H02):被 SIGSTOP 驻停的进程归还执行权,恢复后
	// 重新排队——驻停进程不占用其他进程的执行时间片。
	if observer, ok := instance.(interface {
		ObserveParking(func(parked bool))
	}); ok {
		observer.ObserveParking(func(parked bool) {
			if parked {
				k.queue.leave()
				return
			}
			// 恢复后排队等待下一轮;内核关闭时立即失败放行
			//(父上下文随之取消,运行器经 ctx.Err() 退出)。
			_ = k.queue.enter(context.Background())
		})
	}

	if pending, ok := k.signals.Deliverable(uint64(pid)); ok {
		if action, err := signal.Default(pending); err == nil {
			switch action {
			case signal.Terminate, signal.Core:
				_ = instance.Interrupt(pending)
			}
		}
	}
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		delete(k.instances, pid)
		k.signals.BindInterrupt(uint64(pid), nil)
		k.mu.Unlock()
		cancel(nil)
	}()
	result, waitErr := instance.Wait(ctx)
	if ctx.Err() != nil {
		// 上下文被取消后仍等待客体程序真正结束:进程在程序退出前
		// 不算结束,避免遗留宿主 goroutine(C07)。无视取消的客体
		// 程序由关闭流程的截止时间兜底。
		if finished, finishErr := instance.Wait(context.WithoutCancel(ctx)); finishErr == nil {
			result = finished
			waitErr = finishErr
		}
	}
	status := ExitStatus{Code: result.Code}
	if result.Reason == executable.Signaled {
		status = ExitStatus{Code: signal.ExitCode(result.Signal), Signal: result.Signal}
	}
	// Q02:记录首个缺失指令/系统调用;缺失文件由 openat 端口记录。
	if result.Diagnostic != nil && strings.Contains(result.Diagnostic.Trap, "not-implemented") {
		k.recordGap(GapReport{Kind: "instruction", PID: uint64(pid), PC: result.Diagnostic.PC, Trap: result.Diagnostic.Trap, Repro: instructionGapRepro(result.Diagnostic.Trap, result.Diagnostic.PC)})
	}
	if k.trace != nil {
		for _, entry := range k.trace.Entries() {
			if entry.PID == uint64(pid) && (entry.Result == -95 || entry.Result == -38) {
				k.recordGap(GapReport{Kind: "syscall", PID: uint64(pid), Syscall: entry.Number, Repro: syscallGapRepro(entry.Number)})
				break
			}
		}
	}
	// T02:致命故障/崩溃信号在资源清算前抓取 CPU、映射与最近调用。
	if result.Reason == executable.Fault || (result.Reason == executable.Signaled && crashSignal(result.Signal)) {
		var recent []trace.Entry
		if k.trace != nil {
			for _, entry := range k.trace.Entries() {
				if entry.PID == uint64(pid) {
					recent = append(recent, entry)
				}
			}
			if len(recent) > 32 {
				recent = recent[len(recent)-32:]
			}
		}
		k.recordCrash(k.buildCrashReport(actual, result, recent))
	}
	var runErr error
	switch {
	case result.Reason == executable.Fault:

		k.mu.Lock()
		k.failed = true
		_ = k.life.enter(StateFailed)
		k.mu.Unlock()
		runErr = waitErr
	case result.Reason == executable.Quota:
		runErr = errno.Limit
	case waitErr != nil:
		runErr = waitErr
	default:
		runErr = instanceErr(result, waitErr)
	}
	k.mu.Lock()
	// 组致命信号路径(K08):finishGroupLocked 已把组长置为与实例
	// 结算一致的僵尸终态,此处 Exit 的 Busy 视为幂等成功。
	idempotent := func(err error, code uint8, sig uint8) bool {
		if !errors.Is(err, errno.Busy) {
			return false
		}
		p, getErr := k.table.Get(pid)
		return getErr == nil && p.State == proc.Zombie &&
			p.ExitCode == code && p.ExitSignal == sig
	}
	if ctxErr := ctx.Err(); ctxErr == nil {
		if err := k.table.Exit(pid, status.Code, uint8(status.Signal)); err == nil || idempotent(err, status.Code, uint8(status.Signal)) {
			if exited, getErr := k.table.Get(pid); getErr == nil {
				if cleanErr := k.exitResourcesLocked(exited); cleanErr != nil {
					k.mu.Unlock()
					return status, errors.Join(runErr, cleanErr)
				}
			}
			k.emitLocked(scheduler.KindExit, pid, scheduler.ExitDetail(status.Code, status.Signal))
			k.mu.Unlock()
			k.childStateChanged(pid, true)
			k.children.WakeAll(wait.Data)
			return status, runErr
		} else {
			k.mu.Unlock()
			return status, errors.Join(runErr, err)
		}
	}

	code := status.Code
	if status.Signal == 0 {
		code = 125
	}
	if err := k.table.Exit(pid, code, uint8(status.Signal)); err != nil && !idempotent(err, code, uint8(status.Signal)) {
		k.mu.Unlock()
		return status, errors.Join(runErr, err)
	}
	if exited, getErr := k.table.Get(pid); getErr == nil {
		if cleanErr := k.exitResourcesLocked(exited); cleanErr != nil {
			k.mu.Unlock()
			return status, errors.Join(runErr, cleanErr)
		}
	}
	k.emitLocked(scheduler.KindExit, pid, scheduler.ExitDetail(code, status.Signal))
	k.mu.Unlock()
	k.childStateChanged(pid, true)
	k.children.WakeAll(wait.Data)
	return status, errors.Join(runErr, ctx.Err())
}

// childStateChanged 在子进程退出或驻停/恢复后按 Linux 语义通知父
// 进程(J08):SIGCHLD 默认处置忽略——不产生可观察信号;显式
// SIG_IGN 的退出自动回收不留僵尸;登记处理器时经边界投递。通知与
// wait 竞争安全:回收以僵尸状态为准恰好一次,信号不承载回收责任。
// 必须在未持有 k.mu 时调用(内部会重新加锁)。
func (k *Kernel) childStateChanged(child proc.PID, exited bool) {
	k.mu.Lock()
	p, err := k.table.Get(child)
	if err != nil {
		k.mu.Unlock()
		return
	}
	parent := p.Parent
	k.mu.Unlock()
	if parent == 0 || parent == child {
		return
	}
	ignored := k.signals.Ignored(uint64(parent), signal.CHLD)
	config, err := k.signals.Action(uint64(parent), signal.CHLD)
	explicitIgnore := err == nil && config.Handler == signal.SigIgnore
	switch {
	case exited && (ignored || explicitIgnore):
		_, _, _ = k.WaitChild(context.Background(), parent, child, true)
	case err == nil && config.Handler > signal.SigIgnore:
		_ = k.Signal(parent, signal.CHLD)
	}
}

func instanceErr(result executable.Result, waitErr error) error {
	if result.Reason == executable.Signaled {
		return nil
	}
	return waitErr
}

func (k *Kernel) Close(ctx context.Context) error {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return nil
	}
	k.closed = true
	_ = k.life.enter(StateStopping)
	instances := make([]executable.Instance, 0, len(k.instances))
	for _, instance := range k.instances {
		instances = append(instances, instance)
	}
	// 回收句柄:每个进程绑定的描述符一并释放(C06:关闭后不残留句柄)。
	processes := k.table.List()
	k.emitLocked(scheduler.KindClose, k.init, "session")
	k.mu.Unlock()
	// 关闭可运行队列:排队等待执行权的进程以错误放行(H02)。
	k.queue.close()
	// 关闭 futex 等待表:阻塞的 WAIT 以 ECANCELED 放行(K05)。
	k.futexes.Close()
	// 关闭网络命名空间:阻塞的 socket 操作放行,监听拆除(M01/M12)。
	k.network.Close()
	if k.locks != nil {
		k.locks.Close()
	}
	for _, instance := range instances {
		_ = instance.Interrupt(signal.TERM)
	}
	if k.fds != nil {
		for _, process := range processes {
			_ = k.fds.Detach(uint64(process.ID))
		}
	}
	k.children.Close()
	_ = k.life.enter(StateClosed)
	return ctx.Err()
}

// ActiveProcesses 返回当前有活动执行实例的进程号(C06:关闭时只等待
// 真正在运行的客体程序,内核自身的会话进程不计入)。
func (k *Kernel) ActiveProcesses() []proc.PID {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]proc.PID, 0, len(k.instances))
	for pid := range k.instances {
		out = append(out, pid)
	}
	slices.Sort(out)
	return out
}

// AwaitExit 等待内核中正在运行的客体程序全部结束(C06/C07):上下文
// 到期即返回,不遗留宿主 goroutine——已中断的程序会自行退出。
func (k *Kernel) AwaitExit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	k.mu.Lock()
	instances := make([]executable.Instance, 0, len(k.instances))
	for _, instance := range k.instances {
		instances = append(instances, instance)
	}
	k.mu.Unlock()
	for _, instance := range instances {
		if _, err := instance.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kernel) Closed() (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.closed, nil
}

// meteredStream 把客体输出按字节计入进程 Output 预算(T03)，
// 底层写入器保留原语义。
type meteredStream struct {
	inner  io.Writer
	meters *resources.Meters
	pid    uint64
}

func (m meteredStream) Write(p []byte) (int, error) {
	if m.meters != nil && len(p) > 0 {
		if err := m.meters.ChargeKind(context.Background(), m.pid, resources.KindOutput, uint64(len(p))); err != nil {
			return 0, err
		}
	}
	return m.inner.Write(p)
}

// Failed 报告内核是否因客体故障进入失败状态(T01/T02)。
func (k *Kernel) Failed() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.failed
}

// SessionBudget 返回 VM 级资源预算视图(T01/T03)。
func (k *Kernel) SessionBudget(ctx context.Context) (resources.View, error) {
	if k.meters == nil {
		return resources.View{}, errno.NotImplemented
	}
	return k.meters.SessionView(ctx)
}

// ProcessBudget 返回指定进程的资源预算视图(T01/T03)。
func (k *Kernel) ProcessBudget(ctx context.Context, pid proc.PID) (resources.View, error) {
	if k.meters == nil {
		return resources.View{}, errno.NotImplemented
	}
	return k.meters.View(ctx, uint64(pid))
}

func (k *Kernel) OpenFDs(pid proc.PID) ([]uint32, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, err := k.table.Get(pid); err != nil {
		return nil, err
	}
	if k.fds == nil {
		return []uint32{}, nil
	}
	return k.fds.Opened(uint64(pid)), nil
}

func (k *Kernel) EnsureQuietPoint() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return errno.Cancelled
	}
	if k.executing {
		return errno.Busy
	}
	for _, p := range k.table.List() {
		if p.ID == k.init {
			continue
		}
		return errno.Busy
	}
	return nil
}

func (k *Kernel) SnapshotFS(ctx context.Context) (vmSnapshot, error) {
	if err := k.EnsureQuietPoint(); err != nil {
		return nil, err
	}
	system, err := k.services.Filesystem()
	if err != nil {
		return nil, err
	}
	snapshooter, ok := system.(vmfsSnapshotter)
	if !ok {
		return nil, errno.NotSupported
	}
	return snapshooter.Snapshot(ctx)
}

func (k *Kernel) SnapshotTable(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	k.mu.Lock()
	closed := k.closed
	k.mu.Unlock()
	if closed {
		return nil, errno.Cancelled
	}
	if err := k.EnsureQuietPoint(); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return nil, errno.Cancelled
	}
	data, err := k.table.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	k.emitLocked(scheduler.KindSnapshot, k.init, scheduler.SnapshotDetail(len(data)))
	return data, nil
}

func (k *Kernel) RestoreTable(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return errno.Cancelled
	}
	if k.executing {
		return errno.Busy
	}
	if err := k.table.Restore(ctx, data); err != nil {
		return err
	}

	k.signals.Reset()
	k.init = k.table.Init()

	for _, process := range k.table.List() {
		k.signals.Spawn(uint64(process.ID))
	}
	k.emitLocked(scheduler.KindRestore, k.init, scheduler.RestoreDetail(len(data)))
	return nil
}

func (k *Kernel) Terminal(ctx context.Context, pid proc.PID, id uint32) (fd.Terminal, error) {
	if err := ctx.Err(); err != nil {
		return fd.Terminal{}, err
	}
	k.mu.Lock()
	fds := k.fds
	if k.closed {
		k.mu.Unlock()
		return fd.Terminal{}, errno.Cancelled
	}
	k.mu.Unlock()
	if fds == nil {
		return fd.Terminal{}, errno.NotImplemented
	}
	return fds.Terminal(ctx, uint64(pid), id)
}

func (k *Kernel) emitLocked(kind string, pid proc.PID, detail string) {
	if k.sequencer != nil {
		if k.sequencer.Append(uint64(pid), kind, detail) == 0 {
			k.failed = true
			_ = k.life.enter(StateFailed)
			_ = k.life.enter(StateFailed)
			return
		}
	}
	if _, err := capability.Require(k.services.Audit); err != nil {
		return
	}
	k.auditSeq++
	payload := []byte(detail)
	err := k.services.Audit.Append(context.Background(), audit.Event{
		Sequence: k.auditSeq,
		PID:      uint64(pid),
		Kind:     kind,
		Payload:  payload,
	})
	if err != nil {
		k.failed = true
		_ = k.life.enter(StateFailed)
	}
}

type (
	vmSnapshot      = vmfs.Snapshotted
	vmfsSnapshotter = vmfs.Snapshotter
)

func (k *Kernel) Replace(ctx context.Context, target proc.PID, name string, args []string, dir string, env map[string]string) (proc.Proc, error) {
	if err := ctx.Err(); err != nil {
		return proc.Proc{}, err
	}
	if name == "" || dir == "" || dir[0] != '/' {
		return proc.Proc{}, errno.Invalid
	}

	resolved, err := k.resolve(name)
	if err != nil {
		return proc.Proc{}, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return proc.Proc{}, errno.Cancelled
	}
	if k.executing {
		return proc.Proc{}, errno.Busy
	}
	before, after, err := k.table.Replace(target, resolved.Name, args, dir, env)
	if err != nil {
		return proc.Proc{}, err
	}
	if k.fds != nil {
		if _, err := k.fds.CloseCloexec(ctx, uint64(target)); err != nil {

			if _, _, rollbackErr := k.table.Replace(target, before.Program, before.Args, before.Dir, before.Env); rollbackErr != nil {
				k.failed = true
				_ = k.life.enter(StateFailed)
				_ = k.life.enter(StateFailed)
			}
			return proc.Proc{}, err
		}
	}
	if err := k.signals.ResetForExec(uint64(target)); err != nil {
		k.failed = true
		_ = k.life.enter(StateFailed)
		return proc.Proc{}, err
	}

	k.emitLocked(scheduler.KindExec, target, scheduler.ExecDetail(after.Program, after.Generation))
	return after, nil
}

type kernelUmask struct{ kernel *Kernel }

func (u kernelUmask) Umask(pid uint64) (stdfs.FileMode, error) {
	u.kernel.mu.Lock()
	defer u.kernel.mu.Unlock()
	p, err := u.kernel.table.Get(proc.PID(pid))
	if err != nil {
		return 0, err
	}
	return p.Umask, nil
}

func (k *Kernel) Usage(id proc.PID) (proc.Usage, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return proc.Usage{}, errno.Cancelled
	}
	if _, err := k.table.Get(id); err != nil {
		return proc.Usage{}, err
	}
	usage := proc.Usage{Children: k.childrenWork[id]}
	if k.meters != nil {
		if view, err := k.meters.View(context.Background(), uint64(id)); err == nil {
			usage.Self = view.WorkUsed
		}
	}
	return usage, nil
}

func (k *Kernel) SetUmask(id proc.PID, mask stdfs.FileMode) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failed {
		return errno.Busy
	}
	if k.closed {
		return errno.Cancelled
	}
	return k.table.SetUmask(id, mask)
}

func clampNice(value int) int {
	switch {
	case value < proc.NiceLow:
		return proc.NiceLow
	case value > proc.NiceHigh:
		return proc.NiceHigh
	default:
		return value
	}
}

type procControl struct {
	k    *Kernel
	self proc.PID
}

func (c procControl) Begin(parent proc.PID, name string, args []string, dir string, env map[string]string, streams fd.Streams) (proc.Proc, error) {
	return c.k.Begin(parent, name, args, dir, env, streams)
}

func (c procControl) MissingFile(ctx context.Context, path string) {
	c.k.MissingFileGap(ctx, c.self, path)
}

func (c procControl) Finish(parent, id proc.PID, code uint8) error {
	return c.k.Finish(parent, id, ExitStatus{Code: code})
}

// FinishCurrent 以当前表内父进程写入终态(K10/H13):fork 子进程的
// 父进程可能已退出并收养给 init——运行 continuation 查询后调用。
func (c procControl) FinishCurrent(id proc.PID, code uint8) error {
	c.k.mu.Lock()
	p, err := c.k.table.Get(id)
	c.k.mu.Unlock()
	if err != nil {
		return err
	}
	return c.k.Finish(p.Parent, id, ExitStatus{Code: code})
}

func (c procControl) Tgkill(tgid, tid int64, number signal.Number) error {
	return c.k.Tgkill(proc.PID(tgid), proc.PID(tid), number)
}

func (c procControl) CloneThread(clearTID uint64) (proc.Proc, *memory.Table, error) {
	return c.k.CloneThread(c.self, clearTID)
}

func (c procControl) FinishThread(id proc.PID, code uint8, sig uint8) error {
	return c.k.FinishThread(id, code, sig)
}

func (c procControl) FinishGroup(id proc.PID, code uint8, sig uint8) error {
	return c.k.FinishGroup(id, code, sig)
}

func (c procControl) BindThreadInterrupt(id proc.PID, hook func(signal.Number)) {
	if hook == nil {
		c.k.signals.BindInterrupt(uint64(id), nil)
		return
	}
	c.k.signals.BindInterrupt(uint64(id), hook)
}

func (c procControl) SetUmask(id proc.PID, mask stdfs.FileMode) error {
	return c.k.SetUmask(id, mask)
}

func (c procControl) SetSignalAction(sig signal.Number, config signal.ActionConfig) error {
	if c.self == 0 {
		return errno.NoProcess
	}
	return c.k.signals.SetAction(uint64(c.self), sig, config)
}

func (c procControl) SignalAction(sig signal.Number) (signal.ActionConfig, error) {
	if c.self == 0 {
		return signal.ActionConfig{}, errno.NoProcess
	}
	return c.k.signals.Action(uint64(c.self), sig)
}

func (c procControl) SetSignalMask(how signal.MaskHow, set uint64) (uint64, error) {
	if c.self == 0 {
		return 0, errno.NoProcess
	}
	return c.k.signals.ApplyMask(uint64(c.self), how, set)
}

func (c procControl) SignalMask() (uint64, error) {
	if c.self == 0 {
		return 0, errno.NoProcess
	}
	return c.k.signals.Mask(uint64(c.self))
}

func (c procControl) Fork() (proc.Proc, *memory.Table, error) {
	if c.self == 0 {
		return proc.Proc{}, nil, errno.NoProcess
	}
	return c.k.Fork(c.self)
}

func (c procControl) Vfork() (proc.Proc, *memory.Table, error) {
	if c.self == 0 {
		return proc.Proc{}, nil, errno.NoProcess
	}
	return c.k.Vfork(c.self)
}

func (c procControl) PrepareSpace() (*memory.Table, error) {
	if c.self == 0 {
		return nil, errno.NoProcess
	}
	return c.k.PrepareSpace(c.self)
}

func (c procControl) CommitSpace() error {
	if c.self == 0 {
		return errno.NoProcess
	}
	return c.k.CommitSpace(c.self)
}

func (c procControl) DiscardSpace() {
	if c.self == 0 {
		return
	}
	c.k.DiscardSpace(c.self)
}

// SetPGID 按 setpgid(2) 语义把目标进程移入进程组(H11):pid=0 表示
// 调用方自身,pgid=0 表示以目标进程号作为组号;负值 EINVAL。
func (c procControl) SetPGID(pid int64, pgid int64) error {
	if c.self == 0 {
		return errno.NoProcess
	}
	if pid < 0 || pgid < 0 {
		return errno.Invalid
	}
	target := c.self
	if pid != 0 {
		target = proc.PID(pid)
	}
	group := target
	if pgid != 0 {
		group = proc.PID(pgid)
	}
	return c.k.SetPGID(c.self, target, group)
}

// SetSID 按 setsid(2) 为调用方建立新会话:调用方成为会话与进程组组长。
func (c procControl) SetSID() error {
	if c.self == 0 {
		return errno.NoProcess
	}
	return c.k.SetSID(c.self)
}

// GetPGID 返回目标进程的进程组号;pid=0 表示调用方自身。
// 负 pid 与不存在的进程一样按 ESRCH 应答(Linux 内核语义)。
func (c procControl) GetPGID(pid int64) (uint64, error) {
	if c.self == 0 {
		return 0, errno.NoProcess
	}
	target := c.self
	if pid != 0 {
		target = proc.PID(pid)
	}
	value, err := c.k.GetPGID(c.self, target)
	if err != nil {
		return 0, err
	}
	return uint64(value), nil
}

// GetSID 返回目标进程的会话号;pid=0 表示调用方自身。
// 负 pid 与不存在的进程一样按 ESRCH 应答(Linux 内核语义)。
func (c procControl) GetSID(pid int64) (uint64, error) {
	if c.self == 0 {
		return 0, errno.NoProcess
	}
	target := c.self
	if pid != 0 {
		target = proc.PID(pid)
	}
	value, err := c.k.GetSID(c.self, target)
	if err != nil {
		return 0, err
	}
	return uint64(value), nil
}

// RecordSyscall 记录一次受限系统调用轨迹(G08):只带标识、调用编号、
// 结果与耗时,不记录任何缓冲区内容。
func (c procControl) RecordSyscall(pid, tid uint64, number uint64, result int64, duration trace.Duration) {
	if c.k.trace == nil {
		return
	}
	c.k.trace.Record(trace.Entry{
		PID: pid, TID: tid, Number: number, Result: result, Duration: duration,
	})
}

// FutexTable 暴露内核 futex 等待表(K05)。
func (c procControl) FutexTable() *futex.Table {
	return c.k.futexes
}

// SetTidAddress 登记 clear_child_tid 地址(K03,set_tid_address);
// 已有登记被覆盖,0 表示未登记。
func (c procControl) SetTidAddress(address uint64) error {
	c.k.mu.Lock()
	defer c.k.mu.Unlock()
	if c.k.closed {
		return errno.Cancelled
	}
	if c.self == 0 {
		return errno.NoProcess
	}
	c.k.tidAddresses[c.self] = address
	return nil
}

// TidAddress 返回登记的 clear_child_tid 地址(K03);未登记为 0。
func (c procControl) SetRobustList(head, length uint64) error {
	c.k.mu.Lock()
	defer c.k.mu.Unlock()
	if c.k.closed {
		return errno.Cancelled
	}
	if c.self == 0 {
		return errno.NoProcess
	}
	c.k.robustLists[c.self] = robustList{head: head, len: length}
	return nil
}

func (c procControl) RobustList(head, length *uint64) error {
	c.k.mu.Lock()
	defer c.k.mu.Unlock()
	entry, ok := c.k.robustLists[c.self]
	if !ok {
		return errno.NoProcess
	}
	*head = entry.head
	*length = entry.len
	return nil
}

// HandleRobustListAtExit 遍历登记的 robust list(K07):对每个仍由
// 本线程持有的 futex 字(tid 匹配),或上 FUTEX_OWNER_DIED|WAITERS
// 位并清 TID,再唤醒该地址等待者。遍历上限防环形/坏指针(fail-
// closed 报告,不悬挂);读经 mem,写经 mem(执行器注入地址空间)。
func (c procControl) HandleRobustListAtExit(ctx context.Context, mem proc.RobustMemory) error {
	c.k.mu.Lock()
	list, ok := c.k.robustLists[c.self]
	tid := uint64(c.self)
	delete(c.k.robustLists, c.self)
	c.k.mu.Unlock()
	if !ok || list.head == 0 {
		return nil
	}
	futexes := c.k.futexes
	// 唤醒键与 futexKey 的私有分域一致:按线程组(K02)。
	owner := tid
	if p, err := c.k.table.Get(c.self); err == nil {
		owner = uint64(p.TGID)
	}
	const maxRobustEntries = 1024
	address := list.head
	for step := 0; step < maxRobustEntries; step++ {
		if address == 0 {
			return nil
		}
		var raw [8]byte
		if err := mem.Read(ctx, address, raw[:]); err != nil {
			return errno.Fault
		}
		next := uint64(0)
		for i := 0; i < 8; i++ {
			next |= uint64(raw[i]) << (8 * i)
		}
		var futexRaw [4]byte
		if err := mem.Read(ctx, address+8, futexRaw[:]); err != nil {
			return errno.Fault
		}
		word := uint32(futexRaw[0]) | uint32(futexRaw[1])<<8 | uint32(futexRaw[2])<<16 | uint32(futexRaw[3])<<24
		if word&futexTidMask == uint32(tid) {
			updated := word&futexWaitersBit | futexOwnerDiedBit
			var out [4]byte
			out[0] = byte(updated)
			out[1] = byte(updated >> 8)
			out[2] = byte(updated >> 16)
			out[3] = byte(updated >> 24)
			if err := mem.Write(ctx, address+8, out[:]); err != nil {
				return errno.Fault
			}
			futexes.Wake(futex.Key{Owner: owner, Address: address + 8}, 1)
		}
		if next != 0 && next <= address {
			// 环形或倒退链:停止遍历(fail-closed)。
			return nil
		}
		address = next
	}
	return nil
}

func (c procControl) NotifyBroker() *inotify.Broker {
	return c.k.notifies
}

func (c procControl) NetNamespace() *net.Namespace {
	return c.k.network
}

func (c procControl) Locks() *locks.Table {
	return c.k.locks
}

func (c procControl) TidAddress() uint64 {
	c.k.mu.Lock()
	defer c.k.mu.Unlock()
	return c.k.tidAddresses[c.self]
}

// ClearTidAtExit 在执行流终止时落实 K03 语义:登记地址写零(客体
// 地址空间)并按 futex 语义唤醒该地址上的等待者。地址写不进(已
// 解除映射)时仍执行唤醒——等待者条件由其自身复查。执行器在运行
// 流返回后调用;线程化(K02)后移至线程退出路径。
func (c procControl) ClearTidAtExit(ctx context.Context, write func(context.Context, uint64, []byte) error) {
	address := c.TidAddress()
	if address == 0 {
		return
	}
	c.k.mu.Lock()
	delete(c.k.tidAddresses, c.self)
	c.k.mu.Unlock()
	if write != nil {
		_ = write(ctx, address, make([]byte, 8))
	}
	// 唤醒键按线程组(K02):等待 join 的线程与退出线程同组。
	owner := uint64(c.self)
	if p, err := c.k.table.Get(c.self); err == nil {
		owner = uint64(p.TGID)
	}
	if c.k.futexes != nil {
		c.k.futexes.Wake(futex.Key{Owner: owner, Address: address}, math.MaxInt32)
	}
}

func (c procControl) KillSignal(pid int64, n signal.Number) error {
	if n != 0 && !n.Valid() {
		return errno.Invalid
	}
	if c.self == 0 {
		return errno.NoProcess
	}
	// sig=0 是存在性探测:目标可寻址即成功,不投递。
	if n == 0 {
		switch {
		case pid > 0:
			if _, err := c.k.table.Get(proc.PID(pid)); err != nil {
				return err
			}
			return nil
		case pid == 0:
			caller, err := c.k.table.Get(c.self)
			if err != nil {
				return err
			}
			if !job.GroupExists(c.k.table, caller.PGID) {
				return errno.NoProcess
			}
			return nil
		case pid == -1:
			// 调用方自身总在可信号集合中。
			return nil
		default:
			if !job.GroupExists(c.k.table, proc.PID(-pid)) {
				return errno.NoProcess
			}
			return nil
		}
	}
	switch {
	case pid > 0:
		return c.k.Signal(proc.PID(pid), n)
	case pid == 0:
		caller, err := c.k.table.Get(c.self)
		if err != nil {
			return err
		}
		return c.k.SignalGroup(caller.PGID, n)
	case pid == -1:
		return c.k.SignalAll(c.self, n)
	default:
		return c.k.SignalGroup(proc.PID(-pid), n)
	}
}

// ResetForExec 完成 execve 的内核侧状态复位(H07):进程表元数据
// 随新映像更新并发出 KindExec 事件,CLOEXEC 描述符关闭,信号处置/
// 屏蔽/挂起复位。执行器先完成映像替换再调用;失败即 fail-closed。
func (c procControl) ResetForExec(path string, args []string, env map[string]string) error {
	// exec 复位 clear_child_tid 登记(与内核 clear_child_tid exec
	// 语义一致)。
	if c.self == 0 {
		return errno.NoProcess
	}
	c.k.mu.Lock()
	delete(c.k.tidAddresses, c.self)
	c.k.mu.Unlock()
	current, err := c.k.table.Get(c.self)
	if err != nil {
		return err
	}
	// execve 保留工作目录;程序名/参数/环境随新映像更新。
	before, after, err := c.k.table.Replace(c.self, path, args, current.Dir, env)
	if err != nil {
		return err
	}
	c.k.mu.Lock()
	c.k.emitLocked(scheduler.KindExec, c.self, scheduler.ExecDetail(after.Program, after.Generation))
	c.k.mu.Unlock()
	if c.k.fds != nil {
		if _, err := c.k.fds.CloseCloexec(context.Background(), uint64(c.self)); err != nil {
			if _, _, rollbackErr := c.k.table.Replace(c.self, before.Program, before.Args, before.Dir, before.Env); rollbackErr != nil {
				c.k.mu.Lock()
				c.k.failed = true
				c.k.mu.Unlock()
			}
			return err
		}
	}
	return c.k.signals.ResetForExec(uint64(c.self))
}

func (c procControl) PendingDeliverable() (signal.Number, signal.ActionConfig, bool) {
	if c.self == 0 {
		return 0, signal.ActionConfig{}, false
	}
	return c.k.signals.DeliverableHandler(uint64(c.self))
}

func (c procControl) ConsumeSignal(n signal.Number) {
	if c.self == 0 {
		return
	}
	c.k.signals.Consume(uint64(c.self), n)
}

func (c procControl) BlockForHandler(n signal.Number, handlerMask uint64, nodefer bool) (uint64, error) {
	if c.self == 0 {
		return 0, errno.NoProcess
	}
	return c.k.signals.BlockForHandler(uint64(c.self), n, handlerMask, nodefer)
}

func (c procControl) RestoreHandlerMask(mask uint64) error {
	if c.self == 0 {
		return errno.NoProcess
	}
	return c.k.signals.RestoreHandlerMask(uint64(c.self), mask)
}

func (c procControl) AlternateStack() (signal.AltStack, bool) {
	if c.self == 0 {
		return signal.AltStack{}, false
	}
	stack := c.k.signals.AltStack(uint64(c.self))
	return stack, stack.Present
}

func (c procControl) SetAlternateStack(stack signal.AltStack) error {
	if c.self == 0 {
		return errno.NoProcess
	}
	c.k.signals.SetAltStack(uint64(c.self), stack)
	return nil
}

func (c procControl) WaitChild(ctx context.Context, target proc.PID, nohang bool) (proc.ChildState, bool, error) {
	if c.self == 0 {
		return proc.ChildState{}, false, errno.NoProcess
	}
	return c.k.WaitChild(ctx, c.self, target, nohang)
}

func (c procControl) HasChildren() bool {
	if c.self == 0 {
		return false
	}
	return c.k.HasChildren(c.self)
}

func (c procControl) Usage(id proc.PID) (proc.Usage, error) {
	return c.k.Usage(id)
}

func (c procControl) SystemConfiguration() (proc.SystemConfiguration, error) {
	return c.k.SystemConfiguration()
}

func (c procControl) Processes() []proc.Proc {
	return c.k.Processes()
}

func (c procControl) Nice(id proc.PID) (int, error) {
	c.k.mu.Lock()
	defer c.k.mu.Unlock()
	if c.k.closed {
		return 0, errno.Cancelled
	}
	p, err := c.k.table.Get(id)
	if err != nil {
		return 0, err
	}
	return p.Nice, nil
}

func (c procControl) SetNice(id proc.PID, nice int) error {
	c.k.mu.Lock()
	defer c.k.mu.Unlock()
	if c.k.failed {
		return errno.Busy
	}
	if c.k.closed {
		return errno.Cancelled
	}
	return c.k.table.SetNice(id, nice)
}

func (c procControl) QueueJob(ctx context.Context, spec proc.JobSpec) (string, error) {
	return c.k.QueueJob(ctx, spec)
}

func (c procControl) ListJobs(ctx context.Context) ([]string, error) {
	return c.k.ListJobs(ctx)
}

func (c procControl) RemoveJob(ctx context.Context, id string) (bool, error) {
	return c.k.RemoveJob(ctx, id)
}

func (c procControl) ReadJob(ctx context.Context, id string) (string, error) {
	return c.k.ReadJob(ctx, id)
}

func (c procControl) QueuePrint(ctx context.Context, spec proc.PrintSpec) (string, error) {
	return c.k.QueuePrint(ctx, spec)
}

func (c procControl) ListPrintRequests(ctx context.Context) ([]string, error) {
	return c.k.ListPrintRequests(ctx)
}

func (c procControl) RunDueJobs(ctx context.Context) error {
	return c.k.RunDueJobs(ctx)
}

func (c procControl) Signal(id proc.PID, n signal.Number) error {
	return c.k.Signal(id, n)
}

func (c procControl) Exists(id proc.PID) (bool, error) {
	c.k.mu.Lock()
	defer c.k.mu.Unlock()
	if c.k.closed {
		return false, errno.Cancelled
	}
	if _, err := c.k.table.Get(id); err != nil {
		if errors.Is(err, errno.NoProcess) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c procControl) Run(ctx context.Context, spec proc.RunSpec) (proc.Proc, error) {
	child, err := c.k.Begin(spec.Parent, spec.Name, spec.Args, spec.Dir, spec.Env, spec.Streams)
	if err != nil {
		return proc.Proc{}, err
	}

	for _, number := range spec.IgnoreSignals {
		if err := c.k.signals.SetIgnored(uint64(child.ID), number, true); err != nil {
			_ = c.k.Finish(child.Parent, child.ID, ExitStatus{Code: 125})
			_, _, _ = c.k.WaitChild(context.Background(), child.Parent, child.ID, true)
			return proc.Proc{}, err
		}
	}

	if spec.NiceAdjust != nil {
		c.k.mu.Lock()
		parent, err := c.k.table.Get(child.Parent)
		if err != nil {
			c.k.mu.Unlock()
			_ = c.k.Finish(child.Parent, child.ID, ExitStatus{Code: 125})
			_, _, _ = c.k.WaitChild(context.Background(), child.Parent, child.ID, true)
			return proc.Proc{}, err
		}
		adjustErr := c.k.table.SetNice(child.ID, clampNice(parent.Nice+*spec.NiceAdjust))
		c.k.mu.Unlock()
		if adjustErr != nil {
			_ = c.k.Finish(child.Parent, child.ID, ExitStatus{Code: 125})
			_, _, _ = c.k.WaitChild(context.Background(), child.Parent, child.ID, true)
			return proc.Proc{}, adjustErr
		}
	}
	status, runErr := c.k.RunRegistered(proc.WithContext(ctx, child), child)
	if ctxErr := ctx.Err(); ctxErr != nil {

		runErr = errors.Join(runErr, ctxErr)
		if status.Signal == 0 {
			status.Code = 125
		}
	}

	c.k.mu.Lock()
	exited, getErr := c.k.table.Get(child.ID)
	c.k.mu.Unlock()
	if getErr != nil || exited.State != proc.Zombie {

		exited = proc.Proc{
			ID: child.ID, Parent: child.Parent, Program: child.Program, State: proc.Zombie,
			ExitCode: status.Code, ExitSignal: uint8(status.Signal),
		}
	}
	// 同步运行助手即 system():结束时等待并回收子进程。RunRegistered
	// 已把子进程转僵尸,Finish 的重复终态在此走等待回收路径。
	if _, found, waitErr := c.k.WaitChild(context.WithoutCancel(ctx), child.Parent, child.ID, true); !found || waitErr != nil {
		// 子进程尚未转可回收僵尸(如启动前上下文已取消):终态化后
		// 回收,同步运行助手不留残留。
		if finishErr := c.k.Finish(child.Parent, child.ID, status); finishErr != nil {
			return exited, errors.Join(runErr, waitErr, finishErr)
		}
		_, _, _ = c.k.WaitChild(context.WithoutCancel(ctx), child.Parent, child.ID, true)
	}
	if runErr != nil {
		return exited, runErr
	}
	return exited, nil
}

// Enter 排队获取执行权(H02/H03):单核语义下等待轮到本进程。
func (c procControl) Enter(ctx context.Context) error {
	return c.k.queue.enter(ctx)
}

// Slice 让出执行权并等待下一轮:时间片用尽或进入可能阻塞的
// 系统调用时调用;没有其他等待者时立即继续。
func (c procControl) Slice(ctx context.Context) error {
	return c.k.queue.slice(ctx)
}

// Leave 归还执行权:进入长期阻塞、驻停或退出前调用;幂等。
func (c procControl) Leave() error {
	c.k.queue.leave()
	return nil
}
