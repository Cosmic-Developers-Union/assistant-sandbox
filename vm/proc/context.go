package proc

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/futex"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/trace"
)

type contextKey struct{}

func WithContext(ctx context.Context, p Proc) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

func FromContext(ctx context.Context) (Proc, error) {
	p, ok := ctx.Value(contextKey{}).(Proc)
	if !ok || p.Resources == nil {
		return Proc{}, errno.NotImplemented
	}
	return p, nil
}

type RunSpec struct {
	Parent  PID
	Name    string
	Args    []string
	Dir     string
	Env     map[string]string
	Streams fd.Streams

	IgnoreSignals []signal.Number

	NiceAdjust *int
}

type Control interface {
	Begin(PID, string, []string, string, map[string]string, fd.Streams) (Proc, error)
	Finish(PID, PID, uint8) error

	Run(context.Context, RunSpec) (Proc, error)
}

type Usage struct {
	Self uint64

	Children uint64
}

// ChildState 是 wait4/waitid 观察到的子进程终态。
type ChildState struct {
	PID        PID
	ExitCode   uint8
	ExitSignal uint8
}

// WaitControl 是 wait4/waitid 所需的子进程等待能力:
// parent 由实现从调用进程推断。
type WaitControl interface {
	// WaitChild 等待调用进程的子进程:target=0 表示任意子进程;
	// nohang 时不阻塞,found=false 表示尚无可回收子进程。
	WaitChild(ctx context.Context, target PID, nohang bool) (ChildState, bool, error)

	// HasChildren 报告调用进程是否还有子进程(ECHILD 判定)。
	HasChildren() bool
}

// ForkControl 是 fork/clone(SIGCHLD) 的进程复制端口:实现复制
// 进程表条目、描述符、信号处置与地址空间,执行续接由调用方完成。
type ForkControl interface {
	Fork() (Proc, *memory.Table, error)
}

// ThreadControl 是 clone 线程形态(K01/K02)的端口:内核登记同组
// 线程条目并接线共享资源(地址空间、描述符集合、信号处置);执行
// 续接(同一地址空间、sp/tls/返回值 0)由调用方完成。CLONE_FILES
// 与 CLONE_SIGHAND 随线程形态固定共享。
type ThreadControl interface {
	// CloneThread 在调用线程所属组内创建线程:返回线程条目与共享
	// 地址空间。clearTID 是 CLONE_CHILD_CLEARTID 的登记地址(0=
	// 不登记),线程退出时由内核消费并唤醒 futex 等待者(K03)。
	CloneThread(clearTID uint64) (Proc, *memory.Table, error)

	// FinishThread 结束单个线程:非组长直接清算(无僵尸,join 依赖
	// clear_child_tid 唤醒);组长保留占位直到组完成。signal 非 0
	// 表示致命信号,按 Linux 语义升级为整组结束(K08)。
	FinishThread(id PID, code uint8, sig uint8) error

	// FinishGroup 结束整个线程组(exit_group,H08):全组以同一状态
	// 结束,组长承载退出状态转僵尸并通知父进程。
	FinishGroup(id PID, code uint8, sig uint8) error
}

// SignalDeliveryControl 是客户态信号投递端口:执行器在指令边界
// 轮询待投递信号,写信号帧并重定向执行流。
type SignalDeliveryControl interface {
	// PendingDeliverable 返回待投递信号及其登记处置;无则 found=false。
	PendingDeliverable() (signal.Number, signal.ActionConfig, bool)

	// ConsumeSignal 在帧写入后清除挂起位。
	ConsumeSignal(signal.Number)

	// BlockForHandler 并入处置掩码并返回投递前的屏蔽集。
	BlockForHandler(signal.Number, uint64, bool) (uint64, error)

	// RestoreHandlerMask 在 rt_sigreturn 时恢复帧内屏蔽集。
	RestoreHandlerMask(uint64) error

	// AlternateStack 返回登记的备用信号栈;present=false 未登记。
	AlternateStack() (signal.AltStack, bool)

	// SetAlternateStack 登记/清除备用信号栈。
	SetAlternateStack(signal.AltStack) error
}

// ExecControl 是 execve 成功后进程状态的内核侧复位端口(H07):
// 进程表元数据随新映像更新,CLOEXEC 描述符关闭,信号处置、
// 屏蔽集与挂起集合全部复位。
type ExecControl interface {
	// ResetForExec 由执行器在映像替换完成后调用;path/args/env
	// 为新映像的客体路径与参数环境。
	ResetForExec(path string, args []string, env map[string]string) error
}

