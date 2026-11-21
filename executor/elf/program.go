package elf

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/common"
	linuxriscv64 "github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/riscv64"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu/riscv64"
	loaderelf "github.com/Cosmic-Developers-Union/assistant-sandbox/loader/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/trace"
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
	loaded, err := loadProgram(ctx, process, p.path, argv(process, p.path), environment(process))
	if err != nil {
		return 0, err
	}
	plan := loaded.main

	if err := chargeMemory(ctx, process, plan); err != nil {
		return 0, err
	}
	chargeSegments(ctx, process, loaded.interpreterSegments())
	space, err := addressSpace(ctx, plan)
	if err != nil {
		return 0, err
	}
	if err := populateSegments(ctx, space, loaded.interpreterSegments()); err != nil {
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
	// 初始机器状态:动态时入口是解释器(AT_BASE/AT_ENTRY/AT_PHDR
	// 由装载器写入主计划初始栈,F06);静态时是主程序入口。
	initial := cpu.Machine{PC: loaded.Entry(), Registers: make([]uint64, 32), Floats: make([]uint64, 32)}
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
	if dispositions, ok := process.Control.(proc.SignalDispositionControl); ok {
		deliverer.disposition = dispositions
	}
	// execve 经同一 handler 原地替换映像(H06/H07):handler 以指针
	// 共享,替换后 Heap/映像布局对运行中的分派器可见。
	handler.Execs = &processExec{process: process, space: space, core: core, handler: &handler}
	config := common.Config{
		Core: core,
		// 同步 CPU 故障(非法指令/无效访问/保护错误)经 faultDispatching
		// 转换为客体信号(J06);ecall 仍由适配器分派系统调用。
		Dispatcher: faultDispatching{next: &linuxriscv64.Adapter{Handler: &handler, Registers: core}, deliver: deliverer},
		Boundary:   deliverer.boundary,
	}
	// H02/H03:执行权排队。时间片用尽与进入(可能阻塞的)系统调用
	// 时让出执行权(单核轮转);退出归还。
	if scheduling, ok := process.Control.(proc.ExecutionScheduling); ok {
		if err := scheduling.Enter(ctx); err != nil {
			return 0, err
		}
		defer func() { _ = scheduling.Leave() }()
		config.Switcher = &sliceSwitcher{sched: scheduling, remaining: instructionsPerSlice}
		config.Dispatcher = schedulingDispatch{next: config.Dispatcher, sched: scheduling}
	}
	runner, err := common.NewRunner(config)
	if err != nil {
		return 0, err
	}
	status, err := runner.Run(ctx)
	if err != nil {
		// F12/T02:执行期故障诊断携带客体路径与故障时的 CPU 快照。
		var fault common.Fault
		if errors.As(err, &fault) {
			wrapped := errno.Wrap(err, "客体程序 "+p.path)
			return 0, &executable.DiagnosticError{Err: wrapped, Diagnostic: *captureDiagnostic(ctx, core, fault.Trap.Error())}
		}
		return 0, err
	}
	// K03:运行流终止,落实 clear_child_tid——登记地址写零并唤醒
	// futex 等待者(线程化后随 K02 移至每线程退出路径)。
	if tidControl, ok := process.Control.(proc.TidAddressControl); ok {
		tidControl.ClearTidAtExit(ctx, func(ctx context.Context, address uint64, content []byte) error {
			return space.Write(ctx, address, content)
		})
	}
	if robust, ok := process.Control.(proc.RobustExitControl); ok {
		_ = robust.HandleRobustListAtExit(ctx, space)
	}
	// exit(2) 的状态按 Linux 截断为低 8 位(WEXITSTATUS);负数 errno
	// 形态(如 openat 失败后的 -2)与参考 Linux 一致地成为 254,
	// 而不是宿主侧错误(A09 差分发现)。
	return uint8(status), nil
}

func readImage(ctx context.Context, process proc.Proc, path string) (content []byte, resultErr error) {
	if process.Resources == nil {

		return nil, errno.Wrap(errno.NotImplemented, "进程没有绑定资源能力")
	}
	system, err := process.Resources.Filesystem()
	if err != nil {
		return nil, err
	}
	file, err := system.Open(ctx, path, 0, 0)
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
	total := segmentsBytes(plan.Segments) + plan.Stack.Region.Size()
	if total == 0 {
		return nil
	}
	return meters.ChargeKind(ctx, uint64(process.ID), resources.KindMemory, total)
}

// chargeSegments 按段区间为动态解释器补充内存计量(F06)。
func chargeSegments(ctx context.Context, process proc.Proc, segments []loaderelf.Segment) {
	meters := metersOf(process)
	if meters == nil || len(segments) == 0 {
		return
	}
	_ = meters.ChargeKind(ctx, uint64(process.ID), resources.KindMemory, segmentsBytes(segments))
}

func segmentsBytes(segments []loaderelf.Segment) uint64 {
	var total uint64
	for _, segment := range segments {
		total += segment.Region.End - segment.Region.Start
	}
	return total
}

// populateSegments 把既有段集(动态解释器)装入地址空间,不携带
// 独立栈(解释器沿用主计划的初始栈)。
func populateSegments(ctx context.Context, space *memory.Table, segments []loaderelf.Segment) error {
	for _, segment := range segments {
		populate := segment.Region
		populate.Prot = memory.ProtRead | memory.ProtWrite
		if err := space.Map(ctx, populate); err != nil {
			return err
		}
		if err := space.Write(ctx, segment.Address, segment.Data); err != nil {
			return err
		}
		if segment.Region.Prot != populate.Prot {
			if err := space.Protect(ctx, segment.Region.Start, segment.Region.End, segment.Region.Prot); err != nil {
				return err
			}
		}
	}
	return nil
}

func addressSpace(ctx context.Context, plan *loaderelf.Plan) (*memory.Table, error) {
	space := memory.New(0)
	if err := populateSpace(ctx, space, plan); err != nil {
		return nil, err
	}
	return space, nil
}

// stackGuardBytes 是栈低端的保护页大小:栈溢出触达保护页时报
// 保护错误(SEGV/ACCERR),不会写入相邻映射(E13)。
const stackGuardBytes = memory.PageSize

// signalTrampolineAddress 是信号返回蹭径页(4GiB 下沿,匿名映射自
// 4GiB 向上分配,永不重叠):内容为 rt_sigreturn 的两指令序列。
// riscv64 musl 不带 SA_RESTORER,真实内核把返回地址指向 vDSO 的
// sigreturn 蹭径;本 VM 以固定页提供等价物,信号投递时写入 ra。
const signalTrampolineAddress = uint64(1)<<32 - memory.PageSize

