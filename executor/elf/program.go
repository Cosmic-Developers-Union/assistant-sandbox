package elf

import (
	"context"
	"errors"
	"io"
	"slices"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/common"
	linuxriscv64 "github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/riscv64"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu/riscv64"
	loaderelf "github.com/Cosmic-Developers-Union/assistant-sandbox/loader/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

const randomBytes = 16

type Factory struct{}

func (Factory) New(path string) (executable.Program, []string, error) {
	if path == "" {
		return nil, nil, errno.Wrap(errno.Invalid, "程序路径不能为空")
	}
	return &program{path: path}, []string{path}, nil
}

type program struct{ path string }

func (p *program) Run(ctx context.Context, process proc.Proc) (uint8, error) {
	image, err := p.image(ctx, process)
	if err != nil {
		return 0, err
	}
	plan, err := loaderelf.Load(image, loaderelf.Options{
		Argv:     argv(process, p.path),
		Env:      environment(process),
		ExecPath: p.path,
		Random:   randomBytesFor(ctx, process),
	})
	if err != nil {
		return 0, err
	}

	if err := chargeMemory(ctx, process, plan); err != nil {
		return 0, err
	}
	space, err := addressSpace(ctx, plan)
	if err != nil {
		return 0, err
	}

	if binder, err := process.Resources.AddressSpace(); err == nil {
		if _, err := binder.BindAddressSpace(uint64(process.ID), space); err != nil {
			return 0, err
		}
	}
	core, err := riscv64.New(cpu.Config{
		Memory: space,
		Entry:  plan.Entry,
		Meter:  meter{ctx: ctx, meters: metersOf(process), pid: uint64(process.ID)},
	})
	if err != nil {
		return 0, err
	}
	// 初始机器状态:入口 + 装载器布置的栈指针(argv/envp/auxv 顶)。
	initial := cpu.Machine{PC: plan.Entry, Registers: make([]uint64, 32), Floats: make([]uint64, 32)}
	initial.Registers[regSp] = plan.Stack.SP
	if err := core.Reset(ctx, initial); err != nil {
		return 0, err
	}
	handler := syscalls(ctx, process, space, plan, core)
	handler.Core = core
	deliverer := &signalDeliverer{space: space, core: core}
	if delivery, ok := process.Control.(proc.SignalDeliveryControl); ok {
		deliverer.control = delivery
	}
	runner, err := common.NewRunner(common.Config{
		Core:       core,
		Dispatcher: &linuxriscv64.Adapter{Handler: handler, Registers: core},
		Boundary:   deliverer.boundary,
	})
	if err != nil {
		return 0, err
	}
	status, err := runner.Run(ctx)
	if err != nil {
		return 0, err
	}
	if status > 255 {
		return 0, errno.Overflow
	}
	return uint8(status), nil
}

func (p *program) image(ctx context.Context, process proc.Proc) (content []byte, resultErr error) {
	if process.Resources == nil {

		return nil, errno.Wrap(errno.NotImplemented, "进程没有绑定资源能力")
	}
	system, err := process.Resources.Filesystem()
	if err != nil {
		return nil, err
	}
	file, err := system.Open(ctx, p.path, 0, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
	}()

	content, err = io.ReadAll(io.LimitReader(file, loaderelf.DefaultMaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if uint64(len(content)) > loaderelf.DefaultMaxImageBytes {
		return nil, errno.Wrap(errno.Limit, "程序映像超过可装载上限")
	}
	return content, nil
}

func chargeMemory(ctx context.Context, process proc.Proc, plan *loaderelf.Plan) error {
	meters := metersOf(process)
	if meters == nil {
		return nil
	}
	var total uint64
	for _, segment := range plan.Segments {
		total += segment.Region.End - segment.Region.Start
	}
	total += plan.Stack.Region.End - plan.Stack.Region.Start
	if total == 0 {
		return nil
	}
	return meters.ChargeKind(ctx, uint64(process.ID), resources.KindMemory, total)
}

func addressSpace(ctx context.Context, plan *loaderelf.Plan) (*memory.Table, error) {
	space := memory.New(0)
	for _, segment := range plan.Segments {
		populate := segment.Region
		populate.Prot = memory.ProtRead | memory.ProtWrite
		if err := space.Map(ctx, populate); err != nil {
			return nil, err
		}
		if err := space.Write(ctx, segment.Address, segment.Data); err != nil {
			return nil, err
		}
		if segment.Region.Prot != populate.Prot {
			if err := space.Protect(ctx, segment.Region.Start, segment.Region.End, segment.Region.Prot); err != nil {
				return nil, err
			}
		}
	}
	if err := space.Map(ctx, plan.Stack.Region); err != nil {
		return nil, err
	}
	if err := space.Write(ctx, plan.Stack.ContentBase, plan.Stack.Content); err != nil {
		return nil, err
	}
	return space, nil
}

func syscalls(ctx context.Context, process proc.Proc, space *memory.Table, plan *loaderelf.Plan, core cpu.Port) common.Syscalls {
	handler := common.Syscalls{Memory: space, Sysname: "Linux", Nodename: "assistant-sandbox", Release: "6.8.0", Version: "#1", Machine: "riscv64", Domainname: "(none)", Cwd: process.Dir}
	identity := &common.Identity{PID: uint64(process.ID), PPID: uint64(process.Parent)}
	handler.Identity = identity
	if process.Resources == nil {

		return handler
	}
	if descriptors, err := process.Resources.Descriptors(); err == nil {
		handler.Descriptors = common.ProcessDescriptors{Table: descriptors, PID: uint64(process.ID)}

		if installer, ok := descriptors.(fd.Installer); ok {
			if system, err := process.Resources.Filesystem(); err == nil {
				handler.Files = common.ProcessFiles{
					System: system,
					Store:  installer,
					PID:    uint64(process.ID),
					Dir:    process.Dir,
				}
			}
		}
	}

	if heap := newHeap(space, process, plan); heap != nil {
		handler.Heap = heap
	}
	if m := newMapper(space, process, plan); m != nil {
		handler.Mapper = m
	}
	handler.Limiter = &limiter{meters: metersOf(process), pid: uint64(process.ID)}
	if waiter, ok := process.Control.(proc.WaitControl); ok {
		handler.Waiter = processWaiter{control: waiter}
	}
	if dispositions, ok := process.Control.(proc.SignalDispositionControl); ok {
		handler.Signals = processSignals{control: dispositions}
	}
	if forker, ok := process.Control.(proc.ForkControl); ok {
		handler.Spawner = processForker{
			parent: process, space: space, plan: plan, core: core,
			control: forker,
		}
	}
	if source, err := process.Resources.Time(); err == nil {
		handler.Clock = source
		handler.Sleeper = source
	}
	if credentials, err := process.Resources.Credentials(); err == nil {
		if current, err := credentials.Lookup(ctx, uint64(process.ID)); err == nil {
			identity.UID, identity.GID = current.UID, current.GID
		}
	}
	return handler
}

type meter struct {
	ctx    context.Context
	meters resources.Meter
	pid    uint64
}

func (m meter) Charge(instructions uint64) error {
	if m.meters == nil {
		return nil
	}
	return m.meters.Charge(m.ctx, m.pid, instructions)
}

func metersOf(process proc.Proc) resources.Meter {
	if process.Resources == nil {
		return nil
	}
	meters, err := process.Resources.Meter()
	if err != nil {
		return nil
	}
	return meters
}

func argv(process proc.Proc, path string) []string {
	if len(process.Args) == 0 {
		return []string{path}
	}
	return slices.Clone(process.Args)
}

func environment(process proc.Proc) []string {
	names := make([]string, 0, len(process.Env))
	for name := range process.Env {
		names = append(names, name)
	}
	slices.Sort(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+process.Env[name])
	}
	return environment
}

func randomBytesFor(ctx context.Context, process proc.Proc) [randomBytes]byte {
	var out [randomBytes]byte
	if process.Resources == nil {
		return out
	}
	source, err := process.Resources.Random()
	if err != nil {
		return out
	}
	if _, err := source.Read(ctx, out[:]); err != nil {
		return out
	}
	return out
}

var _ executable.Program = (*program)(nil)

const heapMaximum = 64 << 20

type heap struct {
	table *memory.Table
	meter resources.Meter
	pid   uint64
	start uint64
	brk   uint64
	limit uint64
}

func newHeap(table *memory.Table, process proc.Proc, plan *loaderelf.Plan) *heap {
	start := heapStart(plan)
	if start == 0 {
		return nil
	}
	limit := plan.Stack.Region.Start
	if limit <= start {
		limit = start + heapMaximum
	}
	return &heap{
		table: table, meter: metersOf(process), pid: uint64(process.ID),
		start: start, brk: start, limit: limit,
	}
}

func heapStart(plan *loaderelf.Plan) uint64 {
	var start uint64
	for _, segment := range plan.Segments {
		if segment.Region.Prot&memory.ProtWrite == 0 {
			continue
		}
		if segment.Region.End > start {
			start = segment.Region.End
		}
	}
	return start
}

func (h *heap) Break(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return h.brk, nil
}

func (h *heap) Grow(ctx context.Context, target uint64) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if target == 0 {
		return h.brk, nil
	}
	if target <= h.brk {
		if target < h.start {
			target = h.start
		}
		target = alignPage(target)
		if target < h.brk {
			if err := h.table.Unmap(ctx, target, h.brk); err != nil {
				return h.brk, nil
			}
			h.release(ctx, h.brk-target)
			h.brk = target
		}
		return h.brk, nil
	}
	target = alignPage(target)
	if target > h.limit {
		return h.brk, nil
	}
	size := target - h.brk
	if err := h.charge(ctx, size); err != nil {
		return h.brk, nil
	}
	region := memory.Region{Start: h.brk, End: target, Prot: memory.ProtRead | memory.ProtWrite, Name: "heap"}
	if err := h.table.Map(ctx, region); err != nil {
		h.release(ctx, size)
		return h.brk, nil
	}
	h.brk = target
	return h.brk, nil
}

func (h *heap) charge(ctx context.Context, size uint64) error {
	if h.meter == nil {
		return nil
	}
	return h.meter.ChargeKind(ctx, h.pid, resources.KindMemory, size)
}

func (h *heap) release(ctx context.Context, size uint64) {
	if h.meter == nil {
		return
	}
	_ = h.meter.ReleaseKind(ctx, h.pid, resources.KindMemory, size)
}

func alignPage(address uint64) uint64 {
	return (address + memory.PageSize - 1) &^ (memory.PageSize - 1)
}

var _ common.Heap = (*heap)(nil)

// processWaiter 把进程控制端口的子进程等待能力适配为 ABI 等待端口。
type processWaiter struct {
	control proc.WaitControl
}

func (w processWaiter) WaitChild(ctx context.Context, target uint64, nohang bool) (common.ChildState, bool, error) {
	state, found, err := w.control.WaitChild(ctx, proc.PID(target), nohang)
	if err != nil || !found {
		return common.ChildState{}, found, err
	}
	return common.ChildState{
		PID:        uint64(state.PID),
		ExitCode:   state.ExitCode,
		ExitSignal: uint32(state.ExitSignal),
	}, true, nil
}

func (w processWaiter) HasChildren() bool {
	return w.control.HasChildren()
}

// processSignals 把进程控制端口的信号处置能力适配为 ABI 登记端口。
type processSignals struct {
	control proc.SignalDispositionControl
}

func (p processSignals) SetAction(sig uint64, action common.SignalAction) error {
	if !signal.Number(sig).Valid() {
		return errno.Invalid
	}
	return p.control.SetSignalAction(signal.Number(sig), signal.ActionConfig{
		Handler: action.Handler, Flags: action.Flags, Restorer: action.Restorer, Mask: action.Mask,
	})
}

func (p processSignals) Action(sig uint64) (common.SignalAction, error) {
	config, err := p.control.SignalAction(signal.Number(sig))
	if err != nil {
		return common.SignalAction{}, err
	}
	return common.SignalAction{
		Handler: config.Handler, Flags: config.Flags, Restorer: config.Restorer, Mask: config.Mask,
	}, nil
}

func (p processSignals) ApplyMask(how, set uint64) (uint64, error) {
	switch how {
	case 1:
		return p.control.SetSignalMask(signal.MaskBlock, set)
	case 2:
		return p.control.SetSignalMask(signal.MaskUnblock, set)
	case 3:
		return p.control.SetSignalMask(signal.MaskSetMask, set)
	default:
		return 0, errno.Invalid
	}
}

func (p processSignals) Mask() (uint64, error) {
	return p.control.SignalMask()
}

// RestoreMask 在 rt_sigreturn 时恢复帧内保存的屏蔽集。
func (p processSignals) RestoreMask(ctx context.Context, mask uint64) error {
	if delivery, ok := p.control.(proc.SignalDeliveryControl); ok {
		return delivery.RestoreHandlerMask(mask)
	}
	return errno.NotSupported
}

func (p processSignals) AltStack(ctx context.Context) (common.SignalAltStack, error) {
	if delivery, ok := p.control.(proc.SignalDeliveryControl); ok {
		stack, present := delivery.AlternateStack()
		return common.SignalAltStack{Sp: stack.Sp, Size: stack.Size, Present: present}, nil
	}
	return common.SignalAltStack{}, errno.NotSupported
}

func (p processSignals) SetAltStack(ctx context.Context, stack common.SignalAltStack) error {
	if delivery, ok := p.control.(proc.SignalDeliveryControl); ok {
		return delivery.SetAlternateStack(signal.AltStack{Sp: stack.Sp, Size: stack.Size, Present: stack.Present})
	}
	return errno.NotSupported
}

// signalDeliverer 在指令边界投递登记了客户态处理器的信号:
// 保存完整机器状态进 rt_sigframe,重定向 PC 到处理器,
// rt_sigreturn 经同一帧恢复(J03/J04)。
type signalDeliverer struct {
	space   *memory.Table
	core    cpu.Port
	control proc.SignalDeliveryControl
}

func (d *signalDeliverer) boundary(ctx context.Context) error {
	if d.control == nil {
		return nil
	}
	number, action, ok := d.control.PendingDeliverable()
	if !ok || action.Handler <= signal.SigIgnore {
		return nil
	}
	machine, err := d.core.State(ctx)
	if err != nil {
		return err
	}
	// SA_ONSTACK 且登记了备用栈时在备用栈上开帧。
	sp := machine.Registers[regSp]
	base := common.AlignFrameBase(sp)
	altStack, present := d.control.AlternateStack()
	if action.Flags&uint32(common.SigactionOnStack) != 0 && present {
		base = common.AlignFrameBase(altStack.Sp + altStack.Size)
	}
	oldMask, err := d.control.BlockForHandler(number, action.Mask, action.Flags&uint32(common.SigactionNoDefer) != 0)
	if err != nil {
		return err
	}
	infoAddress, ucontextAddress, err := common.WriteSignalFrame(ctx, d.space, common.SignalFrameInput{
		Signal:    uint64(number),
		Action:    common.SignalAction{Handler: action.Handler, Flags: action.Flags, Restorer: action.Restorer, Mask: action.Mask},
		FrameBase: base,
		Machine:   machine,
		OldMask:   oldMask,
		AltStack:  common.SignalAltStack{Sp: altStack.Sp, Size: altStack.Size, Present: present},
	})
	if err != nil {
		return err
	}
	next := machine
	next.PC = action.Handler
	next.Registers[regSp] = base
	next.Registers[regArgument0] = uint64(number)
	if action.Flags&uint32(common.SigactionSigInfo) != 0 {
		next.Registers[regArgument0+1] = infoAddress
		next.Registers[regArgument0+2] = ucontextAddress
	}
	if err := d.core.Reset(ctx, next); err != nil {
		return err
	}
	d.control.ConsumeSignal(number)
	return nil
}

const (
	regSp        = 2
	regArgument0 = 10
)

// processForker 把进程控制端口的 fork 能力适配为 clone 系统调用:
// 复制地址空间(E10)后,子进程在自己的核心上从调用边界续接执行,
// 退出时按 Linux 语义转僵尸并唤醒父进程等待。
type processForker struct {
	parent  proc.Proc
	space   *memory.Table
	plan    *loaderelf.Plan
	core    cpu.Port
	control proc.ForkControl
}

func (f processForker) Fork(ctx context.Context) (uint64, error) {
	child, childSpace, err := f.control.Fork()
	if err != nil {
		return 0, err
	}
	if childSpace == nil {
		// 控制端口未托管地址空间时(纯执行器场景),由执行器以
		// E10 原语克隆;内核托管路径返回克隆后的子空间。
		childSpace, err = f.space.Clone()
		if err != nil {
			return 0, err
		}
	}
	// 续接边界:调用时刻的完整寄存器与 ecall 之后的 PC;
	// 子进程 fork 返回值为 0(Linux 语义)。
	state, err := f.core.State(ctx)
	if err != nil {
		return 0, err
	}
	go func() {
		// 子进程生命周期独立于 fork 调用方上下文:调用返回后子进程
		// 继续执行直到自身退出;随内核关闭的传播由 C06/C12 复核。
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		childCore, err := riscv64.New(cpu.Config{
			Memory: childSpace,
			Entry:  state.PC,
			Meter:  meter{ctx: runCtx, meters: metersOf(child), pid: uint64(child.ID)},
		})
		if err != nil {
			return
		}
		next := state
		next.Registers[regArgument0] = 0
		if err := childCore.Reset(runCtx, next); err != nil {
			return
		}
		childHandler := syscalls(runCtx, child, childSpace, f.plan, childCore)
		childHandler.Core = childCore
		deliverer := &signalDeliverer{space: childSpace, core: childCore}
		if delivery, ok := child.Control.(proc.SignalDeliveryControl); ok {
			deliverer.control = delivery
		}
		runner, err := common.NewRunner(common.Config{
			Core:       childCore,
			Dispatcher: &linuxriscv64.Adapter{Handler: childHandler, Registers: childCore},
			Boundary:   deliverer.boundary,
		})
		if err != nil {
			return
		}
		code, runErr := runner.Run(runCtx)
		if runErr != nil {
			// 子进程执行故障:按 fail-closed 以 125 退出并转僵尸。
			code = 125
		}
		_ = child.Control.Finish(f.parent.ID, child.ID, uint8(code))
	}()
	return uint64(child.ID), nil
}
