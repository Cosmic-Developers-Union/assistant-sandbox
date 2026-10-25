package proc

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
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