// interpreterBase 是动态解释器的固定装载槽位:与主程序段(低位)、
// mmap 区(4GiB 起)、堆与栈(高位)均不重叠(F06)。真实 ld.so
// 接入后由顶层分配器替代。
const interpreterBase = uint64(0x40) << 24

// loadedProgram 汇总一次装载的产物:主计划(含初始栈与 auxv)与
// 可选的动态解释器计划(F05/F06)。
type loadedProgram struct {
	main   *loaderelf.Plan
	interp *loaderelf.Plan
}

// Entry 返回初始执行入口:有解释器时为解释器入口(ld.so 先行)。
func (l *loadedProgram) Entry() uint64 {
	if l.interp != nil {
		return l.interp.Entry
	}
	return l.main.Entry
}

// interpreterSegments 返回解释器的可装载段;静态程序返回空。
func (l *loadedProgram) interpreterSegments() []loaderelf.Segment {
	if l.interp != nil {
		return l.interp.Segments
	}
	return nil
}

// loadProgram 从客体文件系统装载 path 的映像;PT_INTERP 指向的
// 动态解释器同样从客体文件系统装载(F05),主计划初始栈的 AT_BASE
// 记录解释器基址(F06)。路径不可达宿主,缺失报明确加载错误。
// pieBase 是 ET_DYN 主程序(PIE)的确定性装载基址(F04):与动态
// 解释器槽位(interpreterBase=0x40000000)错开,映像上限(64MB)加
// 基址仍低于解释器;同一程序在任何次启动中布局一致,差分可重放。
const pieBase = uint64(0x20000000)

func loadProgram(ctx context.Context, process proc.Proc, path string, argv, envp []string) (*loadedProgram, error) {
	content, err := readImage(ctx, process, path)
	if err != nil {
		return nil, errno.Wrap(err, "读取客体程序 "+path)
	}
	options := loaderelf.Options{
		Argv:     argv,
		Env:      envp,
		ExecPath: path,
		Random:   randomBytesFor(ctx, process),
		// ET_DYN 主程序按 PIE 基址装载;ET_EXEC 忽略 bias(装载器
		// 对可执行映像的非零 bias 报 EINVAL,故仅对动态形态传递)。
		LoadBias: pieBase,
	}
	if !isDynamicImage(content) {
		options.LoadBias = 0
	}
	plan, err := loaderelf.Load(content, options)
	if err != nil {
		return nil, errno.Wrap(err, "装载客体程序 "+path)
	}
	out := &loadedProgram{main: plan}
	if plan.InterpreterPath == "" {
		return out, nil
	}
	interpContent, err := readImage(ctx, process, plan.InterpreterPath)
	if err != nil {
		// 携带客体路径与阶段:F05/F12 要求缺失/不可读时报告明确加载错误。
		return nil, errno.Wrap(err, "读取动态解释器 "+plan.InterpreterPath+"(主程序 "+path+")")
	}
	interpPlan, err := loaderelf.Load(interpContent, loaderelf.Options{
		Argv:     argv,
		Env:      envp,
		ExecPath: path,
		Random:   randomBytesFor(ctx, process),
		LoadBias: interpreterBase,
	})
	if err != nil {
		return nil, errno.Wrap(err, "装载动态解释器 "+plan.InterpreterPath)
	}
	if overlaps(plan.Segments, interpPlan.Segments) {
		return nil, errno.Wrap(errno.NoExec, "解释器 "+plan.InterpreterPath+" 装载槽位与主程序 "+path+" 映射重叠")
	}
	// 主计划初始栈需携带解释器基址:以 AT_BASE 重新构建。
	plan, err = loaderelf.Load(content, loaderelf.Options{
		Argv:            argv,
		Env:             envp,
		ExecPath:        path,
		Random:          randomBytesFor(ctx, process),
		InterpreterBase: interpPlan.Bias,
	})
	if err != nil {
		return nil, errno.Wrap(err, "重建主程序初始栈 "+path)
	}
	out.main = plan
	out.interp = interpPlan
	return out, nil
}

func overlaps(left, right []loaderelf.Segment) bool {
	for _, a := range left {
		for _, b := range right {
			if a.Region.Overlaps(b.Region) {
				return true
			}
		}
	}
	return false
}

// populateSpace 把映像段、栈与栈保护页装入既有地址空间;execve
// 原地替换复用同一装载逻辑(H07)。
func populateSpace(ctx context.Context, space *memory.Table, plan *loaderelf.Plan) error {
	// 信号返回蹭径页:两指令序列 li a7,139; ecall。装载以 RW 写入
	// 后切 RX(装载不是客体写访问,与段填充同纪律)。
	_trampoline := []uint32{0x08B00893, 0x00000073} // li a7, 139(rt_sigreturn); ecall
	_trampolineBytes := make([]byte, memory.PageSize)
	for i, instruction := range _trampoline {
		binary.LittleEndian.PutUint32(_trampolineBytes[i*4:], instruction)
	}
	_trampolineRegion := memory.Region{
		Start: signalTrampolineAddress,
		End:   signalTrampolineAddress + memory.PageSize,
		Prot:  memory.ProtRead | memory.ProtWrite,
		Name:  "signal-trampoline",
	}
	if err := space.Map(ctx, _trampolineRegion); err != nil {
		return err
	}
	if err := space.Write(ctx, signalTrampolineAddress, _trampolineBytes); err != nil {
		return err
	}
	if err := space.Protect(ctx, signalTrampolineAddress, signalTrampolineAddress+memory.PageSize, memory.ProtRead|memory.ProtExec); err != nil {
		return err
	}
	guard := memory.Region{
		Start: plan.Stack.Region.Start - stackGuardBytes,
		End:   plan.Stack.Region.Start,
		Prot:  memory.Prot(0), // 无任何访问权限:任何访问都报保护错误
		Name:  "stack-guard",
	}
	if err := space.Map(ctx, guard); err != nil {
		return err
	}
	for _, segment := range plan.Segments {
		populate := segment.Region
		populate.Prot = memory.ProtRead | memory.ProtWrite
		if err := space.Map(ctx, populate); err != nil {
			return err
		}
		if err := space.Write(ctx, segment.Address, segment.Data); err != nil {
			return err
		}
		if segment.Region.Prot != populate.Prot {
			if err := space.Protect(ctx, segment.Region.Start, segment.Region.End, segment.Region.Prot); err != nil {
				return err
			}
		}
	}
	if err := space.Map(ctx, plan.Stack.Region); err != nil {
		return err
	}
	if err := space.Write(ctx, plan.Stack.ContentBase, plan.Stack.Content); err != nil {
		return err
	}
	return nil
}