// SignalKillControl 是 kill/tgkill/进程组发送的投递端口。
type SignalKillControl interface {
	// KillSignal 按 kill(2) 的 pid 语义发送:pid>0 目标进程,
	// pid=0 调用方进程组,pid=-1 除调用方外的全部进程,pid<-1 进程组 -pid。
	KillSignal(pid int64, number signal.Number) error

	// Tgkill 按 tgkill(2) 投递线程定向信号(K08):tid 必须属于
	// tgid 的线程组,否则 ESRCH。
	Tgkill(tgid, tid int64, number signal.Number) error
}

// FutexControl 是 futex 系统调用(K05)的同步端口:暴露内核的
// futex 等待表;表按 {所有者,客体地址} 键控。
type FutexControl interface {
	FutexTable() *futex.Table
}

// TidAddressControl 是 set_tid_address(K03)的登记端口与退出清算
// 回调:SetTidAddress 登记 clear_child_tid 地址,TidAddress 查询,
// ClearTidAtExit 由执行器在运行流终止时调用(写零回调由执行器
// 注入,内核不持有客体地址空间)。
type TidAddressControl interface {
	SetTidAddress(address uint64) error

	TidAddress() uint64

	ClearTidAtExit(ctx context.Context, write func(context.Context, uint64, []byte) error)
}

// SessionControl 是 setsid/setpgid/getpgid/getsid 的会话与进程组端口
// (H11):pid=0 表示调用方自身,负值非法;实现按 Linux 权限规则校验
// (目标必须是调用方或其子进程、同会话、非会话组长)。
type SessionControl interface {
	// SetPGID 把目标进程移入进程组;pgid=0 表示以目标进程号为组号。
	SetPGID(pid int64, pgid int64) error

	// SetSID 为调用方建立新会话(调用方成为组长)。
	SetSID() error

	// GetPGID 返回目标进程的进程组号。
	GetPGID(pid int64) (uint64, error)

	// GetSID 返回目标进程的会话号。
	GetSID(pid int64) (uint64, error)
}

// TraceRecorder 是受限系统调用追踪的记录端口(G08):只记录
// PID/TID、调用编号、结果与耗时,不得携带路径、参数或缓冲区内容。
type TraceRecorder interface {
	// RecordSyscall 记录一次系统调用;result 为返回值,失败时为 -errno。
	RecordSyscall(pid, tid uint64, number uint64, result int64, duration trace.Duration)
}

// SignalDispositionControl 是 rt_sigaction/rt_sigprocmask 的登记端口。
type SignalDispositionControl interface {
	SetSignalAction(signal.Number, signal.ActionConfig) error
	SignalAction(signal.Number) (signal.ActionConfig, error)
	SetSignalMask(signal.MaskHow, uint64) (uint64, error)
	SignalMask() (uint64, error)
}

type UsageControl interface {
	Usage(PID) (Usage, error)
}

type TableControl interface {
	Processes() []Proc
}

type SignalControl interface {
	Signal(PID, signal.Number) error

	Exists(PID) (bool, error)
}

type UmaskControl interface {
	SetUmask(PID, stdfs.FileMode) error
}

type PriorityControl interface {
	Nice(PID) (int, error)

	SetNice(PID, int) error
}

type JobSpec struct {
	Queue    byte
	Deadline uint64
	Nice     int
	Dir      string
	Env      map[string]string
	Mail     bool
	Script   string
}

type PrintSpec struct {
	Destination string
	Copies      int
	Title       string

	Copy   bool
	Notify bool
	Write  bool
	Body   string
}

type Scheduler interface {
	QueueJob(context.Context, JobSpec) (string, error)

	ListJobs(context.Context) ([]string, error)

	RemoveJob(context.Context, string) (bool, error)

	ReadJob(context.Context, string) (string, error)

	QueuePrint(context.Context, PrintSpec) (string, error)

	ListPrintRequests(context.Context) ([]string, error)

	RunDueJobs(context.Context) error
}

const ArgMax = 128 << 10

type SystemConfiguration struct {
	BootTime int64

	BootDeclared bool

	ChildMax int64

	ArgMax int64
}

type SystemControl interface {
	SystemConfiguration() (SystemConfiguration, error)
}

// ExecutionScheduling 是进程执行权排队端口(H02/H03):单核语义下,
// 时间片用尽或进入(可能阻塞的)系统调用时让出执行权排到队尾,
// 驻停(SIGSTOP)与退出归还执行权;阻塞在等待队列的进程不持有
// 执行权,不占用其他进程的执行时间片。未装配时进程独立执行。
type ExecutionScheduling interface {
	// Enter 排队获取执行权;上下文结束或内核关闭时返回错误。
	Enter(ctx context.Context) error

	// Slice 让出执行权并等待下一轮;没有其他等待者时立即继续。
	Slice(ctx context.Context) error

	// Leave 归还执行权(进入长期阻塞、驻停或退出前);幂等。
	Leave() error
}
