package vm

import (
	"context"
	"errors"
	"fmt"
	stdfs "io/fs"
	"slices"
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
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/ipc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/job"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/pipe"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
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

	children  wait.Queue
	instances map[proc.PID]executable.Instance
	resolver  *executable.Resolver
	auditSeq  uint64

	spaces map[proc.PID]memory.Space

	jobsMu      sync.Mutex
	jobsRunning bool
}

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
	k.services = services
	k.meters = services.Meters
	k.sequencer = services.Sequencer
	k.signals = signal.New()
	k.instances = map[proc.PID]executable.Instance{}
	k.init = root.ID
	if fdt, ok := services.FD.(*fd.Table); ok {
		k.fds = fdt
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
	return k, nil
}

func (k *Kernel) Processes() []proc.Proc { k.mu.Lock(); defer k.mu.Unlock(); return k.table.List() }

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

func (k *Kernel) afterReapLocked(p proc.Proc) error {
	k.emitLocked(scheduler.KindReap, p.ID, scheduler.ReapDetail(p.Program))
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
	var closeErr error
	if k.fds != nil {
		closeErr = k.fds.Detach(uint64(p.ID))
	}

	delete(k.spaces, p.ID)
	return closeErr
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
	if k.fds != nil {
		k.fds.Attach(uint64(p.ID), streams)
	}
	k.emitLocked(scheduler.KindSpawn, p.ID, scheduler.SpawnDetail(resolved.Name))
	if k.failed {

		return proc.Proc{}, errno.Busy
	}
	return p, nil
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
	defer func() {
		k.mu.Unlock()
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
	reaped, err := k.table.Reap(parent, id)
	if err != nil {
		return err
	}
	closeErr := k.afterReapLocked(reaped)
	k.emitLocked(scheduler.KindExit, id, scheduler.ExitDetail(status.Code, status.Signal))
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
		k.mu.Unlock()
		reason, err := k.children.Wait(ctx, func() (bool, wait.Reason, error) {
			k.mu.Lock()
			defer k.mu.Unlock()
			if k.closed {
				return true, wait.Shutdown, nil
			}
			if target != 0 {
				p, err := k.table.Get(target)
				return err == nil && p.Parent == parent && p.State == proc.Zombie, wait.Data, nil
			}
			_, ok := k.table.WaitAny(parent)
			return ok, wait.Data, nil
		})
		if err != nil {
			if reason == wait.Shutdown {
				return proc.Proc{}, errno.Cancelled
			}
			return proc.Proc{}, err
		}

	}
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
			if p.State != proc.Zombie {
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
	switch action {
	case signal.Terminate, signal.Core:
		k.signals.Interrupt(uint64(target), n)
		return nil
	case signal.Ignore:
		return nil
	default:
		return errno.NotSupported
	}
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

	if k.meters != nil && !k.fds.IsOpen(uint64(pid), id) {
		if err := k.meters.ChargeKind(context.Background(), uint64(pid), resources.KindDescriptors, 1); err != nil {
			return err
		}
	}

	if err := k.fds.Close(context.Background(), uint64(pid), id); err != nil && !errors.Is(err, errno.BadHandle) {
		return err
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

	opened := k.fds.IsOpen(uint64(pid), id)
	err := k.fds.Close(context.Background(), uint64(pid), id)
	if err == nil && opened && k.meters != nil {
		_ = k.meters.ReleaseKind(context.Background(), uint64(pid), resources.KindDescriptors, 1)
	}
	return err
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
	if err := ctx.Err(); err != nil {
		return ExitStatus{Code: 125}, err
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
		return ExitStatus{Code: 125}, err
	}

	actual.Resources = capability.Scope(uint64(actual.ID), k.services.Set, k.meters)
	actual.Control = procControl{k: k, self: actual.ID}
	runCtx, cancel := context.WithCancelCause(ctx)

	instance, err := executable.NewBuiltin().Start(proc.WithContext(runCtx, actual), resolved.Program, actual)
	if err != nil {
		cancel(nil)
		return ExitStatus{Code: 125}, err
	}
	k.mu.Lock()
	k.instances[pid] = instance
	k.signals.BindInterrupt(uint64(pid), func(n signal.Number) {
		_ = instance.Interrupt(n)
	})

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
	status := ExitStatus{Code: result.Code}
	if result.Reason == executable.Signaled {
		status = ExitStatus{Code: signal.ExitCode(result.Signal), Signal: result.Signal}
	}
	var runErr error
	switch {
	case result.Reason == executable.Fault:

		k.mu.Lock()
		k.failed = true
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
	if ctxErr := ctx.Err(); ctxErr == nil {
		if err := k.table.Exit(pid, status.Code, uint8(status.Signal)); err == nil {
			k.emitLocked(scheduler.KindExit, pid, scheduler.ExitDetail(status.Code, status.Signal))
			k.mu.Unlock()
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
	if err := k.table.Exit(pid, code, uint8(status.Signal)); err != nil {
		k.mu.Unlock()
		return status, errors.Join(runErr, err)
	}
	k.emitLocked(scheduler.KindExit, pid, scheduler.ExitDetail(code, status.Signal))
	k.mu.Unlock()
	k.children.WakeAll(wait.Data)
	return status, errors.Join(runErr, ctx.Err())
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
	instances := make([]executable.Instance, 0, len(k.instances))
	for _, instance := range k.instances {
		instances = append(instances, instance)
	}
	k.emitLocked(scheduler.KindClose, k.init, "session")
	k.mu.Unlock()
	for _, instance := range instances {
		_ = instance.Interrupt(signal.TERM)
	}
	k.children.Close()
	return ctx.Err()
}

func (k *Kernel) Closed() (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.closed, nil
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
			}
			return proc.Proc{}, err
		}
	}
	if err := k.signals.ResetForExec(uint64(target)); err != nil {
		k.failed = true
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

func (c procControl) Finish(parent, id proc.PID, code uint8) error {
	return c.k.Finish(parent, id, ExitStatus{Code: code})
}

func (c procControl) SetUmask(id proc.PID, mask stdfs.FileMode) error {
	return c.k.SetUmask(id, mask)
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
			return proc.Proc{}, err
		}
	}

	if spec.NiceAdjust != nil {
		c.k.mu.Lock()
		parent, err := c.k.table.Get(child.Parent)
		if err != nil {
			c.k.mu.Unlock()
			_ = c.k.Finish(child.Parent, child.ID, ExitStatus{Code: 125})
			return proc.Proc{}, err
		}
		adjustErr := c.k.table.SetNice(child.ID, clampNice(parent.Nice+*spec.NiceAdjust))
		c.k.mu.Unlock()
		if adjustErr != nil {
			_ = c.k.Finish(child.Parent, child.ID, ExitStatus{Code: 125})
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
	finishErr := c.k.Finish(child.Parent, child.ID, status)
	if runErr != nil || finishErr != nil {
		return exited, errors.Join(runErr, finishErr)
	}
	return exited, nil
}