func syscalls(ctx context.Context, process proc.Proc, space *memory.Table, plan *loaderelf.Plan, core cpu.Port) common.Syscalls {
	handler := common.Syscalls{Memory: space, Sysname: "Linux", Nodename: "assistant-sandbox", Release: "6.8.0", Version: "#1", Machine: "riscv64", Domainname: "(none)", Cwd: process.Dir}
	identity := &common.Identity{PID: uint64(process.ID), PPID: uint64(process.Parent), TID: uint64(process.TID), TGID: uint64(process.TGID)}
	handler.Identity = identity
	if process.Resources == nil {

		return handler
	}
	// getrandom 的熵源(L04):与 loader 随机字节同源,生产模式不固定。
	if random, err := process.Resources.Random(); err == nil {
		handler.Random = random
	}
	if descriptors, err := process.Resources.Descriptors(); err == nil {
		handler.Descriptors = common.ProcessDescriptors{Table: descriptors, PID: uint64(process.ID)}
		// timerfd 的描述符端口(L10):创建/武装/查询经描述符表。
		handler.TimerSource = common.ProcessDescriptors{Table: descriptors, PID: uint64(process.ID)}

		if installer, ok := descriptors.(fd.Installer); ok {
			if system, err := process.Resources.Filesystem(); err == nil {
				// I13/O01:按进程身份包装文件系统——权限按
				// owner/group/other 判定,新建节点归属该进程身份。
				if credentials, credErr := process.Resources.Credentials(); credErr == nil {
					if current, lookupErr := credentials.Lookup(ctx, uint64(process.ID)); lookupErr == nil {
						if accessor, ok := system.(vmfs.AccessorFS); ok {
							if scoped, accessErr := accessor.WithAccessor(current.UID, current.GID); accessErr == nil {
								system = scoped
							}
						}
					}
				}
				handler.Files = common.ProcessFiles{
					System: system,
					Store:  installer,
					PID:    uint64(process.ID),
					Dir:    process.Dir,
				}
			}
		}
	}

	// 每空间装载状态(K02):同组线程共享 brk/mmap 游标;首个装载
	// 创建,后续装载复用。
	if runtime := spaceRuntimeOf(space, process, plan); runtime != nil {
		handler.Heap = runtime.heap
		handler.Mapper = runtime.mapper
	}
	handler.Limiter = &limiter{meters: metersOf(process), pid: uint64(process.ID)}
	if waiter, ok := process.Control.(proc.WaitControl); ok {
		handler.Waiter = processWaiter{control: waiter}
	}
	if dispositions, ok := process.Control.(proc.SignalDispositionControl); ok {
		handler.Signals = processSignals{control: dispositions}
	}
	if delivery, ok := process.Control.(proc.SignalDeliveryControl); ok {
		// 阻塞系统调用经此感知待投递信号(EINTR/SA_RESTART,J09)。
		handler.Interrupts = processInterruptor{control: delivery}
	}
	if killer, ok := process.Control.(proc.SignalKillControl); ok {
		handler.Kills = processKiller{control: killer}
	}
	if robust, ok := process.Control.(proc.RobustListControl); ok {
		handler.RobustLists = processRobustList{control: robust}
	}
	if lockTable, ok := process.Control.(proc.LocksControl); ok {
		handler.Locks = lockTable.Locks()
	}
	if notify, ok := process.Control.(proc.NotifyControl); ok {
		// inotify 事件扇出(I20):文件类系统调用成功后按路径发事件。
		handler.Notify = notify.NotifyBroker()
	}
	if netControl, ok := process.Control.(proc.NetControl); ok {
		// socket 系统调用端口(M01-M05):VM 独立命名空间 + 描述符表。
		if descriptors, err := process.Resources.Descriptors(); err == nil {
			if installer, ok := descriptors.(fd.Installer); ok {
				handler.Sockets = processSockets{
					ns:        netControl.NetNamespace(),
					installer: installer,
					descriptors: func(ctx context.Context, fd uint64) (any, error) {
						return descriptorsHandle{table: descriptors, pid: uint64(process.ID)}.Get(ctx, fd)
					},
					pid: uint64(process.ID),
				}
			}
		}
	}
	if sessions, ok := process.Control.(proc.SessionControl); ok {
		handler.Sessions = processSessions{control: sessions}
	}
	if futexes, ok := process.Control.(proc.FutexControl); ok {
		// futex WAIT/WAKE 的同步表(K05):按 {进程,客体地址} 键控。
		handler.Futexes = futexes.FutexTable()
	}
	if tidControl, ok := process.Control.(proc.TidAddressControl); ok {
		// set_tid_address 的登记端口(K03)。
		handler.TidAddresses = tidControl
	}
	if recorder, ok := process.Control.(proc.TraceRecorder); ok {
		// 受限系统调用追踪(G08):只记录标识、调用编号、结果与耗时。
		handler.Tracer = processTrace{control: recorder}
	}
	if forker, ok := process.Control.(proc.ForkControl); ok {
		spawner := processForker{
			parent: process, space: space, plan: plan, core: core,
			control: forker,
		}
		if finisher, ok := process.Control.(proc.FinishCurrentControl); ok {
			spawner.finisher = finisher
		}
		handler.Spawner = spawner
	}
	if vforkControl, ok := process.Control.(proc.VforkControl); ok {
		vforker := processVforker{
			parent: process, space: space, plan: plan, core: core,
			control: vforkControl,
		}
		if finisher, ok := process.Control.(proc.FinishCurrentControl); ok {
			vforker.finisher = finisher
		}
		if rebind, ok := process.Control.(proc.SpaceRebindControl); ok {
			vforker.rebind = rebind
		}
		handler.Vforker = vforker
	}
	if threader, ok := process.Control.(proc.ThreadControl); ok {
		handler.Threader = processThreader{
			parent: process, space: space, plan: plan, core: core,
			control: threader,
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
	mu sync.Mutex

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
	// 堆增长止于栈保护页低端,不得与保护页重叠(E13)。
	limit := plan.Stack.Region.Start - stackGuardBytes
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
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return h.brk, nil
}

func (h *heap) Grow(ctx context.Context, target uint64) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
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

// processRobustList 适配 robust futex 登记端口(K07)。
type processRobustList struct {
	control proc.RobustListControl
}

func (p processRobustList) SetRobustList(head, length uint64) error {
	return p.control.SetRobustList(head, length)
}

func (p processRobustList) RobustList(head, length *uint64) error {
	return p.control.RobustList(head, length)
}

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

// processInterruptor 把进程控制端口的待投递查询适配为阻塞系统
// 调用的信号打断端口(EINTR/SA_RESTART,J09)。
type processInterruptor struct {
	control proc.SignalDeliveryControl
}

func (p processInterruptor) DeliverableSignal() (uint64, common.SignalAction, bool) {
	number, action, ok := p.control.PendingDeliverable()
	if !ok {
		return 0, common.SignalAction{}, false
	}
	return uint64(number), common.SignalAction{
		Handler: action.Handler, Flags: action.Flags, Restorer: action.Restorer, Mask: action.Mask,
	}, true
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
	case 0:
		return p.control.SetSignalMask(signal.MaskBlock, set)
	case 1:
		return p.control.SetSignalMask(signal.MaskUnblock, set)
	case 2:
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
// rt_sigreturn 经同一帧恢复(J03/J04);同步 CPU 故障经同一
// 帧机制转换为客体信号(J06)。
type signalDeliverer struct {
	space       *memory.Table
	core        cpu.Port
	control     proc.SignalDeliveryControl
	disposition proc.SignalDispositionControl
}

// captureDiagnostic 在故障点抓取客体 CPU 快照(T02):PC、整数/浮点
// 寄存器与 FCSR。即使上下文已取消也尽量读取,便于崩溃报告。
func captureDiagnostic(ctx context.Context, core cpu.Port, trap string) *executable.Diagnostic {
	diagnostic := &executable.Diagnostic{Trap: trap}
	machine, err := core.State(context.WithoutCancel(ctx))
	if err != nil {
		return diagnostic
	}
	diagnostic.PC = machine.PC
	diagnostic.Registers = append([]uint64(nil), machine.Registers...)
	diagnostic.Floats = append([]uint64(nil), machine.Floats...)
	diagnostic.FCSR = machine.FCSR
	return diagnostic
}

// errUnhandledFault 标记不可转换为客体信号的同步故障(内核未实现
// 指令等缺口):保持 fail-closed,不以信号投递掩盖内核缺口。
var errUnhandledFault = errors.New("未转换的客体故障")

// faultSignal 把同步 CPU 故障映射为客体信号与 siginfo 类别/地址。
// 取指故障未记录指令编码:si_addr 即取指地址;数据故障以 Detail 为准
// (空指针解引用的故障地址 0 不得被取指地址顶替)。
func faultSignal(trap cpu.Trap) (signal.Number, common.FaultInfo, bool) {
	switch trap.Kind {
	case cpu.TrapIllegalInstruction:
		return signal.ILL, common.FaultInfo{Code: common.SigInfoIllIllegalOp, Addr: trap.Address}, true
	case cpu.TrapBreakpoint:
		return signal.TRAP, common.FaultInfo{Code: common.SigInfoTrapBreak, Addr: trap.Address}, true
	case cpu.TrapMemoryFault:
		address := trap.Detail
		if trap.Instruction == 0 {
			address = trap.Address
		}
		switch {
		case errors.Is(trap.Cause, memory.FaultMisaligned):
			return signal.BUS, common.FaultInfo{Code: common.SigInfoBusAdraln, Addr: address}, true
		case errors.Is(trap.Cause, memory.FaultProtection):
			return signal.SEGV, common.FaultInfo{Code: common.SigInfoSegvAccErr, Addr: address}, true
		default:
			return signal.SEGV, common.FaultInfo{Code: common.SigInfoSegvMapErr, Addr: address}, true
		}
	default:
		return 0, common.FaultInfo{}, false
	}
}

// synchronous 在同步故障点投递客体信号:登记了处理器的信号在故障
// 指令处开帧(处理器返回后经 rt_sigreturn 重新执行故障指令,与
// Linux 一致);SIG_IGN 丢弃并重新执行;默认终止/核心转储的信号
// 返回 Death 由执行层归类为被信号终止。
func (d *signalDeliverer) synchronous(ctx context.Context, trap cpu.Trap) error {
	number, info, convertible := faultSignal(trap)
	if !convertible || d.control == nil || d.disposition == nil {
		return errUnhandledFault
	}
	config, err := d.disposition.SignalAction(number)
	if err != nil {
		return err
	}
	if config.Handler > signal.SigIgnore {
		return d.enterHandler(ctx, number, config, trap.Address, &info)
	}
	if config.Handler == signal.SigIgnore {
		return nil
	}
	action, err := signal.Default(number)
	if err != nil {
		return err
	}
	switch action {
	case signal.Terminate, signal.Core:
		return executable.Death{Signal: number,
			Detail:     fmt.Sprintf("%v si_code=%d si_addr=%#x", trap, info.Code, info.Addr),
			Diagnostic: captureDiagnostic(ctx, d.core, trap.Error())}
	case signal.Ignore:
		return nil
	default:
		return errUnhandledFault
	}
}

// enterHandler 保存机器状态并跳转到处理器:fault 非空时 siginfo
// 携带同步故障类别与地址;savedPC 是处理器返回后恢复的指令地址
// (边界投递为待执行指令,故障投递为触发故障的指令)。
func (d *signalDeliverer) enterHandler(ctx context.Context, number signal.Number, action signal.ActionConfig, savedPC uint64, fault *common.FaultInfo) error {
	machine, err := d.core.State(ctx)
	if err != nil {
		return err
	}
	machine.PC = savedPC
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
		Fault:     fault,
	})
	if err != nil {
		return err
	}
	next := machine
	next.PC = action.Handler
	next.Registers[regSp] = base
	next.Registers[regArgument0] = uint64(number)
	// 处理器返回地址:SA_RESTORER 形态用登记的 restorer;否则内核
	// 提供蹭径(真实内核为 vDSO,riscv64 musl 依赖此路径)。
	if action.Flags&uint32(common.SigactionRestorer) != 0 && action.Restorer != 0 {
		next.Registers[regRA] = action.Restorer
	} else {
		next.Registers[regRA] = signalTrampolineAddress
	}
	if action.Flags&uint32(common.SigactionSigInfo) != 0 {
		next.Registers[regArgument0+1] = infoAddress
		next.Registers[regArgument0+2] = ucontextAddress
	}
	return d.core.Reset(ctx, next)
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
	if err := d.enterHandler(ctx, number, action, machine.PC, nil); err != nil {
		return err
	}
	d.control.ConsumeSignal(number)
	return nil
}

// faultDispatching 包装系统调用适配器:非 ecall 的同步故障经
// signalDeliverer 转换为客体信号投递;未转换的按原样上抛
// (Runner 视为致命故障,fail-closed)。
type faultDispatching struct {
	next    common.Dispatcher
	deliver *signalDeliverer
}

func (f faultDispatching) Dispatch(ctx context.Context, trap cpu.Trap) (common.Exit, error) {
	exit, err := f.next.Dispatch(ctx, trap)
	if err == nil {
		return exit, nil
	}
	var fault common.Fault
	if !errors.As(err, &fault) {
		return exit, err
	}
	raised := f.deliver.synchronous(ctx, fault.Trap)
	switch {
	case raised == nil:
		return common.Exit{}, nil
	case errors.Is(raised, errUnhandledFault):
		return common.Exit{}, err
	default:
		return common.Exit{}, raised
	}
}

const (
	regSp        = 2
	regRA        = 1
	regArgument0 = 10
)

// processForker 把进程控制端口的 fork 能力适配为 clone 系统调用:
// 复制地址空间(E10)后,子进程在自己的核心上从调用边界续接执行,
// 退出时按 Linux 语义转僵尸并唤醒父进程等待。
type processForker struct {
	parent   proc.Proc
	space    *memory.Table
	plan     *loaderelf.Plan
	core     cpu.Port
	control  proc.ForkControl
	finisher proc.FinishCurrentControl
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
		if dispositions, ok := child.Control.(proc.SignalDispositionControl); ok {
			deliverer.disposition = dispositions
		}
		childHandler.Execs = &processExec{process: child, space: childSpace, core: childCore, handler: &childHandler}
		childConfig := common.Config{
			Core:       childCore,
			Dispatcher: faultDispatching{next: &linuxriscv64.Adapter{Handler: &childHandler, Registers: childCore}, deliver: deliverer},
			Boundary:   deliverer.boundary,
		}
		// 子进程同样接入执行权排队(H02/H03)。
		childScheduling, hasScheduling := child.Control.(proc.ExecutionScheduling)
		if hasScheduling {
			if err := childScheduling.Enter(runCtx); err != nil {
				_ = child.Control.Finish(f.parent.ID, child.ID, 125)
				return
			}
			defer func() { _ = childScheduling.Leave() }()
			childConfig.Switcher = &sliceSwitcher{sched: childScheduling, remaining: instructionsPerSlice}
			childConfig.Dispatcher = schedulingDispatch{next: childConfig.Dispatcher, sched: childScheduling}
		}
		runner, err := common.NewRunner(childConfig)
		if err != nil {
			return
		}
		code, runErr := runner.Run(runCtx)
		if runErr != nil {
			// 子进程执行故障:按 fail-closed 以 125 退出并转僵尸。
			code = 125
		}
		// H10 语义:僵尸由父进程 wait* 回收;控制端口无
		// FinishCurrentControl 时退回按登记父进程写终态。
		if f.finisher != nil {
			_ = f.finisher.FinishCurrent(child.ID, uint8(code))
		} else {
			_ = f.parent.Control.Finish(f.parent.ID, child.ID, uint8(code))
		}
	}()
	return uint64(child.ID), nil
}

// spaceRuntime 是同组线程共享的每地址空间装载状态(K02):brk 与
// mmap 游标属于地址空间而非线程,经 memory.Table.Annotation 挂载;
// 计费归属创建它的组长(线程 mmap 共享同一地址空间)。execve 原地
// 替换时整体重建(H07)。
type spaceRuntime struct {
	heap   *heap
	mapper *mapper
}

func spaceRuntimeOf(space *memory.Table, process proc.Proc, plan *loaderelf.Plan) *spaceRuntime {
	if space == nil {
		return nil
	}
	if rt, ok := space.Annotation().(*spaceRuntime); ok {
		return rt
	}
	rt := &spaceRuntime{heap: newHeap(space, process, plan), mapper: newMapper(space, process, plan)}
	space.SetAnnotation(rt)
	return rt
}

// processThreader 把 clone 线程形态适配为同组线程创建(K02):子线程
// 与调用方共享地址空间与每空间装载状态,内核完成登记与共享接线,
// 执行续接为同一 PC、调用方指定的栈与线程指针、返回值 0。
type processThreader struct {
	parent  proc.Proc
	space   *memory.Table
	plan    *loaderelf.Plan
	core    cpu.Port
	control proc.ThreadControl
}

func (t processThreader) CloneThread(ctx context.Context, request common.CloneRequest) (uint64, error) {
	child, space, err := t.control.CloneThread(request.ClearTID)
	if err != nil {
		return 0, err
	}
	// 续接边界:调用时刻的完整寄存器与 ecall 之后的 PC;子线程
	// 返回值为 0,栈与线程指针按 clone 参数。
	state, err := t.core.State(ctx)
	if err != nil {
		return 0, err
	}
	runtime := spaceRuntimeOf(space, child, t.plan)
	if request.ParentTIDPtr != 0 {
		var value [8]byte
		binary.LittleEndian.PutUint64(value[:], uint64(child.ID))
		if err := space.Write(ctx, request.ParentTIDPtr, value[:]); err != nil {
			_ = t.control.FinishThread(child.ID, 125, 0)
			return 0, err
		}
	}
	if request.ChildTIDPtr != 0 {
		var value [8]byte
		binary.LittleEndian.PutUint64(value[:], uint64(child.ID))
		if err := space.Write(ctx, request.ChildTIDPtr, value[:]); err != nil {
			_ = t.control.FinishThread(child.ID, 125, 0)
			return 0, err
		}
	}
	go func() {
		runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
		defer cancel(nil)
		// K08:线程接线的信号中断钩子——signals.Interrupt(tid, n)
		// 取消运行上下文,fatal 信号由此终结整组。
		var interrupted signal.Number
		if binder, ok := child.Control.(proc.ThreadInterruptControl); ok {
			binder.BindThreadInterrupt(child.ID, func(n signal.Number) {
				interrupted = n
				cancel(errno.Wrap(errno.Interrupted, "信号中断"))
			})
		}
		childCore, err := riscv64.New(cpu.Config{
			Memory: space,
			Entry:  state.PC,
			Meter:  meter{ctx: runCtx, meters: metersOf(child), pid: uint64(child.ID)},
		})
		if err != nil {
			_ = t.control.FinishThread(child.ID, 125, 0)
			return
		}
		next := state
		next.Registers[regArgument0] = 0
		if request.Stack != 0 {
			next.Registers[regSp] = request.Stack
		}
		if request.SetTLS {
			next.Registers[4] = request.TLS // tp:x4(K04)
		}
		if err := childCore.Reset(runCtx, next); err != nil {
			_ = t.control.FinishThread(child.ID, 125, 0)
			return
		}
		childHandler := syscalls(runCtx, child, space, t.plan, childCore)
		// 共享每空间装载状态:brk/mmap 游标与计费归属组长(K02)。
		if runtime != nil {
			childHandler.Heap = runtime.heap
			childHandler.Mapper = runtime.mapper
		}
		childHandler.Core = childCore
		deliverer := &signalDeliverer{space: space, core: childCore}
		if delivery, ok := child.Control.(proc.SignalDeliveryControl); ok {
			deliverer.control = delivery
		}
		if dispositions, ok := child.Control.(proc.SignalDispositionControl); ok {
			deliverer.disposition = dispositions
		}
		childHandler.Execs = &processExec{process: child, space: space, core: childCore, handler: &childHandler}
		childConfig := common.Config{
			Core:       childCore,
			Dispatcher: faultDispatching{next: &linuxriscv64.Adapter{Handler: &childHandler, Registers: childCore}, deliver: deliverer},
			Boundary:   deliverer.boundary,
		}
		childScheduling, hasScheduling := child.Control.(proc.ExecutionScheduling)
		if hasScheduling {
			if err := childScheduling.Enter(runCtx); err != nil {
				_ = t.control.FinishThread(child.ID, 125, 0)
				return
			}
			defer func() { _ = childScheduling.Leave() }()
			childConfig.Switcher = &sliceSwitcher{sched: childScheduling, remaining: instructionsPerSlice}
			childConfig.Dispatcher = schedulingDispatch{next: childConfig.Dispatcher, sched: childScheduling}
		}
		runner, err := common.NewRunner(childConfig)
		if err != nil {
			_ = t.control.FinishThread(child.ID, 125, 0)
			return
		}
		code, runErr := runner.Run(runCtx)
		if runErr != nil && interrupted.Valid() {
			// 致命信号:整组结束(K08),退出状态为信号。
			_ = t.control.FinishThread(child.ID, uint8(code), uint8(interrupted))
			return
		}
		if runErr != nil {
			code = 125
		}
		// K03:线程退出路径落实 clear_child_tid——登记地址写零并
		// 唤醒 futex 等待者(pthread_join 依赖)。
		if tidControl, ok := child.Control.(proc.TidAddressControl); ok {
			tidControl.ClearTidAtExit(runCtx, func(ctx context.Context, address uint64, content []byte) error {
				return space.Write(ctx, address, content)
			})
		}
		// K07:robust list 清算——持锁死亡的 futex 标记 OWNER_DIED
		// 并唤醒等待者(EOWNERDIED 语义)。
		if robust, ok := child.Control.(proc.RobustExitControl); ok {
			_ = robust.HandleRobustListAtExit(runCtx, space)
		}
		// H08:exit_group 结束整组;exit 仅结束当前线程。致命信号
		// 中断由内核在 Signal 投递时升级为整组结束(K08)。
		if childHandler.LastExitGroup {
			_ = t.control.FinishGroup(child.ID, uint8(code), 0)
			return
		}
		_ = t.control.FinishThread(child.ID, uint8(code), 0)
	}()
	return uint64(child.ID), nil
}

// processVforker 把 clone 的 vfork 形态(H12)适配为共享地址空间的
// 子进程创建:子进程与调用方共享同一地址空间续接执行(栈按请求,
// 缺省沿用调用方栈),父进程在本系统调用内暂停,直到子进程 exec
// (换绑新地址空间)或退出——vfork 的完成语义。共享状态(地址空间、
// 每空间装载游标)由暂停保证串行,exec 后子进程与父进程各自独立。
type processVforker struct {
	parent   proc.Proc
	space    *memory.Table
	plan     *loaderelf.Plan
	core     cpu.Port
	control  proc.VforkControl
	finisher proc.FinishCurrentControl
	rebind   proc.SpaceRebindControl
}

func (f processVforker) Vfork(ctx context.Context, request common.VforkRequest) (uint64, error) {
	child, space, err := f.control.Vfork()
	if err != nil {
		return 0, err
	}
	// 续接边界:调用时刻的完整寄存器与 ecall 之后的 PC;子进程
	// vfork 返回值为 0(Linux 语义)。
	state, err := f.core.State(ctx)
	if err != nil {
		return 0, err
	}
	binding := &vforkBinding{
		process: child,
		space:   space,
		plan:    f.plan,
		control: f.rebind,
		done:    make(chan struct{}),
	}
	go func() {
		// 子进程生命周期独立于调用方上下文;父进程在 vfork 调用内
		// 暂停,完成(exec/退出)后恢复。
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		childCore, err := riscv64.New(cpu.Config{
			Memory: space,
			Entry:  state.PC,
			Meter:  meter{ctx: runCtx, meters: metersOf(child), pid: uint64(child.ID)},
		})
		if err != nil {
			binding.release()
			_ = f.finisher.FinishCurrent(child.ID, 125)
			return
		}
		next := state
		next.Registers[regArgument0] = 0
		if request.Stack != 0 {
			next.Registers[regSp] = request.Stack
		}
		if err := childCore.Reset(runCtx, next); err != nil {
			binding.release()
			_ = f.finisher.FinishCurrent(child.ID, 125)
			return
		}
		childHandler := syscalls(runCtx, child, space, f.plan, childCore)
		// 共享每空间装载状态:brk/mmap 游标与计费归属空间持有者
		// (K02 同一语义)。
		if runtime := spaceRuntimeOf(space, child, f.plan); runtime != nil {
			childHandler.Heap = runtime.heap
			childHandler.Mapper = runtime.mapper
		}
		childHandler.Core = childCore
		deliverer := &signalDeliverer{space: space, core: childCore}
		if delivery, ok := child.Control.(proc.SignalDeliveryControl); ok {
			deliverer.control = delivery
		}
		if dispositions, ok := child.Control.(proc.SignalDispositionControl); ok {
			deliverer.disposition = dispositions
		}
		binding.handler = &childHandler
		binding.core = childCore
		binding.deliverer = deliverer
		childHandler.Execs = &processExec{process: child, space: space, core: childCore, handler: &childHandler, vf: binding}
		childConfig := common.Config{
			Core:       childCore,
			Dispatcher: faultDispatching{next: &linuxriscv64.Adapter{Handler: &childHandler, Registers: childCore}, deliver: deliverer},
			Boundary:   deliverer.boundary,
		}
		// 子进程同样接入执行权排队(H02/H03)。
		childScheduling, hasScheduling := child.Control.(proc.ExecutionScheduling)
		if hasScheduling {
			if err := childScheduling.Enter(runCtx); err != nil {
				binding.release()
				_ = f.finisher.FinishCurrent(child.ID, 125)
				return
			}
			defer func() { _ = childScheduling.Leave() }()
			childConfig.Switcher = &sliceSwitcher{sched: childScheduling, remaining: instructionsPerSlice}
			childConfig.Dispatcher = schedulingDispatch{next: childConfig.Dispatcher, sched: childScheduling}
		}
		runner, err := common.NewRunner(childConfig)
		if err != nil {
			binding.release()
			_ = f.finisher.FinishCurrent(child.ID, 125)
			return
		}
		code, runErr := runner.Run(runCtx)
		if runErr != nil {
			// 子进程执行故障:按 fail-closed 以 125 退出并转僵尸。
			code = 125
		}
		// exec 成功路径已完成 release;此处对退出路径兜底(恰好一次)。
		binding.release()
		_ = f.finisher.FinishCurrent(child.ID, uint8(code))
	}()
	select {
	case <-binding.done:
	case <-ctx.Done():
		// 调用方上下文取消(关闭/信号):vfork 不再等待完成。
		return 0, ctx.Err()
	}
	return uint64(child.ID), nil
}

// vforkBinding 携带 vfork 子进程的可换绑执行上下文(H12):exec 时
// 换绑全新地址空间并恰好一次地释放父进程暂停。
type vforkBinding struct {
	process   proc.Proc
	space     *memory.Table
	plan      *loaderelf.Plan
	control   proc.SpaceRebindControl
	handler   *common.Syscalls
	core      cpu.Port
	deliverer *signalDeliverer

	done chan struct{}
	once sync.Once
}

// release 在子进程 exec 成功或退出时恰好触发一次:父进程恢复。
func (v *vforkBinding) release() {
	v.once.Do(func() { close(v.done) })
}

// prepare 为 exec 装载新映像准备全新空表(内核暂存待提交绑定)。
func (v *vforkBinding) prepare() (*memory.Table, error) {
	if v.control == nil {
		return nil, errno.Wrap(errno.NotSupported, "地址空间换绑端口未装配")
	}
	return v.control.PrepareSpace()
}

// switchTo 把执行上下文(核心、系统调用内存、信号帧投递、每空间
// 装载状态)切换到新表并固化内核绑定。
func (v *vforkBinding) switchTo(ctx context.Context, fresh *memory.Table) error {
	if err := v.control.CommitSpace(); err != nil {
		return err
	}
	v.space = fresh
	if v.handler != nil {
		v.handler.Memory = fresh
	}
	if attacher, ok := v.core.(cpu.Attacher); ok {
		attacher.Attach(fresh)
	}
	if v.deliverer != nil {
		v.deliverer.space = fresh
	}
	return nil
}

// processExec 把 execve 适配为当前进程的原子映像替换(H06/H07):
// 新映像(含 shebang 解析)完全装载成功后才释放旧地址空间并重置
// 核心;装载失败不做任何修改,旧程序继续执行。成功后 PID 不变,
// 信号处置/挂起/CLOEXEC 经内核控制端口复位。vfork 子进程(H12)经
// vf 换绑全新地址空间:父进程保留原空间,装载失败旧空间不受影响。
type processExec struct {
	process proc.Proc
	space   *memory.Table
	core    cpu.Port
	handler *common.Syscalls
	vf      *vforkBinding
}

// maxShebangDepth 与 Linux BINPRM_MAX_INTERP 一致:解释器链最深 4 层。
const maxShebangDepth = 4

func (p *processExec) Exec(ctx context.Context, path string, argv, envp []string) error {
	loaded, finalArgv, err := p.load(ctx, path, argv, envp, 0)
	if err != nil {
		return err
	}
	plan := loaded.main
	// vfork 子进程:先准备全新空表(内核暂存待提交绑定),装载失败
	// 时丢弃——原空间(父进程仍在使用)全程不被触碰。
	var fresh *memory.Table
	if p.vf != nil {
		fresh, err = p.vf.prepare()
		if err != nil {
			return err
		}
	}
	// 原子边界:装载已成功。先回吐旧映像的内存计量,再为新映像记账;
	// 记账失败(配额耗尽)恢复旧计量,旧程序继续执行。此后释放旧空间、
	// 装入新映像并重置执行流。
	// vfork 子进程不回吐旧计量:旧空间仍归父进程持有,新映像按子
	// 进程自身记账。
	released := uint64(0)
	if p.vf == nil {
		released = p.releaseOld(ctx)
	}
	if err := chargeMemory(ctx, p.process, plan); err != nil {
		if p.vf != nil {
			p.vf.control.DiscardSpace()
		} else {
			p.recharge(ctx, released)
		}
		return err
	}
	chargeSegments(ctx, p.process, loaded.interpreterSegments())
	target := p.space
	if p.vf != nil {
		target = fresh
	} else if err := p.clearSpace(ctx); err != nil {
		return err
	}
	if err := populateSpace(ctx, target, plan); err != nil {
		if p.vf != nil {
			p.vf.control.DiscardSpace()
		}
		return err
	}
	if err := populateSegments(ctx, target, loaded.interpreterSegments()); err != nil {
		if p.vf != nil {
			p.vf.control.DiscardSpace()
		}
		return err
	}
	// brk 随新映像布局重建,堆起始不得沿用旧布局。vfork 子进程的
	// 每空间装载状态随新表重建。
	p.handler.Heap = newHeap(target, p.process, plan)
	if p.vf != nil {
		p.handler.Mapper = newMapper(target, p.process, plan)
	}
	// 动态程序入口为解释器(AT_BASE/AT_ENTRY/AT_PHDR 已在主计划
	// 初始栈,见 F06);静态程序为主程序入口。
	initial := cpu.Machine{PC: loaded.Entry(), Registers: make([]uint64, 32), Floats: make([]uint64, 32)}
	initial.Registers[regSp] = plan.Stack.SP
	if err := p.core.Reset(ctx, initial); err != nil {
		if p.vf != nil {
			p.vf.control.DiscardSpace()
		}
		return err
	}
	if p.vf != nil {
		// 换绑执行上下文并固化内核绑定,随后释放父进程暂停——
		// 此后子进程与父进程各自独立运行。
		if err := p.vf.switchTo(ctx, target); err != nil {
			p.vf.control.DiscardSpace()
			return err
		}
		p.space = target
		p.vf.release()
	}
	if control, ok := p.process.Control.(proc.ExecControl); ok {
		err := control.ResetForExec(path, finalArgv, environmentFromList(envp))
		return err
	}
	return nil
}

// load 读取客体路径的映像并装载;非 ELF 的 "#!" 脚本按 Linux 语义
// 解析解释器与可选参数,最终 argv 为 [解释器(,参数), 脚本路径,
// 原 argv[1:]...],随后装载解释器映像(嵌套上限 4 层)。
func (p *processExec) load(ctx context.Context, path string, argv, envp []string, depth int) (*loadedProgram, []string, error) {
	if depth > maxShebangDepth {
		return nil, nil, errno.Wrap(errno.NoExec, "shebang 解释器嵌套过深")
	}
	content, err := readImage(ctx, p.process, path)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.HasPrefix(content, []byte("#!")) {
		loaded, err := loadProgram(ctx, p.process, path, argv, envp)
		return loaded, argv, err
	}
	interp, arg, ok := parseShebang(content)
	if !ok {
		return nil, nil, errno.Wrap(errno.NoExec, "shebang 行格式无效")
	}
	final := make([]string, 0, len(argv)+3)
	final = append(final, interp)
	if arg != "" {
		final = append(final, arg)
	}
	final = append(final, path)
	if len(argv) > 1 {
		final = append(final, argv[1:]...)
	}
	return p.load(ctx, interp, final, envp, depth+1)
}

// clearSpace 卸载旧映像的全部映射区(含预算释放)。
func (p *processExec) clearSpace(ctx context.Context) error {
	regions, err := p.space.Regions(ctx)
	if err != nil {
		return err
	}
	for _, region := range regions {
		if err := p.space.Unmap(ctx, region.Start, region.End); err != nil {
			return err
		}
	}
	return nil
}

// releaseOld 按当前映射总量释放旧地址空间的内存计量并返回释放量;
// 无计量端口时跳过。计量接口的失败保持尽力而为:地址空间自身仍会
// 在随后被卸载,误差随进程退出回收。
func (p *processExec) releaseOld(ctx context.Context) uint64 {
	meters := metersOf(p.process)
	if meters == nil {
		return 0
	}
	account, err := p.space.Account(ctx)
	if err != nil || account.Mapped == 0 {
		return 0
	}
	_ = meters.ReleaseKind(ctx, uint64(p.process.ID), resources.KindMemory, account.Mapped)
	return account.Mapped
}

// recharge 在替换中途失败时恢复此前回吐的内存计量。
func (p *processExec) recharge(ctx context.Context, released uint64) {
	if meters := metersOf(p.process); meters != nil && released > 0 {
		_ = meters.ChargeKind(ctx, uint64(p.process.ID), resources.KindMemory, released)
	}
}

// parseShebang 解析 "#!解释器 参数" 首行(上限 256 字节,与
// BINPRM_BUF_SIZE 一致);空解释器、缺换行或多于一个参数均视为
// 格式无效(ENOEXEC)。
func parseShebang(content []byte) (interp, arg string, ok bool) {
	header := content
	if len(header) > 256 {
		header = header[:256]
	}
	end := bytes.IndexByte(header, '\n')
	if end < 0 {
		return "", "", false
	}
	fields := strings.Fields(string(header[2:end]))
	if len(fields) == 0 || len(fields) > 2 {
		return "", "", false
	}
	interp = fields[0]
	if len(fields) == 2 {
		arg = fields[1]
	}
	return interp, arg, true
}

// environmentFromList 把 execve 的 "K=V" 环境列表转为进程环境表;
// 不含 '=' 的条目按 Linux 惯例保留原样不可表达,跳过。
func environmentFromList(envp []string) map[string]string {
	env := make(map[string]string, len(envp))
	for _, entry := range envp {
		if index := strings.IndexByte(entry, '='); index > 0 {
			env[entry[:index]] = entry[index+1:]
		}
	}
	return env
}

// processKiller 把进程控制端口的信号发送能力适配为 kill/tgkill 端口。
type processKiller struct {
	control proc.SignalKillControl
}

func (p processKiller) Kill(ctx context.Context, pid int64, number uint64) error {
	if number != 0 && !signal.Number(number).Valid() {
		return errno.Invalid
	}
	return p.control.KillSignal(pid, signal.Number(number))
}

func (p processKiller) Tgkill(ctx context.Context, tgid, tid int64, number uint64) error {
	if number != 0 && !signal.Number(number).Valid() {
		return errno.Invalid
	}
	return p.control.Tgkill(tgid, tid, signal.Number(number))
}

func (p processKiller) Tkill(ctx context.Context, tid int64, number uint64) error {
	if number != 0 && !signal.Number(number).Valid() {
		return errno.Invalid
	}
	return p.control.KillSignal(tid, signal.Number(number))
}

// processSessions 适配 setsid/setpgid/getpgid/getsid 的会话端口(H11)。
type processSessions struct {
	control proc.SessionControl
}

func (p processSessions) SetPGID(pid int64, pgid int64) error {
	return p.control.SetPGID(pid, pgid)
}

func (p processSessions) SetSID() error {
	return p.control.SetSID()
}

func (p processSessions) GetPGID(pid int64) (uint64, error) {
	return p.control.GetPGID(pid)
}

func (p processSessions) GetSID(pid int64) (uint64, error) {
	return p.control.GetSID(pid)
}

// processTrace 适配受限系统调用追踪端口(G08)。
type processTrace struct {
	control proc.TraceRecorder
}

func (p processTrace) RecordSyscall(pid, tid uint64, number uint64, result int64, duration trace.Duration) {
	p.control.RecordSyscall(pid, tid, number, result, duration)
}

// isDynamicImage 报告映像头是否为 ET_DYN(e_type=3)。
func isDynamicImage(content []byte) bool {
	if len(content) < 18 {
		return false
	}
	return binary.LittleEndian.Uint16(content[16:]) == 3
}

// instructionsPerSlice 是 H02 的每个时间片指令数:时间片用尽的
// CPU 密集进程让出执行权,其他可运行进程继续推进。
const instructionsPerSlice = 1 << 16

// sliceSwitcher 把执行权排队端口适配为 Runner 的任务切换端口:
// 时间片用尽时让出执行权并等待下一轮(单核轮转, D10/D11 的
// Switcher 契约在进程间复用)。
type sliceSwitcher struct {
	sched     proc.ExecutionScheduling
	remaining uint64
}

func (s *sliceSwitcher) Tick() bool {
	s.remaining--
	return s.remaining == 0
}

func (s *sliceSwitcher) Switch(ctx context.Context) (bool, error) {
	s.remaining = instructionsPerSlice
	if err := s.sched.Slice(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// schedulingDispatch 在 ecall 分发期间让出执行权(H03):阻塞在
// 等待队列或睡眠中的系统调用不独占执行,其他进程继续推进;分发
// 返回后重新排队获取执行权。CPU 故障的分发快速且不阻塞,直通。
type schedulingDispatch struct {
	next  common.Dispatcher
	sched proc.ExecutionScheduling
}

func (d schedulingDispatch) Dispatch(ctx context.Context, trap cpu.Trap) (common.Exit, error) {
	if trap.Kind != cpu.TrapEcall {
		return d.next.Dispatch(ctx, trap)
	}
	// 只归还不重取:系统调用返回后再重新排队。若在此处用 Slice
	// (归还+重取)会在返回后再次 Enter,等待自己持有的令牌。
	if err := d.sched.Leave(); err != nil {
		return common.Exit{}, err
	}
	outcome, err := d.next.Dispatch(ctx, trap)
	if enterErr := d.sched.Enter(ctx); enterErr != nil && err == nil {
		return outcome, enterErr
	}
	return outcome, err
}
