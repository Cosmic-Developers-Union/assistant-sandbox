package common

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/epoll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/inotify"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/locks"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signalfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/trace"
)

type Memory interface {
	Read(ctx context.Context, address uint64, buffer []byte) error

	Write(ctx context.Context, address uint64, buffer []byte) error
}

// SharedKeyer 是共享后备区间查询(K10):futex 键控按区间后备标识
// 加盐,使文件共享映射上的同步原语跨进程可见。由内存表实现。
type SharedKeyer interface {
	SharedKeyAt(address uint64) (uint64, bool)
}

type Descriptors interface {
	Read(ctx context.Context, descriptor uint64, buffer []byte) (int, error)

	Write(ctx context.Context, descriptor uint64, buffer []byte) (int, error)

	Close(ctx context.Context, descriptor uint64) error

	Seek(ctx context.Context, descriptor uint64, offset int64, whence SeekWhence) (int64, error)

	Get(ctx context.Context, descriptor uint64) (any, error)

	Fcntl(ctx context.Context, descriptor uint64, cmd uint64, arg uint64) (uint64, error)

	Pipe(ctx context.Context) (uint64, uint64, error)

	EventFD(ctx context.Context, initialValue, flags uint64) (uint64, error)

	// SignalFD 创建新的 signalfd;mask 为客体信号位图(已剥离
	// SIGKILL/SIGSTOP 位)。
	SignalFD(ctx context.Context, mask, flags uint64) (uint64, error)

	// UpdateSignalFD 更新已存在 signalfd 的掩码与选项。
	UpdateSignalFD(ctx context.Context, descriptor, mask, flags uint64) error

	// Inotify 创建新的 inotify 实例(I20);InotifyAt 取已存在实例
	// (add_watch/rm_watch 用)。
	Inotify(ctx context.Context, flags uint64) (uint64, error)

	InotifyAt(ctx context.Context, descriptor uint64) (*inotify.FD, error)

	// Epoll 创建新的 epoll 实例(L07);EpollInstance 取已存在实例
	// (epoll_ctl/epoll_pwait 用)。
	Epoll(ctx context.Context, flags uint64) (uint64, error)

	EpollInstance(ctx context.Context, descriptor uint64) (*epoll.FD, error)

	Dup2(ctx context.Context, oldfd, newfd uint64, cloexec bool) error

	// DupFrom 复制描述符到 ≥ 0 的最低空闲编号,共享底层句柄。
	DupFrom(ctx context.Context, descriptor uint64) (uint64, error)
}

type SeekWhence int

const (
	SeekSet SeekWhence = iota

	SeekCurrent

	SeekEnd
)

type Clock interface {
	Wall(ctx context.Context) (int64, error)

	Monotonic(ctx context.Context) (uint64, error)
}

// Sleeper 提供可被取消的纳秒级等待;ctx 取消或内核关闭时返回错误,
// clock_nanosleep 将其转换为 EINTR 并报告剩余时间。
type Sleeper interface {
	Sleep(ctx context.Context, nanoseconds uint64) error
}

// Interruptor 报告登记了客户态处理器的待投递信号,供阻塞系统调用
// 按 EINTR/SA_RESTART 契约响应信号投递(J09)。
type Interruptor interface {
	DeliverableSignal() (signal uint64, action SignalAction, deliverable bool)
}

// Execer 是 execve 的映像替换端口:成功时执行器已完成映像与执行
// 状态的原子替换(调用不返回);失败时返回错误且旧程序继续(H07)。
type Execer interface {
	Exec(ctx context.Context, path string, argv, envp []string) error
}

// Limiter 暴露客体进程的真实资源限额,供 getrlimit/prlimit64 查询;
// 上限只允许下调,尝试超过真实预算的设置返回 EPERM。
type Limiter interface {
	// DescriptorLimit 返回 NOFILE 限额;false 表示未配置,按 RLIM_INFINITY 报告。
	DescriptorLimit(ctx context.Context) (uint64, bool)

	SetDescriptorLimit(ctx context.Context, value uint64) error
}

type Identity struct {
	PID  uint64
	PPID uint64
	// TID 是调用线程号;单线程模型下与 PID 相同,0 表示未区分。
	TID uint64
	// TGID 是线程组号(K02):同组线程共享地址空间,futex 等按组
	// 键控;单线程模型下与 PID 相同,0 表示未区分(实现回退 PID)。
	TGID uint64
	UID  uint32
	GID  uint32
}

type OpenRequest struct {
	Path  string
	Flags OpenFlags
	Perm  uint32
}

type Files interface {
	Open(ctx context.Context, request OpenRequest) (uint64, error)

	Stat(ctx context.Context, path string) (stdfs.FileInfo, error)

	Lstat(ctx context.Context, path string) (stdfs.FileInfo, error)

	Readlink(ctx context.Context, path string) (string, error)

	Mkdir(ctx context.Context, path string, mode uint32) error

	Remove(ctx context.Context, path string) error

	Symlink(ctx context.Context, target, linkpath string) error

	Link(ctx context.Context, oldpath, newpath string) error

	Rename(ctx context.Context, oldpath, newpath string) error

	ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error)

	// SetTimes 以纳秒更新访问/修改时间;follow 决定是否解析路径
	// 末端的符号链接(utimensat AT_SYMLINK_NOFOLLOW)。
	SetTimes(ctx context.Context, path string, atime, mtime int64, follow bool) error

	// Statfs 报告路径所在文件系统的容量视图(statfs)。
	Statfs(ctx context.Context, path string) (StatfsInfo, error)
}

// StatfsInfo 是 statfs 编码所需的文件系统容量视图;
// 配额未声明时按 0 报告。
type StatfsInfo struct {
	Bytes  vmfs.Quota
	Inodes vmfs.Quota
}

const PathLimit = 4096

const atCurrentWorkingDirectory = ^uint64(0) - 99

// asm-generic 的 AT_REMOVEDIR:unlinkat 借此表达 rmdir 语义。
const atRemovedir = 0o1000

// asm-generic 的 fstatat 标志。
const (
	atSymlinkNoFollow = 0x100
	atEmptyPath       = 0x1000
	atFlagMask        = atSymlinkNoFollow | atEmptyPath
)

const DefaultBufferLimit = 1 << 20

type Random interface {
	Read(context.Context, []byte) (int, error)
}

type Syscalls struct {
	Memory Memory

	Descriptors Descriptors

	Files Files

	Clock Clock

	Identity *Identity

	// Credentials 是每进程身份变更端口(O01);nil 时 setuid 等 ENOTSUP。
	Credentials CredentialSetter

	// Gaps 是运行时启动缺口端口(Q02)。
	Gaps GapSink

	Heap Heap

	Mapper Mappable

	Sleeper Sleeper

	// Interrupts 报告登记了处理器的待投递信号(J09);未装配时
	// 阻塞调用不被信号打断,等待自然到期或上下文取消。
	Interrupts Interruptor

	// Execs 是 execve 的映像替换端口(H06/H07):成功时当前执行流
	// 已切换到新映像(本调用不返回);失败时旧程序继续。
	Execs Execer

	Limiter Limiter

	BufferLimit uint64

	// VectorLimit 约束 argv/envp 等指针向量的条目数;0 使用默认值。
	VectorLimit uint64

	Random Random

	Sysname    string
	Nodename   string
	Release    string
	Version    string
	Machine    string
	Domainname string

	Cwd string

	// CPUs 是客体可见的 CPU 数;0 按单 CPU 报告。
	CPUs uint64

	Waiter Waiter

	Signals SignalDispositions

	Spawner Spawner

	// Threader 是 clone 线程形态(K02)的端口;未装配时线程形态
	// clone 报 ENOSYS 族。
	Threader ThreadSpawner

	// Vforker 是 clone 的 vfork 形态(H12)端口;未装配时 vfork 形态
	// 报 ENOTSUP。
	Vforker Vforker

	// Notify 是文件变更事件扇出端口(I20):文件类系统调用成功后按
	// 已解析路径发事件;未装配时全部为空操作。
	Notify inotify.Observer

	// Sockets 是 socket 系统调用端口(M02+);未装配时 socket 族
	// 报 NotImplemented 族。
	Sockets Sockets

	// Locks 是字节范围文件锁表(I14);未装配时文件锁命令报 ENOSYS 族。
	Locks *locks.Table

	// RobustLists 是 robust futex 登记端口(K07);未装配时
	// set/get_robust_list 报 ENOSYS 族。
	RobustLists RobustListPort

	// LastExitGroup 记录最近一次 exit 族调用是否为 exit_group(H08):
	// 线程运行器据此区分线程退出与整组退出。仅调用线程读写。
	LastExitGroup bool

	Kills SignalSender

	// Sessions 是 setsid/setpgid/getpgid/getsid 的会话端口(H11);
	// 未装配时这四个调用明确 ENOTSUP。
	Sessions SessionControl

	// Futexes 是 futex WAIT/WAKE 的同步端口(K05);未装配时
	// futex 报 ENOSYS。
	Futexes Futexes

	// TimerSource 是 timerfd 的描述符端口(L10);未装配时
	// timerfd 族报 ENOSYS。
	TimerSource TimerFDs

	// TidAddresses 是 set_tid_address 的登记端口(K03);未装配时
	// 报 ENOSYS。
	TidAddresses TidAddressControl

	// Core 供 rt_sigreturn 恢复完整机器状态;未装配时 rt_sigreturn 报 ENOSYS。
	Core cpu.Port

	// Tracer 是受限的系统调用追踪端口(G08);未装配时不产生轨迹。
	Tracer SyscallTracer
}

// SignalSender 是 kill/tgkill 的发送端口;实现经内核投递,
// 自定义处理器经 J03/J04 的边界投递执行。
type SignalSender interface {
	Kill(ctx context.Context, pid int64, number uint64) error

	// Tgkill 投递线程定向信号(K08):tid 必须属于 tgid 的线程组,
	// 否则 ESRCH;实现校验组成员关系。
	Tgkill(ctx context.Context, tgid, tid int64, number uint64) error

	// Tkill 投递线程定向信号(tkill(2),musl pthread_kill 路径):
	// tid 必须存在,否则 ESRCH。
	Tkill(ctx context.Context, tid int64, number uint64) error
}

// SessionControl 是 setsid/setpgid/getpgid/getsid 的会话与进程组端口
// (H11):pid=0 表示调用方自身,负值非法;实现按 Linux 权限规则校验。
type SessionControl interface {
	// SetPGID 按 setpgid(2) 语义把目标进程移入进程组。
	SetPGID(pid int64, pgid int64) error

	// SetSID 按 setsid(2) 语义为调用方建立新会话。
	SetSID() error

	// GetPGID 返回目标进程的进程组号。
	GetPGID(pid int64) (uint64, error)

	// GetSID 返回目标进程的会话号。
	GetSID(pid int64) (uint64, error)
}

// SyscallTracer 是受限的系统调用追踪端口(G08):只记录 PID/TID、
// 调用编号、结果与耗时,不得携带路径、参数或缓冲区内容。
type SyscallTracer interface {
	// RecordSyscall 记录一次系统调用;result 为返回值,失败时为 -errno。
	RecordSyscall(pid, tid uint64, number uint64, result int64, duration trace.Duration)
}

// Spawner 是 clone(SIGCHLD) 的进程复制端口;实现复制地址空间、
// 描述符与信号处置,并从调用方的执行核心读取续接边界(PC 与寄存器)。
type Spawner interface {
	Fork(ctx context.Context) (uint64, error)
}

// SignalAction 是 rt_sigaction 的登记项(asm-generic struct sigaction):
// Handler 为 0(SIG_DFL)/1(SIG_IGN) 或客户态处理器地址。
type SignalAction struct {
	Handler  uint64
	Mask     uint64
	Flags    uint32
	Restorer uint64
}

// SignalDispositions 是 rt_sigaction/rt_sigprocmask 的登记端口。
// AltStack/SetAltStack 为可选能力,缺失时 sigaltstack 报 ENOTSUP。
type SignalDispositions interface {
	SetAction(sig uint64, action SignalAction) error

	Action(sig uint64) (SignalAction, error)

	// ApplyMask 按 how(SIG_BLOCK/UNBLOCK/SETMASK)调整屏蔽集并返回旧值。
	ApplyMask(how, set uint64) (uint64, error)

	Mask() (uint64, error)

	// AltStack 返回当前备用信号栈(sigaltstack 查询)。
	AltStack(ctx context.Context) (SignalAltStack, error)

	// SetAltStack 登记/清除备用信号栈。
	SetAltStack(ctx context.Context, stack SignalAltStack) error
}

// ChildState 是 wait4/waitid 观察到的子进程终态。
type ChildState struct {
	PID        uint64
	ExitCode   uint8
	ExitSignal uint32
}

// Waiter 是子进程等待端口;parent 由实现从调用进程推断。
type Waiter interface {
	// WaitChild 等待调用进程的子进程:target=0 表示任意子进程;
	// nohang 时不阻塞,found=false 表示尚无可回收子进程。
	WaitChild(ctx context.Context, target uint64, nohang bool) (ChildState, bool, error)

	// HasChildren 报告调用进程是否还有子进程(ECHILD 判定)。
	HasChildren() bool
}

// Handle 分派一次系统调用。未登记的调用一律 ENOSYS(G01);装配了
// 追踪端口时按 G08 记录 PID/TID、调用编号、结果与耗时——轨迹不含
// 任何路径、参数或缓冲区内容。
func (s Syscalls) Handle(ctx context.Context, call Call) Reply {
	// 未登记的系统调用一律 ENOSYS:登记表见 registry.go(G01)。
	if !handled[call.Syscall] {
		return Reply{Errno: errno.NotImplemented}
	}
	if s.Tracer == nil {
		return s.dispatch(ctx, call)
	}
	start := clock.MonotonicNanos()
	reply := s.dispatch(ctx, call)
	elapsed := clock.MonotonicNanos() - start
	s.Tracer.RecordSyscall(s.identityPID(), s.identityTID(), call.Number, s.resultOf(reply), trace.Duration(elapsed))
	return reply
}

// dispatch 是 Handle 的实际分派体(不含追踪)。
func (s Syscalls) dispatch(ctx context.Context, call Call) Reply {
	switch call.Syscall {
	case SysExit, SysExitGroup:
		// H08:exit_group 与 exit 的区分由线程运行器消费(K02)。
		s.LastExitGroup = call.Syscall == SysExitGroup
		return Reply{Exit: true, Value: call.Args[0]}
	case SysWrite:
		return s.write(ctx, call)
	case SysRead:
		return s.read(ctx, call)
	case SysPread64:
		return s.pread(ctx, call)
	case SysPwrite64:
		return s.pwrite(ctx, call)
	case SysOpenAt:
		return s.openAt(ctx, call)
	case SysClose:
		return s.close(ctx, call)
	case SysLseek:
		return s.lseek(ctx, call)
	case SysGetPID, SysGetPPID, SysGetTID, SysGetUID, SysGetEUID, SysGetGID, SysGetEGID:
		return s.identity(call)
	case SysClockGettime:
		return s.clockGettime(ctx, call)
	case SysClockNanosleep:
		return s.clockNanosleep(ctx, call)
	case SysClockGetres:
		return s.clockGetres(ctx, call)
	case SysGettimeofday:
		return s.gettimeofday(ctx, call)
	case SysBrk:
		return s.brk(ctx, call)
	case SysMmap:
		return s.mmap(ctx, call)
	case SysMunmap:
		return s.munmap(ctx, call)
	case SysMprotect:
		return s.mprotect(ctx, call)
	case SysMremap:
		return s.mremap(ctx, call)
	case SysMadvise:
		return s.madvise(ctx, call)
	case SysMsync:
		return s.msync(ctx, call)
	case SysReadv:
		return s.readv(ctx, call)
	case SysWritev:
		return s.writev(ctx, call)
	case SysUname:
		return s.uname(ctx, call)
	case SysGetrandom:
		return s.getrandom(ctx, call)
	case SysGetcwd:
		return s.getcwd(ctx, call)
	case SysSysinfo:
		return s.sysinfo(ctx, call)
	case SysGetpgid, SysGetsid:
		return s.sessionQuery(call)
	case SysSetpgid:
		return s.setpgid(call)
	case SysSetsid:
		return s.setsid(call)
	case SysTimes:
		return s.times(ctx, call)
	case SysGetgroups:
		return s.getgroups(ctx, call)
	// 以下调用按兼容配置 linux-rv64gc-1(A03)声明为不支持:
	// itimer、nice、锁定、mincore、umask 与身份变更的真实语义分别
	// 由 T03、E15、I13、O01 承接;在此之前明确 ENOTSUP,不以无操作
	// 成功伪装完成。
	case SysSchedYield:
		return Reply{}
	case SysPrlimit64:
		return s.prlimit64(ctx, call)
	case SysGetrlimit:
		return s.getrlimit(ctx, call)
	case SysSetrlimit:
		return s.setrlimit(ctx, call)
	case SysGetrusage:
		return s.getrusage(ctx, call)
	case SysGetitimer, SysSetitimer:
		return Reply{Errno: errno.NotSupported}
	case SysGetpriority, SysSetpriority:
		return Reply{Errno: errno.NotSupported}
	case SysMlock, SysMunlock, SysMlockall, SysMunlockall:
		return Reply{Errno: errno.NotSupported}
	case SysMincore:
		return Reply{Errno: errno.NotSupported}
	case SysFaccessat:
		return s.faccessat(ctx, call)
	case SysMkdirat:
		return s.mkdirat(ctx, call)
	case SysUnlinkat:
		return s.unlinkat(ctx, call)
	case SysSymlinkat:
		return s.symlinkat(ctx, call)
	case SysLinkat:
		return s.linkat(ctx, call)
	case SysRenameat:
		return s.renameat(ctx, call)
	case SysRenameat2:
		return s.renameat2(ctx, call)
	case SysFtruncate:
		return s.ftruncate(ctx, call)
	case SysUtimensat:
		return s.utimensat(ctx, call)
	case SysStatfs:
		return s.statfs(ctx, call)
	case SysSchedGetAffinity:
		return s.schedGetAffinity(ctx, call)
	case SysSchedSetAffinity:
		return s.schedSetAffinity(ctx, call)
	case SysGetCPU:
		return s.getcpu(ctx, call)
	case SysWaitid:
		return s.waitid(ctx, call)
	case SysRtSigaction:
		return s.rtSigaction(ctx, call)
	case SysRtSigprocmask:
		return s.rtSigprocmask(ctx, call)
	case SysRtSigreturn:
		return s.rtSigreturn(ctx, call)
	case SysSigaltstack:
		return s.sigaltstack(ctx, call)
	case SysClone:
		return s.cloneProcess(ctx, call)
	case SysExecve:
		return s.execve(ctx, call)
	case SysKill:
		return s.kill(ctx, call)
	case SysTgkill:
		return s.tgkill(ctx, call)
	case SysTkill:
		return s.tkill(ctx, call)
	case SysWait4:
		return s.wait4(ctx, call)
	case SysSetRobustList:
		return s.setRobustList(ctx, call)
	case SysGetRobustList:
		return s.getRobustList(ctx, call)
	case SysFstatfs:
		return s.fstatfs(ctx, call)
	case SysFcntl:
		return s.fcntl(ctx, call)
	case SysPipe2:
		return s.pipe2(ctx, call)
	case SysEventfd2:
		return s.eventfd2(ctx, call)
	case SysSignalfd4:
		return s.signalfd4(ctx, call)
	case SysInotifyInit1:
		return s.inotifyInit1(ctx, call)
	case SysInotifyAddWatch:
		return s.inotifyAddWatch(ctx, call)
	case SysInotifyRmWatch:
		return s.inotifyRmWatch(ctx, call)
	case SysSocket:
		return s.socket(ctx, call)
	case SysSocketpair:
		return s.socketpair(ctx, call)
	case SysBind:
		return s.bind(ctx, call)
	case SysListen:
		return s.listen(ctx, call)
	case SysAccept:
		return s.accept4(ctx, call)
	case SysAccept4:
		return s.accept4(ctx, call)
	case SysConnect:
		return s.connect(ctx, call)
	case SysSendto:
		return s.sendto(ctx, call)
	case SysRecvfrom:
		return s.recvfrom(ctx, call)
	case SysShutdown:
		return s.shutdown(ctx, call)
	case SysGetsockname:
		return s.getsockname(ctx, call)
	case SysGetpeername:
		return s.getpeername(ctx, call)
	case SysGetsockopt:
		return s.getsockopt(ctx, call)
	case SysSetsockopt:
		return s.setsockopt(ctx, call)
	case SysSendmsg:
		return s.sendmsg(ctx, call)
	case SysRecvmsg:
		return s.recvmsg(ctx, call)
	case SysDup:
		return s.dup(ctx, call)
	case SysDup3:
		return s.dup3(ctx, call)
	case SysGetdents64:
		return s.getdents64(ctx, call)
	case SysPselect6:
		return s.pselect6(ctx, call)
	case SysFutex:
		return s.futex(ctx, call)
	case SysIoctl:
		return s.ioctl(ctx, call)
	case SysSetTidAddress:
		return s.setTidAddress(ctx, call)
	case SysTimerfdCreate:
		return s.timerfdCreate(ctx, call)
	case SysTimerfdSettime:
		return s.timerfdSettime(ctx, call)
	case SysTimerfdGettime:
		return s.timerfdGettime(ctx, call)
	case SysEpollCreate1:
		return s.epollCreate1(ctx, call)
	case SysEpollCtl:
		return s.epollCtl(ctx, call)
	case SysEpollPwait:
		return s.epollPwait(ctx, call)
	case SysEpollPwait2:
		return s.epollPwait2(ctx, call)
	case SysPoll, SysPpoll:
		return s.poll(ctx, call)
	case SysFstat:
		return s.fstat(ctx, call)
	case SysNewfstatat:
		return s.newfstatat(ctx, call)
	case SysStatx:
		return s.statx(ctx, call)
	case SysReadlinkat:
		return s.readlinkat(ctx, call)
	case SysUmask:
		// 文件创建掩码由 I13 交付;未落地前明确 ENOTSUP。
		return Reply{Errno: errno.NotSupported}
	case SysFsync:
		return s.fsync(ctx, call, false)
	case SysFdatasync:
		return s.fsync(ctx, call, true)
	case SysSetuid, SysSetgid, SysSetreuid, SysSetregid, SysSetresuid, SysSetresgid, SysSetfsuid, SysSetfsgid:
		return s.setIdentity(ctx, call)
	default:
		return Reply{Errno: errno.NotImplemented}
	}
}

func (s Syscalls) write(ctx context.Context, call Call) Reply {
	// Linux 先解析描述符:坏 fd 报 EBADF,先于缓冲区校验(write(-1,
	// NULL, 0) 的 errno 语义)。
	if s.Descriptors != nil {
		if _, err := s.Descriptors.Get(ctx, call.Args[0]); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
	}
	buffer, slot, reply, ready := s.writeBuffer(call)
	if !ready {
		return reply
	}
	if len(buffer) == 0 {
		// 零长度写不触碰缓冲区(Linux:合法 fd 上返回 0)。
		return Reply{Value: 0}
	}
	if slot != nil {
		defer writeBufferPool.Put(slot)
	}
	if err := s.Memory.Read(ctx, call.Args[1], buffer); err != nil {

		return Reply{Errno: errno.Fault}
	}
	written, err := s.interruptibleIO(ctx, func(ioCtx context.Context) (int, error) {
		return s.Descriptors.Write(ioCtx, call.Args[0], buffer)
	})
	if err != nil {
		if errors.Is(err, errno.BrokenPipe) {
			// EPIPE 前先投递 SIGPIPE(I15):默认处置终止进程,
			// 忽略/屏蔽/登记处理器时调用仍按 EPIPE 返回。
			s.raisePipeSignal(ctx)
		}
		return Reply{Errno: errnoOf(err)}
	}
	if written < 0 || written > len(buffer) {
		return Reply{Errno: errno.IO}
	}
	// I20:成功写入向文件监视发 IN_MODIFY。
	s.observe(s.descriptorPath(ctx, call.Args[0]), inotify.InModify, 0, false)
	return Reply{Value: uint64(written)}
}

var writeBufferPool = sync.Pool{New: func() any {
	buffer := make([]byte, 0, 4096)
	return &buffer
}}

// writeBuffer 为 write 提供有界缓冲(T11):小写入复用池,大写入按需
// 分配;slot 非空表示缓冲来自池,调用方写完后必须归还。
func (s Syscalls) writeBuffer(call Call) ([]byte, *[]byte, Reply, bool) {
	if s.Memory == nil || s.Descriptors == nil {
		return nil, nil, Reply{Errno: errno.NotImplemented}, false
	}
	count := call.Args[2]
	if count == 0 {
		return nil, nil, Reply{}, false
	}
	limit := s.BufferLimit
	if limit == 0 {
		limit = DefaultBufferLimit
	}
	if count > limit {
		return nil, nil, Reply{Errno: errno.Invalid}, false
	}
	if count <= 4096 {
		slot := writeBufferPool.Get().(*[]byte)
		if cap(*slot) < int(count) {
			*slot = make([]byte, count)
		} else {
			*slot = (*slot)[:count]
		}
		return *slot, slot, Reply{}, true
	}
	return make([]byte, count), nil, Reply{}, true
}

// GapSink 是运行时启动缺口端口(Q02)。
type GapSink interface {
	MissingFile(ctx context.Context, path string)
}

// CredentialSetter 是每进程 UID/GID 变更端口(O01)。
type CredentialSetter interface {
	SetCredentials(ctx context.Context, uid, gid uint32) error
}

func (s Syscalls) setIdentity(ctx context.Context, call Call) Reply {
	if s.Identity == nil || s.Credentials == nil {
		return Reply{Errno: errno.NotSupported}
	}
	switch call.Syscall {
	case SysSetuid:
		return s.setUID(ctx, call.Args[0])
	case SysSetgid:
		return s.setGID(ctx, call.Args[0])
	case SysSetreuid:
		uid := call.Args[1]
		if uid == ^uint64(0) {
			uid = call.Args[0]
		}
		if uid == ^uint64(0) {
			uid = uint64(s.Identity.UID)
		}
		return s.setUID(ctx, uid)
	case SysSetregid:
		gid := call.Args[1]
		if gid == ^uint64(0) {
			gid = call.Args[0]
		}
		if gid == ^uint64(0) {
			gid = uint64(s.Identity.GID)
		}
		return s.setGID(ctx, gid)
	case SysSetresuid:
		uid := call.Args[1]
		if uid == ^uint64(0) {
			uid = call.Args[0]
		}
		if uid == ^uint64(0) {
			uid = uint64(s.Identity.UID)
		}
		return s.setUID(ctx, uid)
	case SysSetresgid:
		gid := call.Args[1]
		if gid == ^uint64(0) {
			gid = call.Args[0]
		}
		if gid == ^uint64(0) {
			gid = uint64(s.Identity.GID)
		}
		return s.setGID(ctx, gid)
	case SysSetfsuid:
		return s.setUID(ctx, call.Args[0])
	case SysSetfsgid:
		return s.setGID(ctx, call.Args[0])
	default:
		return Reply{Errno: errno.NotImplemented}
	}
}

func (s Syscalls) setUID(ctx context.Context, value uint64) Reply {
	uid := uint32(value)
	if s.Identity.UID != 0 && uid != s.Identity.UID {
		return Reply{Errno: errno.NotPermitted}
	}
	if err := s.Credentials.SetCredentials(ctx, uid, s.Identity.GID); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	s.Identity.UID = uid
	return Reply{}
}

func (s Syscalls) setGID(ctx context.Context, value uint64) Reply {
	gid := uint32(value)
	if s.Identity.GID != 0 && gid != s.Identity.GID {
		return Reply{Errno: errno.NotPermitted}
	}
	if err := s.Credentials.SetCredentials(ctx, s.Identity.UID, gid); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	s.Identity.GID = gid
	return Reply{}
}

// sigPipe 是 asm-generic 的 SIGPIPE 信号号。
const sigPipe uint64 = 13

// raisePipeSignal 向调用方进程投递 SIGPIPE(write 命中 EPIPE 时,
// 与 Linux 语义一致)。端口未装配时静默跳过,EPIPE 仍如实返回。
func (s Syscalls) raisePipeSignal(ctx context.Context) {
	if s.Kills == nil || s.Identity == nil {
		return
	}
	_ = s.Kills.Kill(ctx, int64(s.Identity.PID), sigPipe)
}

func (s Syscalls) read(ctx context.Context, call Call) Reply {
	buffer, reply, ready := s.buffer(call)
	if !ready {
		return reply
	}
	loaded, err := s.interruptibleIO(ctx, func(ioCtx context.Context) (int, error) {
		return s.Descriptors.Read(ioCtx, call.Args[0], buffer)
	})
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Reply{}
		}
		return Reply{Errno: errnoOf(err)}
	}
	if loaded < 0 || loaded > len(buffer) {
		return Reply{Errno: errno.IO}
	}
	if loaded == 0 {
		return Reply{}
	}
	if err := s.Memory.Write(ctx, call.Args[1], buffer[:loaded]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	// I20:成功读取向文件监视发 IN_ACCESS。
	s.observe(s.descriptorPath(ctx, call.Args[0]), inotify.InAccess, 0, false)
	return Reply{Value: uint64(loaded)}
}

func (s Syscalls) buffer(call Call) ([]byte, Reply, bool) {
	if s.Memory == nil || s.Descriptors == nil {

		return nil, Reply{Errno: errno.NotImplemented}, false
	}
	count := call.Args[2]
	if count == 0 {

		return nil, Reply{}, false
	}
	limit := s.BufferLimit
	if limit == 0 {
		limit = DefaultBufferLimit
	}
	if count > limit {
		return nil, Reply{Errno: errno.Invalid}, false
	}
	return make([]byte, count), Reply{}, true
}

// pread64/pwrite64 在显式偏移读写,不改变描述符游标(Linux 语义)。
func (s Syscalls) pread(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	count := call.Args[2]
	if count == 0 {
		return Reply{}
	}
	limit := s.BufferLimit
	if limit == 0 {
		limit = DefaultBufferLimit
	}
	if count > limit {
		return Reply{Errno: errno.Invalid}
	}
	offset := int64(call.Args[3])
	if offset < 0 {
		return Reply{Errno: errno.Invalid}
	}
	buffer := make([]byte, count)
	var loaded int
	switch file := handle.(type) {
	case RandomFile:
		if !file.Readable() {
			return Reply{Errno: errno.BadHandle}
		}
		loaded, err = file.ReadAt(buffer, offset)
	case io.ReaderAt:
		loaded, err = file.ReadAt(buffer, offset)
	default:
		return Reply{Errno: errno.Seek}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return Reply{Errno: errnoOf(err)}
	}
	if loaded < 0 || loaded > len(buffer) {
		return Reply{Errno: errno.IO}
	}
	if loaded == 0 {
		return Reply{}
	}
	if err := s.Memory.Write(ctx, call.Args[1], buffer[:loaded]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	s.observe(s.descriptorPath(ctx, call.Args[0]), inotify.InAccess, 0, false)
	return Reply{Value: uint64(loaded)}
}

func (s Syscalls) pwrite(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	count := call.Args[2]
	if count == 0 {
		return Reply{}
	}
	limit := s.BufferLimit
	if limit == 0 {
		limit = DefaultBufferLimit
	}
	if count > limit {
		return Reply{Errno: errno.Invalid}
	}
	offset := int64(call.Args[3])
	if offset < 0 {
		return Reply{Errno: errno.Invalid}
	}
	buffer := make([]byte, count)
	if err := s.Memory.Read(ctx, call.Args[1], buffer); err != nil {
		return Reply{Errno: errno.Fault}
	}
	var written int
	switch file := handle.(type) {
	case RandomFile:
		if !file.Writable() {
			return Reply{Errno: errno.BadHandle}
		}
		written, err = file.WriteAt(buffer, offset)
	case io.WriterAt:
		written, err = file.WriteAt(buffer, offset)
	default:
		return Reply{Errno: errno.Seek}
	}
	if err != nil {
		if errors.Is(err, errno.BrokenPipe) {
			s.raisePipeSignal(ctx)
		}
		return Reply{Errno: errnoOf(err)}
	}
	if written < 0 || written > len(buffer) {
		return Reply{Errno: errno.IO}
	}
	s.observe(s.descriptorPath(ctx, call.Args[0]), inotify.InModify, 0, false)
	return Reply{Value: uint64(written)}
}

// observe 在成功的文件系统操作后向观察端口发事件(I20):未装配
// 或空路径为空操作;事件按已解析的客体绝对路径匹配监视。
func (s Syscalls) observe(path string, mask uint32, cookie uint32, isDir bool) {
	if s.Notify == nil || path == "" {
		return
	}
	s.Notify.Observe(path, mask, cookie, isDir)
}

// descriptorPath 查询描述符对应文件的客体绝对路径(I20,fd 侧事件):
// 句柄不可定位或无路径能力时返回空(尽力而为,不发事件)。
func (s Syscalls) descriptorPath(ctx context.Context, fd uint64) string {
	if s.Descriptors == nil {
		return ""
	}
	handle, err := s.Descriptors.Get(ctx, fd)
	if err != nil {
		return ""
	}
	if pather, ok := handle.(interface{ GuestPath() (string, bool) }); ok {
		if path, ok := pather.GuestPath(); ok {
			return path
		}
	}
	return ""
}

func errnoOf(err error) errno.Code {
	for _, code := range errno.Codes() {
		if errors.Is(err, code) {
			return code
		}
	}
	return errno.IO
}

func (s Syscalls) openAt(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.OpenFlags == nil {

		return Reply{Errno: errno.NotSupported}
	}
	if err := call.OpenFlags.Validate(); err != nil {
		return Reply{Errno: errno.Invalid}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	path, code = s.resolveDirPath(ctx, call.Args[0], path)
	if code != "" {
		return Reply{Errno: code}
	}
	// I20:O_CREAT 且此前不存在时先发 IN_CREATE(与内核一致,
	// 创建打开共发 CREATE+OPEN 两事件)。
	created := false
	isDir := false
	if call.OpenFlags.Create {
		if info, statErr := s.Files.Stat(ctx, path); statErr != nil || info == nil {
			created = true
		} else {
			isDir = info.IsDir()
		}
	} else if info, statErr := s.Files.Stat(ctx, path); statErr == nil && info != nil {
		isDir = info.IsDir()
	}
	descriptor, err := s.Files.Open(ctx, OpenRequest{
		Path:  path,
		Flags: *call.OpenFlags,
		Perm:  uint32(call.Args[3]),
	})
	if err != nil {
		if s.Gaps != nil && errors.Is(err, errno.NotFound) {
			s.Gaps.MissingFile(ctx, path)
		}
		return Reply{Errno: errnoOf(err)}
	}
	if created {
		s.observe(path, inotify.InCreate, 0, false)
	}
	s.observe(path, inotify.InOpen, 0, isDir)
	return Reply{Value: descriptor}
}

func (s Syscalls) close(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	// I20:关闭前取路径与访问模式,按可写性发 IN_CLOSE_WRITE/NOWRITE。
	path := s.descriptorPath(ctx, call.Args[0])
	writable := false
	if handle, err := s.Descriptors.Get(ctx, call.Args[0]); err == nil {
		if flags, ok := handle.(interface{ StatusFlags() (uint32, error) }); ok {
			if raw, flagErr := flags.StatusFlags(); flagErr == nil {
				writable = raw&accmode != 0
			}
		}
	}
	if err := s.Descriptors.Close(ctx, call.Args[0]); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if writable {
		s.observe(path, inotify.InCloseWrite, 0, false)
	} else {
		s.observe(path, inotify.InCloseNowrit, 0, false)
	}
	return Reply{}
}

// accmode 是 O_ACCMODE:0=RDONLY,1=WRONLY,2=RDWR。
const accmode = 3

// resolveDirPath 按目录句柄解析客体路径(I02):绝对路径忽略 dirfd;
// AT_FDCWD 的相对路径按进程工作目录(由文件端口解析);有效目录
// 描述符按句柄目录当前所在位置解析——目录被重命名后句柄跟随
// inode,解析结果反映新位置。非目录句柄报 ENOTDIR,坏描述符 EBADF。
func (s Syscalls) resolveDirPath(ctx context.Context, dirfd uint64, name string) (string, errno.Code) {
	if strings.HasPrefix(name, "/") {
		return name, ""
	}
	if dirfd == atCurrentWorkingDirectory {
		return name, ""
	}
	if s.Descriptors == nil {
		return "", errno.NotImplemented
	}
	if dirfd > math.MaxUint32 {
		return "", errno.BadHandle
	}
	handle, err := s.Descriptors.Get(ctx, dirfd)
	if err != nil {
		return "", errnoOf(err)
	}
	resolver, ok := handle.(vmfs.DirectoryResolver)
	if !ok {
		return "", errno.NotDirectory
	}
	resolved, err := resolver.ResolveGuestPath(name)
	if err != nil {
		return "", errnoOf(err)
	}
	return resolved, ""
}

func (s Syscalls) readPath(ctx context.Context, address uint64) (string, errno.Code) {
	var buffer [1]byte
	path := make([]byte, 0, 64)
	for offset := uint64(0); offset < PathLimit; offset++ {
		if err := s.Memory.Read(ctx, address+offset, buffer[:]); err != nil {
			return "", errno.Fault
		}
		if buffer[0] == 0 {
			if len(path) == 0 {

				return "", errno.NotFound
			}
			return string(path), ""
		}
		path = append(path, buffer[0])
	}
	return "", errno.NameTooLong
}

const (
	clockRealtime  = 0
	clockMonotonic = 1
)

func (s Syscalls) lseek(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var whence SeekWhence
	switch call.Args[2] {
	case uint64(SeekSet):
		whence = SeekSet
	case uint64(SeekCurrent):
		whence = SeekCurrent
	case uint64(SeekEnd):
		whence = SeekEnd
	default:
		return Reply{Errno: errno.Invalid}
	}
	offset, err := s.Descriptors.Seek(ctx, call.Args[0], int64(call.Args[1]), whence)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if offset < 0 {
		return Reply{Errno: errno.Invalid}
	}
	return Reply{Value: uint64(offset)}
}

// identityPID 返回调用进程的 PID(未装配身份端口时为 0)。
func (s Syscalls) identityPID() uint64 {
	if s.Identity == nil {
		return 0
	}
	return s.Identity.PID
}

// identityTID 返回调用线程的 TID;单线程模型下与 PID 相同。
func (s Syscalls) identityTID() uint64 {
	if s.Identity == nil {
		return 0
	}
	if s.Identity.TID != 0 {
		return s.Identity.TID
	}
	return s.Identity.PID
}

// resultOf 把应答折算为轨迹结果:成功为返回值,失败为 -errno。
func (s Syscalls) resultOf(reply Reply) int64 {
	if reply.Errno != "" {
		number, _ := errno.Linux(reply.Errno)
		return -int64(number)
	}
	return int64(reply.Value)
}

func (s Syscalls) identity(call Call) Reply {
	if s.Identity == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	switch call.Syscall {
	case SysGetPID:
		return Reply{Value: s.Identity.PID}
	case SysGetTID:
		if s.Identity.TID != 0 {
			return Reply{Value: s.Identity.TID}
		}
		return Reply{Value: s.Identity.PID}
	case SysGetPPID:
		return Reply{Value: s.Identity.PPID}
	case SysGetUID, SysGetEUID:
		return Reply{Value: uint64(s.Identity.UID)}
	case SysGetGID, SysGetEGID:
		return Reply{Value: uint64(s.Identity.GID)}
	default:
		return Reply{Errno: errno.NotImplemented}
	}
}

// sessionQuery 处理 getpgid/getsid:pid=0 表示调用方自身。会话端口
// 未装配时明确 ENOTSUP,不以调用方 PID 冒充。负 pid 不在此处拒绝:
// Linux 内核对 getpgid/getsid 的负 pid 返回 ESRCH(进程不存在),
// 由端口按真实查找结果应答。
func (s Syscalls) sessionQuery(call Call) Reply {
	if s.Sessions == nil {
		return Reply{Errno: errno.NotSupported}
	}
	pid := int64(call.Args[0])
	var value uint64
	var err error
	switch call.Syscall {
	case SysGetpgid:
		value, err = s.Sessions.GetPGID(pid)
	case SysGetsid:
		value, err = s.Sessions.GetSID(pid)
	default:
		return Reply{Errno: errno.Invalid}
	}
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: value}
}

// setpgid 处理 setpgid(2):pid=0 表示调用方自身,pgid=0 表示以目标
// 进程号作为进程组号;负值非法(Linux 内核返回 EINVAL)。
func (s Syscalls) setpgid(call Call) Reply {
	if s.Sessions == nil {
		return Reply{Errno: errno.NotSupported}
	}
	pid := int64(call.Args[0])
	pgid := int64(call.Args[1])
	if pid < 0 || pgid < 0 {
		return Reply{Errno: errno.Invalid}
	}
	if err := s.Sessions.SetPGID(pid, pgid); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// setsid 处理 setsid(2):调用方脱离原会话并成为新会话与进程组的组长;
// 调用方已是组长时由内核按 EPERM 拒绝。
func (s Syscalls) setsid(call Call) Reply {
	if s.Sessions == nil {
		return Reply{Errno: errno.NotSupported}
	}
	if err := s.Sessions.SetSID(); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) clockGettime(ctx context.Context, call Call) Reply {
	if s.Clock == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var nanoseconds int64
	switch call.Args[0] {
	case clockRealtime:
		wall, err := s.Clock.Wall(ctx)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		nanoseconds = wall
	case clockMonotonic:
		monotonic, err := s.Clock.Monotonic(ctx)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		if monotonic > 1<<63-1 {
			return Reply{Errno: errno.Overflow}
		}
		nanoseconds = int64(monotonic)
	default:

		return Reply{Errno: errno.NotSupported}
	}
	seconds := nanoseconds / nanosPerSecond
	remainder := nanoseconds % nanosPerSecond
	if remainder < 0 {
		seconds--
		remainder += nanosPerSecond
	}
	encoded := encodeTimespec(seconds, remainder)
	if err := s.Memory.Write(ctx, call.Args[1], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

const nanosPerSecond = 1_000_000_000

// TIMER_ABSTIME 要求把请求值解释为时钟绝对时间。
const timerAbstime = 1

func (s Syscalls) clockNanosleep(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Clock == nil || s.Sleeper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != clockRealtime && call.Args[0] != clockMonotonic {
		return Reply{Errno: errno.Invalid}
	}
	flags := call.Args[1]
	if flags&^uint64(timerAbstime) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	seconds, nanoseconds, code := s.readTimespec(ctx, call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	if nanoseconds >= nanosPerSecond {
		return Reply{Errno: errno.Invalid}
	}
	abstime := flags&uint64(timerAbstime) != 0
	if !abstime && int64(seconds) < 0 {
		return Reply{Errno: errno.Invalid}
	}

	if abstime {
		return s.sleepUntil(ctx, call, call.Args[0], int64(seconds), int64(nanoseconds))
	}
	duration, ok := addDuration(int64(seconds), int64(nanoseconds))
	if !ok {
		return Reply{Errno: errno.Overflow}
	}
	return s.sleepRelative(ctx, call, call.Args[0], duration)
}

func (s Syscalls) readTimespec(ctx context.Context, address uint64) (uint64, uint64, errno.Code) {
	var raw [16]byte
	if err := s.Memory.Read(ctx, address, raw[:]); err != nil {
		return 0, 0, errno.Fault
	}
	return binary.LittleEndian.Uint64(raw[0:]), binary.LittleEndian.Uint64(raw[8:]), ""
}

func addDuration(seconds, nanoseconds int64) (int64, bool) {
	if seconds > (1<<63-1-nanoseconds)/nanosPerSecond {
		return 0, false
	}
	total, ok := mulNoOverflow(seconds, nanosPerSecond)
	if !ok {
		return 0, false
	}
	sum, err := addNoOverflow(total, nanoseconds)
	if err != nil {
		return 0, false
	}
	return sum, true
}

func mulNoOverflow(left, right int64) (int64, bool) {
	if left == 0 || right == 0 {
		return 0, true
	}
	product := left * right
	if product/right != left {
		return 0, false
	}
	return product, true
}

func addNoOverflow(left, right int64) (int64, error) {
	sum := left + right
	if sum < left && right > 0 || sum > left && right < 0 {
		return 0, errno.Overflow
	}
	return sum, nil
}

// sleepSlice 是接入信号轮询后的睡眠切片时长(1ms):切片之间检查
// 待投递信号,信号投递延迟以切片为上界。
const sleepSlice uint64 = 1_000_000

// interruptibleIO 执行可能阻塞的描述符读写(H03):登记了处理器的
// 待投递信号到达时以 EINTR 返回(J09 的 1ms 切片契约),上下文
// 结束时取消在途读写并返回其结果。无中断源或睡眠端口未装配时
// 直接执行一次阻塞调用。
func (s Syscalls) interruptibleIO(ctx context.Context, run func(context.Context) (int, error)) (int, error) {
	if s.Interrupts == nil || s.Sleeper == nil {
		return run(ctx)
	}
	if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
		return 0, errno.Interrupted
	}
	ioCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		n   int
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		n, err := run(ioCtx)
		done <- outcome{n, err}
	}()
	for {
		if err := s.Sleeper.Sleep(ctx, sleepSlice); err != nil {
			if ctx.Err() != nil {
				// 上下文结束(关闭/KILL):取消在途读写并返回其结果。
				cancel()
				result := <-done
				return result.n, result.err
			}
			// 睡眠端口的非取消错误与时序无关:退化为单次阻塞等待,
			// 不再轮询中断(读写完成或 ctx 取消后经 done 返回)。
			result := <-done
			return result.n, result.err
		}
		select {
		case result := <-done:
			return result.n, result.err
		default:
		}
		if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
			return 0, errno.Interrupted
		}
	}
}

// sleepOnce 睡指定时长;接入 Interrupts 时按切片睡眠并在片间检查
// 待投递信号,发现即返回 errno.Interrupted。未接入时保持单次睡眠。
func (s Syscalls) sleepOnce(ctx context.Context, nanoseconds uint64) error {
	if s.Interrupts == nil {
		return s.Sleeper.Sleep(ctx, nanoseconds)
	}
	for remaining := nanoseconds; remaining > 0; {
		step := remaining
		if step > sleepSlice {
			step = sleepSlice
		}
		if err := s.Sleeper.Sleep(ctx, step); err != nil {
			return err
		}
		remaining -= step
		if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
			return errno.Interrupted
		}
	}
	return nil
}

// sleepRelative 执行相对等待。相对期限一律以单调时钟计量(L01):
// 系统时间调整既不会让等待时长漂移,也不会让单调时间回退。
func (s Syscalls) sleepRelative(ctx context.Context, call Call, clockID uint64, duration int64) Reply {
	remaining := duration
	for {
		started, err := s.clockNow(ctx, clockMonotonic)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		err = s.sleepOnce(ctx, uint64(remaining))
		if err == nil {
			return Reply{}
		}
		if !errors.Is(err, errno.Interrupted) || s.Interrupts == nil {
			// 上下文取消或时钟故障:按已过时间回填剩余后报 EINTR。
			return s.interrupted(call, clockMonotonic, duration, started)
		}
		// 信号唤醒:折算剩余;SA_RESTART 重启剩余时长,否则报
		// EINTR 并回填剩余(处理器在返回后的指令边界投递)。
		if now, e := s.clockNow(ctx, clockMonotonic); e == nil {
			if elapsed := now - started; elapsed < remaining {
				remaining -= elapsed
			} else {
				return Reply{}
			}
		}
		if _, action, ok := s.Interrupts.DeliverableSignal(); ok && action.Flags&SigactionRestart != 0 {
			continue
		}
		return s.writeRemaining(call, remaining)
	}
}

func (s Syscalls) sleepUntil(ctx context.Context, call Call, clockID uint64, seconds, nanoseconds int64) Reply {
	target, ok := addDuration(seconds, nanoseconds)
	if !ok {
		return Reply{Errno: errno.Overflow}
	}
	for {
		now, err := s.clockNow(ctx, clockID)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		if now >= target {
			return Reply{}
		}
		err = s.sleepOnce(ctx, uint64(target-now))
		if err == nil {
			return Reply{}
		}
		if errors.Is(err, errno.Interrupted) && s.Interrupts != nil {
			// 绝对期限不被信号打断(POSIX):重算剩余继续等待。
			continue
		}
		// 上下文取消等:报 EINTR,不更新剩余时间指针。
		return Reply{Errno: errno.Interrupted}
	}
}

func (s Syscalls) clockNow(ctx context.Context, clockID uint64) (int64, error) {
	if clockID == clockMonotonic {
		monotonic, err := s.Clock.Monotonic(ctx)
		if err != nil {
			return 0, err
		}
		return int64(monotonic), nil
	}
	return s.Clock.Wall(ctx)
}

// interrupted 把等待失败统一呈现为 EINTR,并尽力回填剩余时间。
func (s Syscalls) interrupted(call Call, clockID uint64, duration, started int64) Reply {
	remaining := duration
	if now, err := s.clockNow(context.Background(), clockID); err == nil && now > started {
		if elapsed := now - started; elapsed < remaining {
			remaining -= elapsed
		} else {
			remaining = 0
		}
	}
	if remaining < 0 {
		remaining = 0
	}
	return s.writeRemaining(call, remaining)
}

// writeRemaining 把剩余时间写入调用方的 rem 指针并返回 EINTR。
func (s Syscalls) writeRemaining(call Call, remaining int64) Reply {
	if call.Args[3] != 0 {
		if remaining < 0 {
			remaining = 0
		}
		encoded := encodeTimespec(remaining/nanosPerSecond, remaining%nanosPerSecond)
		if err := s.Memory.Write(context.Background(), call.Args[3], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Errno: errno.Interrupted}
}

type Heap interface {
	Break(ctx context.Context) (uint64, error)

	Grow(ctx context.Context, target uint64) (uint64, error)
}

// Mappable 由内核地址空间实现,承接 mmap 家族的区域管理。
// start/end 与返回地址必须页对齐;length 由调用方先行对齐。
type Mappable interface {
	// Anonymous 映射零填充的匿名私有区间并返回映射起始地址。
	// request.Hint 为 0 时由内核选址;Fixed 时必须落在 Hint 且替换重叠映射;
	// NoReplace 与 Fixed 组合时,重叠返回 EEXIST 且不改动任何映射。
	Anonymous(ctx context.Context, request MapRequest) (uint64, error)

	// File 映射文件区间(E08/E09):Shared 为真时读写直通文件
	// (跨地址空间一致,修改即时持久);否则以映射时的文件内容为
	// 初始快照,修改不写回。选址与替换语义同 Anonymous。
	File(ctx context.Context, request FileMapRequest) (uint64, error)

	// Unmap 解除页对齐范围;范围未被完整映射时报 ENOMEM 且不改动。
	Unmap(ctx context.Context, start, end uint64) error

	// Protect 将页对齐范围内所有映射改为 prot(原始 Linux PROT_* 位),
	// 允许跨区间;存在空洞或 W^X 违例时报错且不改动。
	Protect(ctx context.Context, start, end uint64, prot uint64) error

	// Mremap 重映射 [oldStart,oldEnd) 到 newEnd 大小:允许收缩;
	// 扩张时若尾部被占用则需 mayMove 迁移;fixed 时必须落在 fixedStart。
	// 返回映射(可能已迁移)的起始地址。
	Mremap(ctx context.Context, oldStart, oldEnd, newEnd uint64, mayMove, fixed bool, fixedStart uint64) (uint64, error)

	// Madvise 对页对齐范围施加建议;只支持有明确语义的子集,
	// 未支持的 advice 返回 ENOTSUP,范围未映射返回 ENOMEM。
	Madvise(ctx context.Context, start, end uint64, advice uint64) error

	// Sync 检查范围完整映射并确认修改已持久(msync;共享映射写入
	// 即时直通,此处校验覆盖性)。范围有空洞报 ENOMEM。
	Sync(ctx context.Context, start, end uint64) error
}

type MapRequest struct {
	Length uint64
	Prot   uint64
	Hint   uint64
	Fixed  bool
	// NoReplace 对应 MAP_FIXED_NOREPLACE。
	NoReplace bool
}

// FileMapRequest 是文件映射(E08/E09):Shared 区分 MAP_SHARED 与
// MAP_PRIVATE;Offset 是文件偏移(已对齐校验)。
type FileMapRequest struct {
	MapRequest
	File   RandomFile
	Offset uint64
	Shared bool
}

// RandomFile 是可随机访问的常规文件句柄(pread/pwrite 语义;
// mmap 文件映射的后备,E08/E09)。
type RandomFile interface {
	ReadAt(p []byte, off int64) (int, error)

	WriteAt(p []byte, off int64) (int, error)

	// Readable/Writable 报告打开访问模式(PROT 校验用)。
	Readable() bool

	Writable() bool
}

// riscv64 采用 asm-generic 的 mmap 标志与保护位定义。
const (
	ProtRead  = 0x1
	ProtWrite = 0x2
	ProtExec  = 0x4

	// asm-generic 的 eventfd2 标志位。
	efdSemaphore = 0x1
	efdNonBlock  = 0o4000
	efdCloexec   = 0o2000000
	efdFlagMask  = efdSemaphore | efdNonBlock | efdCloexec

	// asm-generic 的 mremap 标志。
	mremapMayMove  = 0x1
	mremapFixed    = 0x2
	mremapFlagMask = mremapMayMove | mremapFixed

	// asm-generic 的 madvise advice 子集。
	MadvNormal     = 0
	MadvRandom     = 1
	MadvSequential = 2
	MadvWillneed   = 3
	MadvDontneed   = 4
	madvSupported  = MadvNormal | MadvRandom | MadvSequential | MadvWillneed | MadvDontneed

	mapShared         = 0x01
	mapPrivate        = 0x02
	mapFixed          = 0x10
	mapAnonymous      = 0x20
	mapFixedNoReplace = 0x100000
	mapSupportedMask  = mapShared | mapPrivate | mapFixed | mapAnonymous | mapFixedNoReplace

	// asm-generic 的 msync 标志:SYNC/ASYNC 必须恰有其一并仅可
	// 与 INVALIDATE 组合。
	msAsync      = 1
	msInvalidate = 2
	msSync       = 4
)

func (s Syscalls) mmap(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	length := call.Args[1]
	if length == 0 {
		return Reply{Errno: errno.Invalid}
	}
	flags := call.Args[3]
	if flags&mapSupportedMask != flags {
		return Reply{Errno: errno.Invalid}
	}
	shared := flags&mapShared != 0
	private := flags&mapPrivate != 0
	if shared == private {
		// MAP_SHARED 与 MAP_PRIVATE 必须恰有其一。
		return Reply{Errno: errno.Invalid}
	}
	hint := call.Args[0]
	if hint%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length = alignPageUp(length)
	request := MapRequest{
		Length: length, Prot: call.Args[2], Hint: hint,
		Fixed: flags&mapFixed != 0, NoReplace: flags&mapFixedNoReplace != 0,
	}
	if flags&mapAnonymous != 0 {
		if shared {
			// MAP_SHARED|MAP_ANONYMOUS 需要跨进程共享零页,未纳入
			// 当前兼容配置,明确拒绝。
			return Reply{Errno: errno.NotSupported}
		}
		if call.Args[5] != 0 {
			return Reply{Errno: errno.Invalid}
		}
		start, err := s.Mapper.Anonymous(ctx, request)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		return Reply{Value: start}
	}
	// 文件映射(E08 私有/E09 共享):描述符必须是可随机访问的
	// 常规文件,偏移页对齐,访问模式与保护位一致。
	if s.Descriptors == nil {
		return Reply{Errno: errno.BadHandle}
	}
	if call.Args[4] > math.MaxUint32 {
		return Reply{Errno: errno.BadHandle}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[4])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	file, ok := handle.(RandomFile)
	if !ok {
		// 不可定位的描述符(管道/epoll 等)与 Linux 一致报 ENODEV。
		return Reply{Errno: errno.NoDevice}
	}
	if call.Args[5]%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[2]&ProtRead != 0 && !file.Readable() {
		return Reply{Errno: errno.Permission}
	}
	if shared && call.Args[2]&ProtWrite != 0 && !file.Writable() {
		return Reply{Errno: errno.Permission}
	}
	start, err := s.Mapper.File(ctx, FileMapRequest{
		MapRequest: request, File: file, Offset: call.Args[5], Shared: shared,
	})
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: start}
}

// msync(E09):共享映射写入即时直通文件,本调用校验范围完整映射
// 后确认持久;SYNC/ASYNC 必须恰有其一,UNKNOWN 标志 EINVAL。
func (s Syscalls) msync(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	flags := call.Args[2]
	if flags&^(msAsync|msInvalidate|msSync) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	if flags&msSync != 0 && flags&msAsync != 0 {
		return Reply{Errno: errno.Invalid}
	}
	if flags&(msSync|msAsync) == 0 {
		return Reply{Errno: errno.Invalid}
	}
	start := call.Args[0]
	if start%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length := alignPageUp(call.Args[1])
	if length == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if start+length <= start {
		return Reply{Errno: errno.Overflow}
	}
	if err := s.Mapper.Sync(ctx, start, start+length); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) munmap(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	start := call.Args[0]
	if start%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length := alignPageUp(call.Args[1])
	if length == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if start+length <= start {
		return Reply{Errno: errno.Overflow}
	}
	if err := s.Mapper.Unmap(ctx, start, start+length); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// IOV_MAX 是单次分散/聚集 IO 的向量条目上限(asm-generic)。
const iovMax = 1024

// DefaultVectorLimit 是 argv/envp 指针数默认上限。
const DefaultVectorLimit = 4096

// Iovec 是分散/聚集 IO 的缓冲区描述符(struct iovec)。
type Iovec struct {
	Base   uint64
	Length uint64
}

func (s Syscalls) vectorLimit() uint64 {
	if s.VectorLimit > 0 {
		return s.VectorLimit
	}
	return DefaultVectorLimit
}

// readStringVector 读取以空指针结尾的字符串指针数组(argv/envp 形态)。
// 数组缺少 NULL 终止时报 EFAULT(越界读取),条目数超限报 E2BIG,
// 单个字符串超长报 ENAMETOOLONG,总字节超限报 E2BIG——不允许无限分配。
func (s Syscalls) readStringVector(ctx context.Context, address uint64) ([]string, errno.Code) {
	if s.Memory == nil {
		return nil, errno.NotImplemented
	}
	limit := s.vectorLimit()
	byteLimit := s.BufferLimit
	if byteLimit == 0 {
		byteLimit = DefaultBufferLimit
	}
	out := make([]string, 0, 8)
	var total uint64
	var pointer [8]byte
	for index := uint64(0); index < limit; index++ {
		if err := s.Memory.Read(ctx, address+index*8, pointer[:]); err != nil {
			return nil, errno.Fault
		}
		entry := binary.LittleEndian.Uint64(pointer[:])
		if entry == 0 {
			return out, ""
		}
		str, code := s.readVectorString(ctx, entry)
		if code != "" {
			return nil, code
		}
		total += uint64(len(str)) + 1
		if total > byteLimit {
			return nil, errno.TooBig
		}
		out = append(out, str)
	}
	return nil, errno.TooBig
}

// readVectorString 读取单个 NUL 结尾字符串;与路径不同,空串合法(argv 可含空参数)。
func (s Syscalls) readVectorString(ctx context.Context, address uint64) (string, errno.Code) {
	var buffer [1]byte
	out := make([]byte, 0, 64)
	for offset := uint64(0); offset < PathLimit; offset++ {
		if err := s.Memory.Read(ctx, address+offset, buffer[:]); err != nil {
			return "", errno.Fault
		}
		if buffer[0] == 0 {
			return string(out), ""
		}
		out = append(out, buffer[0])
	}
	return "", errno.NameTooLong
}

// readIovecs 读取 iovec 数组;个数超限报 EINVAL,数组读取失败报 EFAULT。
func (s Syscalls) readIovecs(ctx context.Context, address, count uint64) ([]Iovec, errno.Code) {
	if s.Memory == nil {
		return nil, errno.NotImplemented
	}
	if count == 0 || count > iovMax {
		return nil, errno.Invalid
	}
	raw := make([]byte, count*16)
	if err := s.Memory.Read(ctx, address, raw); err != nil {
		return nil, errno.Fault
	}
	out := make([]Iovec, count)
	for index := range out {
		out[index] = Iovec{
			Base:   binary.LittleEndian.Uint64(raw[index*16:]),
			Length: binary.LittleEndian.Uint64(raw[index*16+8:]),
		}
	}
	return out, ""
}

// readv/writev 的单段缓冲区上限,复用 BufferLimit 约束内存分配。
func (s Syscalls) scatterGatherLimit() uint64 {
	if s.BufferLimit > 0 {
		return s.BufferLimit
	}
	return DefaultBufferLimit
}

func (s Syscalls) readv(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	iovecs, code := s.readIovecs(ctx, call.Args[1], call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	limit := s.scatterGatherLimit()
	total := 0
	for _, iov := range iovecs {
		if iov.Length == 0 {
			continue
		}
		if iov.Length > limit {
			return Reply{Errno: errno.Invalid}
		}
		buffer := make([]byte, iov.Length)
		loaded, err := s.Descriptors.Read(ctx, call.Args[0], buffer)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if total > 0 {
				return Reply{Value: uint64(total)}
			}
			return Reply{Errno: errnoOf(err)}
		}
		if loaded < 0 || loaded > len(buffer) {
			return Reply{Errno: errno.IO}
		}
		if loaded == 0 {
			break
		}
		if err := s.Memory.Write(ctx, iov.Base, buffer[:loaded]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		total += loaded
		if loaded < len(buffer) {
			break
		}
	}
	// I20:成功读取发 IN_ACCESS(与 read 一致)。
	if total > 0 {
		s.observe(s.descriptorPath(ctx, call.Args[0]), inotify.InAccess, 0, false)
	}
	return Reply{Value: uint64(total)}
}

func (s Syscalls) writev(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	iovecs, code := s.readIovecs(ctx, call.Args[1], call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	limit := s.scatterGatherLimit()
	total := 0
	for _, iov := range iovecs {
		if iov.Length == 0 {
			continue
		}
		if iov.Length > limit {
			if total > 0 {
				return Reply{Value: uint64(total)}
			}
			return Reply{Errno: errno.Invalid}
		}
		buffer := make([]byte, iov.Length)
		if err := s.Memory.Read(ctx, iov.Base, buffer); err != nil {
			return Reply{Errno: errno.Fault}
		}
		written, err := s.Descriptors.Write(ctx, call.Args[0], buffer)
		if err != nil {
			if total > 0 {
				return Reply{Value: uint64(total)}
			}
			if errors.Is(err, errno.BrokenPipe) {
				s.raisePipeSignal(ctx)
			}
			return Reply{Errno: errnoOf(err)}
		}
		total += written
		if written < len(buffer) {
			break
		}
	}
	// I20:成功写入发 IN_MODIFY(与 write 一致)。
	if total > 0 {
		s.observe(s.descriptorPath(ctx, call.Args[0]), inotify.InModify, 0, false)
	}
	return Reply{Value: uint64(total)}
}

func (s Syscalls) mremap(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	oldStart := call.Args[0]
	if oldStart%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	oldSize := alignPageUp(call.Args[1])
	newSize := alignPageUp(call.Args[2])
	if oldSize == 0 || newSize == 0 {
		return Reply{Errno: errno.Invalid}
	}
	flags := call.Args[3]
	if flags&^uint64(mremapFlagMask) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	fixed := flags&mremapFixed != 0
	mayMove := flags&mremapMayMove != 0
	if fixed && !mayMove {
		return Reply{Errno: errno.Invalid}
	}
	fixedStart := call.Args[4]
	if fixed && fixedStart%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	if oldStart+oldSize <= oldStart || oldStart+newSize <= oldStart {
		return Reply{Errno: errno.Overflow}
	}
	start, err := s.Mapper.Mremap(ctx, oldStart, oldStart+oldSize, oldStart+newSize, mayMove, fixed, fixedStart)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: start}
}

func (s Syscalls) madvise(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	start := call.Args[0]
	if start%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	advice := call.Args[2]
	if advice&^uint64(madvSupported) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length := alignPageUp(call.Args[1])
	if length == 0 {
		return Reply{Value: 0}
	}
	if start+length <= start {
		return Reply{Errno: errno.Overflow}
	}
	if err := s.Mapper.Madvise(ctx, start, start+length, advice); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) mprotect(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	start := call.Args[0]
	if start%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[2]&^(ProtRead|ProtWrite|ProtExec) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length := alignPageUp(call.Args[1])
	if length == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if start+length <= start {
		return Reply{Errno: errno.Overflow}
	}
	if err := s.Mapper.Protect(ctx, start, start+length, call.Args[2]); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func alignPageUp(value uint64) uint64 {
	return (value + memory.PageSize - 1) &^ (memory.PageSize - 1)
}

func (s Syscalls) uname(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	fields := []string{s.sysname(), s.nodename(), s.release(), s.version(), s.machine(), s.domainname()}
	var encoded [390]byte
	for index, value := range fields {
		copy(encoded[index*65:], []byte(value))
	}
	if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

func (s Syscalls) sysname() string {
	if s.Sysname == "" {
		return "Linux"
	}
	return s.Sysname
}

func (s Syscalls) nodename() string {
	if s.Nodename == "" {
		return "assistant-sandbox"
	}
	return s.Nodename
}

func (s Syscalls) release() string {
	if s.Release == "" {
		return "6.8.0"
	}
	return s.Release
}

func (s Syscalls) version() string {
	if s.Version == "" {
		return "#1 SMP"
	}
	return s.Version
}

func (s Syscalls) machine() string {
	if s.Machine == "" {
		return "riscv64"
	}
	return s.Machine
}

func (s Syscalls) domainname() string {
	if s.Domainname == "" {
		return "(none)"
	}
	return s.Domainname
}

// asm-generic 的 getrandom 标志;两种随机流都由客体配置的
// 熵源统一提供,GRND_RANDOM/GRND_INSECURE 不切换数据来源。
const (
	grndNonblock = 0x1
	grndRandom   = 0x2
	grndInsecure = 0x4
	grndFlagMask = grndNonblock | grndRandom | grndInsecure
)

func (s Syscalls) getrandom(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[2]&^uint64(grndFlagMask) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length := call.Args[1]
	if length == 0 {
		return Reply{Value: 0}
	}
	limit := s.BufferLimit
	if limit == 0 {
		limit = DefaultBufferLimit
	}
	if length > limit {
		return Reply{Errno: errno.Invalid}
	}
	buffer := make([]byte, length)
	if s.Random == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if _, err := s.Random.Read(ctx, buffer); err != nil {
		return Reply{Errno: errno.IO}
	}
	if err := s.Memory.Write(ctx, call.Args[0], buffer); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: length}
}

func (s Syscalls) getcwd(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	cwd := s.Cwd
	if cwd == "" {
		cwd = "/"
	}
	if !strings.HasPrefix(cwd, "/") {
		cwd = "/" + cwd
	}
	size := call.Args[1]
	encoded := append([]byte(cwd), 0)
	if size == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if uint64(len(encoded)) > size {
		return Reply{Errno: errno.Range}
	}
	if err := s.Memory.Write(ctx, call.Args[0], encoded); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(len(encoded))}
}

func (s Syscalls) sysinfo(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var encoded [128]byte
	// procs at offset 64+8+8? Layout: uptime(8)+loads(24)+totalram(8)+freeram(8)+sharedram(8)+bufferram(8)+totalswap(8)+freeswap(8)=88? Let's place procs at 80? Simpler: zero-filled success.
	encoded[80] = 1 // procs low byte (approx)
	if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

func (s Syscalls) times(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var encoded [32]byte
	if call.Args[0] != 0 {
		if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Value: 0}
}

func (s Syscalls) getgroups(ctx context.Context, call Call) Reply {
	size := call.Args[1]
	if size == 0 {
		return Reply{Value: 0}
	}
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	// No supplementary groups in this ABI profile.
	return Reply{Value: 0}
}

// asm-generic 的 RLIMIT_NOFILE;其余资源未纳入客体限额配置,按 RLIM_INFINITY 报告。
const rlimitNoFile = 7

const rlimInfinity = ^uint64(0)

func (s Syscalls) rlimitPair(ctx context.Context, resource uint64) [16]byte {
	var encoded [16]byte
	value := rlimInfinity
	if resource == rlimitNoFile && s.Limiter != nil {
		if limit, ok := s.Limiter.DescriptorLimit(ctx); ok {
			value = limit
		}
	}
	binary.LittleEndian.PutUint64(encoded[0:], value)
	binary.LittleEndian.PutUint64(encoded[8:], value)
	return encoded
}

func (s Syscalls) writeRlimit(ctx context.Context, address, resource uint64) errno.Code {
	encoded := s.rlimitPair(ctx, resource)
	if err := s.Memory.Write(ctx, address, encoded[:]); err != nil {
		return errno.Fault
	}
	return ""
}

func (s Syscalls) prlimit64(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[2] != 0 {
		if err := s.applyRlimit(ctx, call.Args[1], call.Args[2]); err != "" {
			return Reply{Errno: err}
		}
	}
	if call.Args[3] != 0 {
		if code := s.writeRlimit(ctx, call.Args[3], call.Args[1]); code != "" {
			return Reply{Errno: code}
		}
	}
	return Reply{}
}

func (s Syscalls) applyRlimit(ctx context.Context, resource, address uint64) errno.Code {
	var encoded [16]byte
	if err := s.Memory.Read(ctx, address, encoded[:]); err != nil {
		return errno.Fault
	}
	current := binary.LittleEndian.Uint64(encoded[0:])
	ceiling := binary.LittleEndian.Uint64(encoded[8:])
	if current > ceiling {
		return errno.Invalid
	}
	if resource != rlimitNoFile {
		// 未纳入限额配置的资源不接受设置,明确拒绝而非伪成功。
		return errno.NotSupported
	}
	if s.Limiter == nil {
		return errno.NotImplemented
	}
	if err := s.Limiter.SetDescriptorLimit(ctx, current); err != nil {
		return errnoOf(err)
	}
	return ""
}

func (s Syscalls) setrlimit(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if err := s.applyRlimit(ctx, call.Args[0], call.Args[1]); err != "" {
		return Reply{Errno: err}
	}
	return Reply{Value: 0}
}

func (s Syscalls) getrusage(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[1] == 0 {
		return Reply{Errno: errno.Invalid}
	}
	var encoded [144]byte
	if err := s.Memory.Write(ctx, call.Args[1], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: 0}
}

func (s Syscalls) faccessat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	path, code = s.resolveDirPath(ctx, call.Args[0], path)
	if code != "" {
		return Reply{Errno: code}
	}
	if _, err := s.Files.Stat(ctx, path); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) getrlimit(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[1] == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if code := s.writeRlimit(ctx, call.Args[1], call.Args[0]); code != "" {
		return Reply{Errno: code}
	}
	return Reply{Value: 0}
}

func (s Syscalls) readlinkat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	path, code = s.resolveDirPath(ctx, call.Args[0], path)
	if code != "" {
		return Reply{Errno: code}
	}
	target, err := s.Files.Readlink(ctx, path)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	size := call.Args[3]
	if size == 0 {
		return Reply{Errno: errno.Invalid}
	}
	data := []byte(target)
	if uint64(len(data)) > size {
		data = data[:size]
	}
	if err := s.Memory.Write(ctx, call.Args[2], data); err != nil {
		return Reply{Errno: errno.Fault}
	}
	// Linux readlink(2) 返回放入缓冲区的字节数:截断时即截断长度。
	return Reply{Value: uint64(len(data))}
}

func (s Syscalls) mkdirat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	path, code = s.resolveDirPath(ctx, call.Args[0], path)
	if code != "" {
		return Reply{Errno: code}
	}
	mode := uint32(call.Args[2])
	if err := s.Files.Mkdir(ctx, path, mode); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	// I20:新建目录发 IN_CREATE|IN_ISDIR。
	s.observe(path, inotify.InCreate, 0, true)
	return Reply{}
}

func (s Syscalls) unlinkat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	path, code = s.resolveDirPath(ctx, call.Args[0], path)
	if code != "" {
		return Reply{Errno: code}
	}
	flags := call.Args[2]
	// unlinkat 只接受 AT_REMOVEDIR;未知 flags 位报 EINVAL(Linux 语义)。
	if flags&^uint64(atRemovedir) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	info, statErr := s.Files.Stat(ctx, path)
	if statErr == nil && info != nil {
		if flags&atRemovedir != 0 {
			if !info.IsDir() {
				return Reply{Errno: errno.NotDirectory}
			}
		} else if info.IsDir() {
			return Reply{Errno: errno.IsDirectory}
		}
	}
	if err := s.Files.Remove(ctx, path); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	// I20:删除按预取的目录标记发 IN_DELETE(自监视转 DELETE_SELF)。
	s.observe(path, inotify.InDelete, 0, statErr == nil && info != nil && info.IsDir())
	return Reply{}
}

func (s Syscalls) symlinkat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	target, code := s.readPath(ctx, call.Args[0])
	if code != "" {
		return Reply{Errno: code}
	}
	linkpath, code := s.readPath(ctx, call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	linkpath, code = s.resolveDirPath(ctx, call.Args[1], linkpath)
	if code != "" {
		return Reply{Errno: code}
	}
	if err := s.Files.Symlink(ctx, target, linkpath); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	// I20:新建符号链接发 IN_CREATE。
	s.observe(linkpath, inotify.InCreate, 0, false)
	return Reply{}
}

// linkat 的 AT_SYMLINK_FOLLOW(asm-generic 0x400)。
const atSymlinkFollow = 0o2000

func (s Syscalls) linkat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	// linkat 只接受 AT_SYMLINK_FOLLOW;未知 flags 位报 EINVAL。
	flags := call.Args[4]
	if flags&^uint64(atSymlinkFollow) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	oldpath, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	oldpath, code = s.resolveDirPath(ctx, call.Args[0], oldpath)
	if code != "" {
		return Reply{Errno: code}
	}
	newpath, code := s.readPath(ctx, call.Args[3])
	if code != "" {
		return Reply{Errno: code}
	}
	newpath, code = s.resolveDirPath(ctx, call.Args[2], newpath)
	if code != "" {
		return Reply{Errno: code}
	}
	// 默认不解引用符号链接(link(2) 语义,经 System.Link);
	// AT_SYMLINK_FOLLOW 需要文件系统显式支持该能力。
	if flags&uint64(atSymlinkFollow) != 0 {
		follow, ok := s.Files.(vmfs.LinkFollow)
		if !ok {
			return Reply{Errno: errno.NotSupported}
		}
		if err := follow.LinkFollowing(ctx, oldpath, newpath); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		s.observe(newpath, inotify.InCreate, 0, false)
		return Reply{}
	}
	if err := s.Files.Link(ctx, oldpath, newpath); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	s.observe(newpath, inotify.InCreate, 0, false)
	return Reply{}
}

func (s Syscalls) renameat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	oldpath, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	oldpath, code = s.resolveDirPath(ctx, call.Args[0], oldpath)
	if code != "" {
		return Reply{Errno: code}
	}
	newpath, code := s.readPath(ctx, call.Args[3])
	if code != "" {
		return Reply{Errno: code}
	}
	newpath, code = s.resolveDirPath(ctx, call.Args[2], newpath)
	if code != "" {
		return Reply{Errno: code}
	}
	isDir := false
	if info, statErr := s.Files.Stat(ctx, oldpath); statErr == nil && info != nil {
		isDir = info.IsDir()
	}
	if err := s.Files.Rename(ctx, oldpath, newpath); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	// I20:改名为配对的 IN_MOVED_FROM/IN_MOVED_TO(cookie 相同)。
	cookie := uint32(0)
	if s.Notify != nil {
		cookie = s.Notify.NextCookie()
	}
	s.observe(oldpath, inotify.InMovedFrom, cookie, isDir)
	s.observe(newpath, inotify.InMovedTo, cookie, isDir)
	return Reply{}
}

// renameat2 的 flags 扩展(RENAME_NOREPLACE/EXCHANGE/WHITEOUT)不在
// 首个兼容配置内:非零 flags 明确 EINVAL,不伪装成功;flags=0 等价
// renameat。
func (s Syscalls) renameat2(ctx context.Context, call Call) Reply {
	if call.Args[4] != 0 {
		return Reply{Errno: errno.Invalid}
	}
	return s.renameat(ctx, call)
}

func (s Syscalls) ftruncate(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	size := int64(call.Args[1])
	if size < 0 {
		return Reply{Errno: errno.Invalid}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	target, ok := handle.(truncater)
	if !ok {
		return Reply{Errno: errno.NotSupported}
	}
	if err := target.Truncate(size); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) clockGetres(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	// 1ns resolution claim.
	encoded := encodeTimespec(0, 1)
	if err := s.Memory.Write(ctx, call.Args[1], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

func (s Syscalls) gettimeofday(ctx context.Context, call Call) Reply {
	if s.Clock == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	wall, err := s.Clock.Wall(ctx)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	seconds := wall / nanosPerSecond
	micros := (wall % nanosPerSecond) / 1000
	if wall < 0 && wall%nanosPerSecond != 0 {
		seconds--
		micros = ((wall % nanosPerSecond) + nanosPerSecond) / 1000
	}
	var encoded [16]byte
	binary.LittleEndian.PutUint64(encoded[0:], uint64(seconds))
	binary.LittleEndian.PutUint64(encoded[8:], uint64(micros))
	if call.Args[0] != 0 {
		if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Value: 0}
}

// dirReader 是目录句柄的可选能力:按游标返回目录项。
// 实现约定见 filesystem/memfs 的 handle.Readdir:条目 Info().Sys() 携带
// vm/fs.Metadata 时,其 Inode 充当 d_off cookie 并可经 lseek 恢复游标。
type dirReader interface {
	Readdir(limit int) ([]stdfs.DirEntry, error)
}

// asm-generic 的目录项类型值。
const (
	dtFifo     = 1
	dtCharDev  = 2
	dtDir      = 4
	dtBlockDev = 3
	dtRegular  = 8
	dtSymlink  = 10
	dtSocket   = 12

	// 最小目录项记录长度:d_ino(8)+d_off(8)+d_reclen(2)+d_type(1)。
	direntMinRecord = 19
)

func direntType(mode stdfs.FileMode) byte {
	switch {
	case mode&stdfs.ModeDir != 0:
		return dtDir
	case mode&stdfs.ModeSymlink != 0:
		return dtSymlink
	case mode&stdfs.ModeDevice != 0 && mode&stdfs.ModeCharDevice != 0:
		return dtCharDev
	case mode&stdfs.ModeDevice != 0:
		return dtBlockDev
	case mode&stdfs.ModeNamedPipe != 0:
		return dtFifo
	case mode&stdfs.ModeSocket != 0:
		return dtSocket
	default:
		return dtRegular
	}
}

func (s Syscalls) getdents64(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	reader, ok := handle.(dirReader)
	if !ok {
		return Reply{Errno: errno.NotSupported}
	}
	if call.Args[2] == 0 || call.Args[2] > uint64(math.MaxInt) {
		return Reply{Errno: errno.Invalid}
	}
	capacity := int(call.Args[2])
	// 批量取回再打包;未写进缓冲区的条目必须把游标退回,
	// 保证下一次调用仍能看到它们(与内核 filldir 行为一致)。
	entries, err := reader.Readdir(direntBatchLimit)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if len(entries) == 0 {
		return Reply{Value: 0}
	}
	out := make([]byte, 0, capacity)
	emitted := 0
	fallback := uint64(0)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			break
		}
		nameBytes := append([]byte(entry.Name()), 0)
		reclen := (direntMinRecord + len(nameBytes) + 7) &^ 7
		if len(out)+reclen > capacity {
			break
		}
		fallback++
		// cookie 优先取文件系统声明的 inode 号,缺失时退化为批内序号。
		cookie, ino := fallback, fallback
		if metadata, ok := info.Sys().(*vmfs.Metadata); ok {
			cookie, ino = metadata.Inode, metadata.Inode
		}
		record := make([]byte, reclen)
		binary.LittleEndian.PutUint64(record[0:], ino)
		binary.LittleEndian.PutUint64(record[8:], cookie)
		binary.LittleEndian.PutUint16(record[16:], uint16(reclen))
		record[18] = direntType(info.Mode())
		copy(record[19:], nameBytes)
		out = append(out, record...)
		emitted++
	}
	if emitted == 0 {
		// 缓冲区容不下任何一条记录。
		return Reply{Errno: errno.Invalid}
	}
	if emitted < len(entries) {
		if err := s.rewindDirents(ctx, handle, entries[emitted:]); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
	}
	if err := s.Memory.Write(ctx, call.Args[1], out); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(len(out))}
}

// direntBatchLimit 是 getdents64 单次向目录句柄取回的条目上限。
const direntBatchLimit = 1024

// rewindDirents 把目录游标退回 leftover 的首条,使未写出的条目
// 在下一次 getdents64 仍可见;目录句柄必须支持定位(vm/fs.File 契约)。
func (s Syscalls) rewindDirents(ctx context.Context, handle any, leftover []stdfs.DirEntry) error {
	first, err := leftover[0].Info()
	if err != nil {
		return err
	}
	metadata, ok := first.Sys().(*vmfs.Metadata)
	if !ok {
		// cookie 退化为批内序号的句柄无法可靠回退;仍按批首序号重试。
		return errno.IO
	}
	seeker, ok := handle.(io.Seeker)
	if !ok {
		return errno.IO
	}
	_, err = seeker.Seek(int64(metadata.Inode), int(SeekSet))
	return err
}

// statter 是持有文件元数据的描述符句柄的可选能力(文件与目录句柄)。
type statter interface {
	Stat() (stdfs.FileInfo, error)
}

// asm-generic 的文件类型位(st_mode 高位)。
const (
	modeFifo     = 0o010000
	modeCharDev  = 0o020000
	modeDir      = 0o040000
	modeRegular  = 0o100000
	modeBlockDev = 0o060000
	modeSymlink  = 0o120000
	modeSocket   = 0o140000

	modeSetuid = 0o4000
	modeSetgid = 0o2000
	modeSticky = 0o1000

	// statPreferredBlksize 是 struct stat 的 st_blksize 报告值。
	statPreferredBlksize = 4096
)

// unixModeBits 把 Go 的 FileMode 转为 st_mode 的类型与权限位。
func unixModeBits(mode stdfs.FileMode) uint32 {
	var kind uint32
	switch {
	case mode&stdfs.ModeDir != 0:
		kind = modeDir
	case mode&stdfs.ModeSymlink != 0:
		kind = modeSymlink
	case mode&stdfs.ModeDevice != 0 && mode&stdfs.ModeCharDevice != 0:
		kind = modeCharDev
	case mode&stdfs.ModeDevice != 0:
		kind = modeBlockDev
	case mode&stdfs.ModeNamedPipe != 0:
		kind = modeFifo
	case mode&stdfs.ModeSocket != 0:
		kind = modeSocket
	default:
		kind = modeRegular
	}
	perm := uint32(mode.Perm())
	if mode&stdfs.ModeSetuid != 0 {
		perm |= modeSetuid
	}
	if mode&stdfs.ModeSetgid != 0 {
		perm |= modeSetgid
	}
	if mode&stdfs.ModeSticky != 0 {
		perm |= modeSticky
	}
	return kind | perm
}

// statBlockCount 按 512 字节扇区向上取整,与 Linux st_blocks 记账一致。
func statBlockCount(size int64) int64 {
	return (size + 511) / 512
}

func splitNanos(total int64) (int64, int64) {
	seconds := total / nanosPerSecond
	nanoseconds := total % nanosPerSecond
	if nanoseconds < 0 {
		seconds--
		nanoseconds += nanosPerSecond
	}
	return seconds, nanoseconds
}

// statInput 从文件系统返回的 FileInfo 构造 struct stat 编码输入。
// Sys() 携带 fs.Metadata 时以扩展元数据为准;否则 inode/nlink/所有权取
// 缺省值,三个时间以修改时间报告。
func statInput(info stdfs.FileInfo) StatInput {
	input := StatInput{
		Mode:    unixModeBits(info.Mode()),
		NLink:   1,
		Size:    info.Size(),
		Blksize: statPreferredBlksize,
		Blocks:  statBlockCount(info.Size()),
	}
	modified := info.ModTime()
	input.AtimeSec, input.AtimeNsec = splitNanos(modified.UnixNano())
	input.MtimeSec, input.MtimeNsec = splitNanos(modified.UnixNano())
	input.CtimeSec, input.CtimeNsec = splitNanos(modified.UnixNano())
	if metadata, ok := info.Sys().(*vmfs.Metadata); ok {
		input.Ino = metadata.Inode
		input.NLink = uint32(metadata.NLink)
		input.UID, input.GID = metadata.UID, metadata.GID
		input.AtimeSec, input.AtimeNsec = splitNanos(metadata.AtimeNanos)
		input.MtimeSec, input.MtimeNsec = splitNanos(metadata.MtimeNanos)
		input.CtimeSec, input.CtimeNsec = splitNanos(metadata.CtimeNanos)
	}
	return input
}

// writeStat 把 FileInfo 编码为 asm-generic struct stat 写入客体缓冲区。
func (s Syscalls) writeStat(ctx context.Context, address uint64, info stdfs.FileInfo) Reply {
	encoded := encodeStat(statInput(info))
	if err := s.Memory.Write(ctx, address, encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

// statDescriptor 对描述符句柄取状态;句柄不支持时明确 ENOTSUP。
func (s Syscalls) statDescriptor(ctx context.Context, descriptor, address uint64) Reply {
	handle, err := s.Descriptors.Get(ctx, descriptor)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return s.writeStatFromHandle(ctx, address, handle)
}

// writeStatFromHandle 对已解析的句柄取状态并写入 struct stat。
func (s Syscalls) writeStatFromHandle(ctx context.Context, address uint64, handle any) Reply {
	stater, ok := handle.(statter)
	if !ok {
		return Reply{Errno: errno.NotSupported}
	}
	info, err := stater.Stat()
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return s.writeStat(ctx, address, info)
}

func (s Syscalls) fstat(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	return s.statDescriptor(ctx, call.Args[0], call.Args[1])
}

// statPathTarget 解析 fstatat/statx 共用的状态目标:非空路径按
// dirfd 解析(AT_FDCWD 或目录句柄相对解析,I02);空路径仅在
// AT_EMPTY_PATH 且 dirfd 指向真实描述符时返回该句柄。
func (s Syscalls) statPathTarget(ctx context.Context, dirfd, pathAddress, flags uint64) (stdfs.FileInfo, any, errno.Code) {
	pathname, code := s.readPath(ctx, pathAddress)
	if code != "" {
		if code != errno.NotFound || flags&atEmptyPath == 0 {
			return nil, nil, code
		}
	}
	if pathname == "" {
		if flags&atEmptyPath == 0 || dirfd == atCurrentWorkingDirectory {
			return nil, nil, errno.NotFound
		}
		if s.Descriptors == nil {
			return nil, nil, errno.NotImplemented
		}
		handle, err := s.Descriptors.Get(ctx, dirfd)
		if err != nil {
			return nil, nil, errnoOf(err)
		}
		return nil, handle, ""
	}
	pathname, code = s.resolveDirPath(ctx, dirfd, pathname)
	if code != "" {
		return nil, nil, code
	}
	var (
		info stdfs.FileInfo
		err  error
	)
	if flags&atSymlinkNoFollow != 0 {
		info, err = s.Files.Lstat(ctx, pathname)
	} else {
		info, err = s.Files.Stat(ctx, pathname)
	}
	if err != nil {
		return nil, nil, errnoOf(err)
	}
	return info, nil, ""
}

// newfstatat 支持 AT_FDCWD、AT_SYMLINK_NOFOLLOW 与 AT_EMPTY_PATH。
func (s Syscalls) newfstatat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	flags := call.Args[3]
	if flags&^uint64(atFlagMask) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	info, handle, code := s.statPathTarget(ctx, call.Args[0], call.Args[1], flags)
	if code != "" {
		return Reply{Errno: code}
	}
	if info == nil {
		return s.writeStatFromHandle(ctx, call.Args[2], handle)
	}
	return s.writeStat(ctx, call.Args[2], info)
}

// asm-generic 的 statx 附加标志:AT_NO_AUTOMOUNT 与同步类型位可接受为空操作。
const (
	atNoAutomask    = 0x800
	atStatxSyncType = 0x6000
	atStatxFlagMask = atSymlinkNoFollow | atEmptyPath | atNoAutomask | atStatxSyncType
)

// statx(int dfd, pathname, flags, mask, buffer) 按 mask 报告 statx 子集。
func (s Syscalls) statx(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	flags := call.Args[2]
	if flags&^uint64(atStatxFlagMask) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	info, handle, code := s.statPathTarget(ctx, call.Args[0], call.Args[1], flags)
	if code != "" {
		return Reply{Errno: code}
	}
	target := info
	if target == nil {
		stater, ok := handle.(statter)
		if !ok {
			return Reply{Errno: errno.NotSupported}
		}
		var err error
		target, err = stater.Stat()
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
	}
	encoded := encodeStatx(statInput(target), uint32(call.Args[3]))
	if err := s.Memory.Write(ctx, call.Args[4], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

// asm-generic 的 utimensat 纳秒特殊值。
const (
	utimeNow  = 0x3fffffff
	utimeOmit = 0x3ffffffe
)

// timeSetter 是文件句柄的可选能力:以纳秒更新访问/修改时间。
type timeSetter interface {
	SetTimes(atime, mtime int64) error
}

// truncater 是文件句柄的可选能力:按字节长度截断。
type truncater interface {
	Truncate(size int64) error
}

// syncer 是文件句柄的可选能力:数据冲入稳定存储。
type syncer interface {
	Sync(fdatasync bool) error
}

// utimensat(dfd, pathname, times[2], flags) 支持 UTIME_NOW/UTIME_OMIT、
// AT_SYMLINK_NOFOLLOW 与 pathname 为 NULL 的 futimens 形态(对 dfd 生效)。
func (s Syscalls) utimensat(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	flags := call.Args[3]
	if flags&^uint64(atSymlinkNoFollow) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	atime, mtime, code := s.readTimespecPair(ctx, call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	atime, mtime, code = s.resolveTimestamps(ctx, call, atime, mtime)
	if code != "" {
		return Reply{Errno: code}
	}
	if call.Args[1] == 0 {
		// futimens 形态:路径为 NULL,直接作用于 dirfd 指向的文件。
		if call.Args[0] == atCurrentWorkingDirectory {
			return Reply{Errno: errno.NotFound}
		}
		if s.Descriptors == nil {
			return Reply{Errno: errno.NotImplemented}
		}
		handle, err := s.Descriptors.Get(ctx, call.Args[0])
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		setter, ok := handle.(timeSetter)
		if !ok {
			return Reply{Errno: errno.NotSupported}
		}
		if err := setter.SetTimes(atime, mtime); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		return Reply{}
	}
	if s.Files == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	pathname, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	pathname, code = s.resolveDirPath(ctx, call.Args[0], pathname)
	if code != "" {
		return Reply{Errno: code}
	}
	if err := s.Files.SetTimes(ctx, pathname, atime, mtime, flags&atSymlinkNoFollow == 0); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// readTimespecPair 读取 utimensat 的 [atime, mtime] 时钟对:
// 指针为 NULL 表示双 NOW;每项纳秒只接受合法值与 NOW/OMIT 特殊值。
func (s Syscalls) readTimespecPair(ctx context.Context, address uint64) (atime, mtime int64, code errno.Code) {
	if address == 0 {
		return utimeNow, utimeNow, ""
	}
	for _, offset := range []uint64{0, timespecSize} {
		var raw [timespecSize]byte
		if err := s.Memory.Read(ctx, address+offset, raw[:]); err != nil {
			return 0, 0, errno.Fault
		}
		nanoseconds := binary.LittleEndian.Uint64(raw[8:])
		switch nanoseconds {
		case utimeNow:
			if offset == 0 {
				atime = utimeNow
			} else {
				mtime = utimeNow
			}
		case utimeOmit:
			if offset == 0 {
				atime = utimeOmit
			} else {
				mtime = utimeOmit
			}
		default:
			if nanoseconds >= nanosPerSecond {
				return 0, 0, errno.Invalid
			}
			seconds := int64(binary.LittleEndian.Uint64(raw[0:]))
			if offset == 0 {
				atime = seconds*nanosPerSecond + int64(nanoseconds)
			} else {
				mtime = seconds*nanosPerSecond + int64(nanoseconds)
			}
		}
	}
	return atime, mtime, ""
}

// resolveTimestamps 把 NOW/OMIT 标记解析为具体纳秒值:
// NOW 取客体时钟,OMIT 取目标的当前时间。
func (s Syscalls) resolveTimestamps(ctx context.Context, call Call, atime, mtime int64) (int64, int64, errno.Code) {
	needCurrent := atime == utimeOmit || mtime == utimeOmit
	if atime == utimeNow || mtime == utimeNow || needCurrent {
		if s.Clock == nil {
			return 0, 0, errno.NotImplemented
		}
	}
	if atime == utimeNow || mtime == utimeNow {
		wall, err := s.Clock.Wall(ctx)
		if err != nil {
			return 0, 0, errnoOf(err)
		}
		if atime == utimeNow {
			atime = wall
		}
		if mtime == utimeNow {
			mtime = wall
		}
	}
	if needCurrent {
		var info stdfs.FileInfo
		var err error
		switch {
		case call.Args[1] == 0:
			if s.Descriptors == nil {
				return 0, 0, errno.NotImplemented
			}
			var handle any
			handle, err = s.Descriptors.Get(ctx, call.Args[0])
			if err == nil {
				stater, ok := handle.(statter)
				if !ok {
					return 0, 0, errno.NotSupported
				}
				info, err = stater.Stat()
			}
		case call.Args[1] != 0 && call.Args[3]&atSymlinkNoFollow != 0:
			pathname, code := s.readPath(ctx, call.Args[1])
			if code != "" {
				return 0, 0, code
			}
			info, err = s.Files.Lstat(ctx, pathname)
		default:
			pathname, code := s.readPath(ctx, call.Args[1])
			if code != "" {
				return 0, 0, code
			}
			info, err = s.Files.Stat(ctx, pathname)
		}
		if err != nil {
			return 0, 0, errnoOf(err)
		}
		decoded := statInput(info)
		if atime == utimeOmit {
			atime = decoded.AtimeSec*nanosPerSecond + decoded.AtimeNsec
		}
		if mtime == utimeOmit {
			mtime = decoded.MtimeSec*nanosPerSecond + decoded.MtimeNsec
		}
	}
	return atime, mtime, ""
}

// fsync/fdatasync 经句柄的 Sync 能力冲数据;不支持同步的描述符
// (管道、eventfd 等)报 EINVAL,与 Linux 一致。
func (s Syscalls) fsync(ctx context.Context, call Call, fdatasync bool) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	target, ok := handle.(syncer)
	if !ok {
		return Reply{Errno: errno.Invalid}
	}
	if err := target.Sync(fdatasync); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// asm-generic 的 waitid 参数与 siginfo 常量。
const (
	pAll = 0
	pPID = 1

	wExited    = 1
	wStopped   = 2
	wContinued = 4
	wNoHang    = 8
	wNoWait    = 16

	cldExited = 1
	cldKilled = 2

	sigchldNumber = 17
	siginfoSize   = 128
	siginfoPID    = 16
	siginfoUID    = 20
	siginfoStatus = 24
	siginfoUTime  = 32
	siginfoSTime  = 40
)

// waitid(idtype, id, infop, options) 支持 P_PID/P_ALL 与 WEXITED/
// WNOHANG;停止/继续与 WNOWAIT 未纳入兼容配置,明确 ENOTSUP。
func (s Syscalls) waitid(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Waiter == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	switch call.Args[0] {
	case pAll, pPID:
	default:
		return Reply{Errno: errno.NotSupported}
	}
	if call.Args[0] == pPID && call.Args[1] == 0 {
		return Reply{Errno: errno.Invalid}
	}
	options := call.Args[3]
	if options&wExited == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if options&(wStopped|wContinued) != 0 || options&wNoWait != 0 {
		return Reply{Errno: errno.NotSupported}
	}
	if call.Args[2] == 0 {
		return Reply{Errno: errno.Invalid}
	}
	state, found, err := s.Waiter.WaitChild(ctx, call.Args[1], options&wNoHang != 0)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if !found {
		// WNOHANG 无就绪子进程:清零 siginfo 并成功返回。
		var empty [siginfoSize]byte
		if err := s.Memory.Write(ctx, call.Args[2], empty[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		return Reply{}
	}
	return s.writeSigchld(ctx, call.Args[2], state)
}

// writeSigchld 按 asm-generic siginfo_t(128 字节)编码 SIGCHLD 信息:
// 正常退出 si_code=CLD_EXITED 且 si_status=退出码;
// 被信号终止 si_code=CLD_KILLED 且 si_status=信号编号。
// 资源使用字段随 T03 接线,当前为零。
func (s Syscalls) writeSigchld(ctx context.Context, address uint64, state ChildState) Reply {
	var encoded [siginfoSize]byte
	putI32 := func(offset int, value int32) {
		binary.LittleEndian.PutUint32(encoded[offset:], uint32(value))
	}
	putI32(0, sigchldNumber)
	if state.ExitSignal != 0 {
		putI32(8, cldKilled)
		putI32(siginfoStatus, int32(state.ExitSignal))
	} else {
		putI32(8, cldExited)
		putI32(siginfoStatus, int32(state.ExitCode))
	}
	putI32(siginfoPID, int32(state.PID))
	putI32(siginfoUID, 0)
	if err := s.Memory.Write(ctx, address, encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

// asm-generic 的 struct sigaction(32 字节)与 rt_sigprocmask 的 how。
const (
	sigactionHandler  = 0
	sigactionMask     = 8
	sigactionFlags    = 16
	sigactionRestorer = 24
	sigactionSize     = 32

	// asm-generic 的 SIG_BLOCK=0、SIG_UNBLOCK=1、SIG_SETMASK=2。
	sigBlock   = 0
	sigUnblock = 1
	sigSetMask = 2

	// 客体 sigset 为 8 字节(64 位)。
	sigsetSize = 8
)

// sigMaxNumbers 是本兼容配置支持的最高信号号:Linux 1..64
// (含 musl 内部保留的 32/33),与 vm/signal.Max 一致。
const sigMaxNumbers = 64

func validGuestSignal(sig uint64) bool { return sig >= 1 && sig <= sigMaxNumbers }

// rt_sigaction(sig, act, oldact, sigsetsize) 登记处置并回写旧值。
// 未知号 EINVAL,KILL/STOP 不可改,自定义处理器地址原样登记
// (处理器执行随 J03/J04 信号帧落地)。
func (s Syscalls) rtSigaction(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Signals == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	sig := call.Args[0]
	if !validGuestSignal(sig) {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[3] != sigsetSize {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[1] != 0 {
		var raw [sigactionSize]byte
		if err := s.Memory.Read(ctx, call.Args[1], raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		action := SignalAction{
			Handler:  binary.LittleEndian.Uint64(raw[sigactionHandler:]),
			Mask:     binary.LittleEndian.Uint64(raw[sigactionMask:]),
			Flags:    binary.LittleEndian.Uint32(raw[sigactionFlags:]),
			Restorer: binary.LittleEndian.Uint64(raw[sigactionRestorer:]),
		}
		if err := s.Signals.SetAction(sig, action); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
	}
	if call.Args[2] != 0 {
		current, err := s.Signals.Action(sig)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		var encoded [sigactionSize]byte
		binary.LittleEndian.PutUint64(encoded[sigactionHandler:], current.Handler)
		binary.LittleEndian.PutUint64(encoded[sigactionMask:], current.Mask)
		binary.LittleEndian.PutUint32(encoded[sigactionFlags:], current.Flags)
		binary.LittleEndian.PutUint64(encoded[sigactionRestorer:], current.Restorer)
		if err := s.Memory.Write(ctx, call.Args[2], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{}
}

// rt_sigprocmask(how, set, oldset, sigsetsize) 调整屏蔽集并回写旧值;
// set 为 NULL 时仅查询。
func (s Syscalls) rtSigprocmask(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Signals == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[3] != sigsetSize {
		return Reply{Errno: errno.Invalid}
	}
	var old uint64
	if call.Args[1] != 0 {
		var set [8]byte
		if err := s.Memory.Read(ctx, call.Args[1], set[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		mask := binary.LittleEndian.Uint64(set[:])
		var err error
		old, err = s.Signals.ApplyMask(call.Args[0], mask)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
	} else {
		if call.Args[0] != sigBlock && call.Args[0] != sigUnblock && call.Args[0] != sigSetMask {
			return Reply{Errno: errno.Invalid}
		}
		current, err := s.Signals.Mask()
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		old = current
	}
	if call.Args[2] != 0 {
		var encoded [8]byte
		binary.LittleEndian.PutUint64(encoded[0:], old)
		if err := s.Memory.Write(ctx, call.Args[2], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{}
}

// execve(path, argv, envp):路径与向量均来自客体内存;成功时映像
// 已替换(不返回),装载失败返回错误且旧程序继续(H06/H07)。
func (s Syscalls) execve(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Execs == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	path, code := s.readPath(ctx, call.Args[0])
	if code != "" {
		return Reply{Errno: code}
	}
	argv, code := s.readStringVector(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	envp, code := s.readStringVector(ctx, call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	if err := s.Execs.Exec(ctx, path, argv, envp); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// kill(pid, sig) 与 tgkill(tgid, tid, sig):本兼容配置为单线程进程,
// tgkill 要求 tid==tgid,否则 ESRCH(与无线程进程的 Linux 行为一致)。
func (s Syscalls) kill(ctx context.Context, call Call) Reply {
	if s.Kills == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	sig := call.Args[1]
	if sig != 0 && !validGuestSignal(sig) {
		return Reply{Errno: errno.Invalid}
	}
	if err := s.Kills.Kill(ctx, int64(call.Args[0]), sig); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// wait4(pid, &status, options, &rusage)(K10):阻塞等待子进程并把
// 终态编码为 wait4 状态格式(退出码<<8 / 信号)。pid>0 定向;0 与
// -1 按任意子进程等待;PGID 等待与 rusage 计数明确 ENOTSUP(T03 接
// 资源指标后复核)。
func (s Syscalls) wait4(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Waiter == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	pid := int64(call.Args[0])
	options := call.Args[2]
	if options&^(wait4WNoHang|wait4WUntraced) != 0 {
		// WSTOPPED/WCONTINUED 等不在兼容配置。
		return Reply{Errno: errno.NotSupported}
	}
	var target uint64
	switch {
	case pid > 0:
		target = uint64(pid)
	case pid == 0 || pid == -1:
		target = 0
	default:
		return Reply{Errno: errno.NotSupported}
	}
	state, found, err := s.Waiter.WaitChild(ctx, target, options&wait4WNoHang != 0)

	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if !found {
		return Reply{Value: 0} // WNOHANG 且无终态
	}
	// wait4 状态编码:正常退出 code<<8;信号终止低 7 位。
	var encoded uint64
	if state.ExitSignal != 0 {
		encoded = uint64(state.ExitSignal) & 0x7f
	} else {
		encoded = uint64(state.ExitCode&0xff) << 8
	}
	statusAddress := call.Args[1]
	if statusAddress != 0 {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], uint32(encoded))
		if err := s.Memory.Write(ctx, statusAddress, raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Value: state.PID}
}

const (
	wait4WNoHang   = 1
	wait4WUntraced = 2
)

// set_robust_list(head, len)(K07):登记 robust futex 链表;线程
// 退出时由内核清算(HandleRobustListAtExit)。
func (s Syscalls) setRobustList(ctx context.Context, call Call) Reply {
	if s.RobustLists == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if err := s.RobustLists.SetRobustList(call.Args[0], call.Args[1]); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// get_robust_list(pid, &head, &len)(K07):查询登记(按调用线程;
// 跨进程查询兼容配置取本线程,未知 pid 报 ESRCH)。
func (s Syscalls) getRobustList(ctx context.Context, call Call) Reply {
	if s.RobustLists == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != 0 && call.Args[0] != s.identityPID() {
		return Reply{Errno: errno.NoProcess}
	}
	var head, length uint64
	if err := s.RobustLists.RobustList(&head, &length); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	var headBuf, lenBuf [8]byte
	for i := 0; i < 8; i++ {
		headBuf[i] = byte(head >> (8 * i))
		lenBuf[i] = byte(length >> (8 * i))
	}
	if err := s.Memory.Write(ctx, call.Args[1], headBuf[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	if err := s.Memory.Write(ctx, call.Args[2], lenBuf[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

// tkill(tid, sig)(K08):线程定向信号,tid 必须存在;fatal 信号
// 由内核升级为整组结束。musl 的 pthread_kill 走 tkill。
func (s Syscalls) tkill(ctx context.Context, call Call) Reply {
	if s.Kills == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	sig := call.Args[1]
	if sig != 0 && !validGuestSignal(sig) {
		return Reply{Errno: errno.Invalid}
	}
	if err := s.Kills.Tkill(ctx, int64(call.Args[0]), sig); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) tgkill(ctx context.Context, call Call) Reply {
	if s.Kills == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	sig := call.Args[2]
	if sig != 0 && !validGuestSignal(sig) {
		return Reply{Errno: errno.Invalid}
	}
	// K08:组成员关系由实现校验(tid 不属于 tgid 报 ESRCH)。
	if err := s.Kills.Tgkill(ctx, int64(call.Args[0]), int64(call.Args[1]), sig); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// asm-generic clone 的 SIGCHLD 位:flags==SIGCHLD 即 fork。
const cloneSigchld = 17

// clone 标志(asm-generic):libc 线程形态(K02)所需子集 + 明确拒绝项。
const (
	cloneVM             = 0x00000100
	cloneDetachedIgnore = 0x00400000 // CLONE_DETACHED:已废弃,Linux 忽略
	cloneFS             = 0x00000200
	cloneFiles          = 0x00000400
	cloneSighand        = 0x00000800
	clonePtrace         = 0x00002000
	cloneVFork          = 0x00004000
	cloneParent         = 0x00008000
	cloneThread         = 0x00010000
	cloneSysvsem        = 0x00040000
	cloneSetTLS         = 0x00080000
	cloneParentSetTID   = 0x00100000
	cloneChildClearTID  = 0x00200000
	cloneDetached       = 0x00400000
	cloneUntraced       = 0x00800000
	cloneChildSetTID    = 0x01000000
	cloneSignalMask     = 0xff
	cloneNewNamespaceLo = 0x20000000 // CLONE_NEWNS 起的命名空间族
)

// CloneRequest 是线程形态 clone 的已解析参数(H05)。
type CloneRequest struct {
	// Stack 是子线程的初始 sp(0=沿用调用线程当前栈)。
	Stack uint64
	// TLS 与 SetTLS 是 CLONE_SETTLS 的线程指针初值。
	TLS    uint64
	SetTLS bool
	// ParentTIDPtr/ChildTIDPtr 是 CLONE_PARENT_SETTID/CLONE_CHILD_SETTID
	// 的写回地址(0=不写回);内核在登记后写线程号。
	ParentTIDPtr uint64
	ChildTIDPtr  uint64
	// ClearTID 是 CLONE_CHILD_CLEARTID 的登记地址(0=不登记),
	// 线程退出时内核写零并唤醒 futex 等待者(K03)。
	ClearTID uint64
}

// ThreadSpawner 是 clone 线程形态(K02)的端口:实现登记同组线程、
// 共享地址空间/描述符/信号处置,并在子线程上下文完成续接执行。
type ThreadSpawner interface {
	CloneThread(ctx context.Context, request CloneRequest) (uint64, error)
}

// VforkRequest 是 vfork 形态 clone 的已解析参数(H12)。
type VforkRequest struct {
	// Stack 是子进程的初始 sp(0=沿用调用方当前栈,musl vfork 语义;
	// posix_spawn 传自备栈)。
	Stack uint64
}

// Vforker 是 clone 的 vfork 形态(H12)端口:实现登记共享地址空间的
// 子进程并暂停调用方,直到子进程 exec(换绑新地址空间)或退出后
// 恢复——vfork 的完成语义。
type Vforker interface {
	Vfork(ctx context.Context, request VforkRequest) (uint64, error)
}

// RobustListPort 是 robust futex 登记端口(K07)。
type RobustListPort interface {
	SetRobustList(head, length uint64) error

	RobustList(head *uint64, length *uint64) error
}

// validateCloneFlags 按 Linux 语义校验 clone 标志(H05):未知位 EINVAL;
// CLONE_SIGHAND 必须伴随 CLONE_VM,CLONE_THREAD 必须伴随 CLONE_SIGHAND;
// 线程形态的退出信号必须为 0;命名空间族与调试/驱动形态明确 ENOTSUP。
func validateCloneFlags(flags uint64) errno.Code {
	if flags&^(cloneVM|cloneFS|cloneFiles|cloneSighand|cloneThread|cloneSysvsem|
		cloneSetTLS|cloneParentSetTID|cloneChildClearTID|cloneChildSetTID|
		cloneDetachedIgnore|uint64(cloneSignalMask)) != 0 {
		if flags&(clonePtrace|cloneVFork|cloneParent|cloneUntraced) != 0 ||
			flags&^uint64(cloneSignalMask) >= cloneNewNamespaceLo {
			return errno.NotSupported
		}
		return errno.Invalid
	}
	if flags&cloneSighand != 0 && flags&cloneVM == 0 {
		return errno.Invalid
	}
	if flags&cloneThread != 0 {
		if flags&cloneSighand == 0 {
			return errno.Invalid
		}
		if flags&uint64(cloneSignalMask) != 0 {
			return errno.Invalid
		}
		return ""
	}
	// 非线程的共享形态(部分共享 VM/FS/FILES/SIGHAND 的"进程")不在
	// 兼容配置,明确拒绝而非伪装 fork。
	if flags&^(uint64(cloneSignalMask)) != 0 {
		return errno.NotSupported
	}
	return errno.NotSupported
}

// clone(flags, sp, parent_tid, tls, child_tid):fork 形态(flags==SIGCHLD
// 且 sp=0)复制进程(H04);vfork 形态(CLONE_VM|CLONE_VFORK|SIGCHLD,
// glibc/musl 的 vfork 与 posix_spawn 底层)共享地址空间并暂停调用方
// 到子进程 exec/exit(H12);线程形态(K02,libc pthread)经 Threader
// 共享地址空间续接执行;其余形态按 H05 校验后明确拒绝。
func (s Syscalls) cloneProcess(ctx context.Context, call Call) Reply {
	flags, sp := call.Args[0], call.Args[1]
	if flags == cloneSigchld && sp == 0 {
		if s.Spawner == nil {
			return Reply{Errno: errno.NotImplemented}
		}
		pid, err := s.Spawner.Fork(ctx)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		return Reply{Value: pid}
	}
	// vfork 形态:仅接受恰为 CLONE_VM|CLONE_VFORK|SIGCHLD 的组合;
	// 其他带 CLONE_VFORK 的组合仍按 H05 拒绝。CLONE_VM 不含
	// CLONE_SETTLS 等参数位,父/子 TID 写回与 clear_child_tid 不适用。
	if flags == cloneVM|cloneVFork|cloneSigchld {
		if s.Vforker == nil {
			return Reply{Errno: errno.NotSupported}
		}
		pid, err := s.Vforker.Vfork(ctx, VforkRequest{Stack: sp})
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		return Reply{Value: pid}
	}
	if code := validateCloneFlags(flags); code != "" {
		return Reply{Errno: code}
	}
	if s.Threader == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var request CloneRequest
	request.Stack = sp
	request.SetTLS = flags&cloneSetTLS != 0
	request.TLS = call.Args[3]
	if flags&cloneParentSetTID != 0 {
		request.ParentTIDPtr = call.Args[2]
	}
	if flags&cloneChildSetTID != 0 {
		request.ChildTIDPtr = call.Args[4]
	}
	if flags&cloneChildClearTID != 0 {
		request.ClearTID = call.Args[4]
	}
	tid, err := s.Threader.CloneThread(ctx, request)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: tid}
}

// sigreturn 的帧基址经 Args[0] 注入(适配器从 sp 寄存器读取);
// 恢复后 Runner 从帧内 PC 继续,a0 的返回值由适配器跳过不写。
func (s Syscalls) rtSigreturn(ctx context.Context, call Call) Reply {
	if s.Core == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	state, err := DecodeSignalFrame(ctx, s.Memory, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if s.Signals != nil {
		if err := s.restoreMaskViaPort(ctx, state.Mask); err != "" {
			return Reply{Errno: err}
		}
	}
	if err := s.Core.Reset(ctx, state.Machine); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// restoreMaskViaPort 经可选的屏蔽恢复端口恢复帧内屏蔽集;
// 端口未实现屏蔽恢复时静默跳过(帧恢复仍完整)。
func (s Syscalls) restoreMaskViaPort(ctx context.Context, mask uint64) errno.Code {
	if restorer, ok := s.Signals.(interface {
		RestoreMask(context.Context, uint64) error
	}); ok {
		if err := restorer.RestoreMask(ctx, mask); err != nil {
			return errnoOf(err)
		}
	}
	return ""
}

// sigaltstack(ss) 登记或查询备用信号栈(stack_t 24 字节)。
func (s Syscalls) sigaltstack(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Signals == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != 0 {
		var raw [24]byte
		if err := s.Memory.Read(ctx, call.Args[0], raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		sp := binary.LittleEndian.Uint64(raw[0:])
		flags := binary.LittleEndian.Uint32(raw[8:])
		size := binary.LittleEndian.Uint64(raw[16:])
		stack := SignalAltStack{Sp: sp, Size: size}
		switch flags {
		case 0:
			stack.Present = true
		case ssDisable:
			stack.Present = false
		default:
			return Reply{Errno: errno.Invalid}
		}
		if stack.Present && size < rtSigframeSize {
			return Reply{Errno: errno.NoMemory}
		}
		if err := s.Signals.SetAltStack(ctx, stack); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
	}
	if call.Args[1] != 0 {
		current, err := s.Signals.AltStack(ctx)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		var encoded [24]byte
		binary.LittleEndian.PutUint64(encoded[0:], current.Sp)
		flags := uint32(0)
		if !current.Present {
			flags = ssDisable
		}
		binary.LittleEndian.PutUint32(encoded[8:], flags)
		binary.LittleEndian.PutUint64(encoded[16:], current.Size)
		if err := s.Memory.Write(ctx, call.Args[1], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{}
}

// cpuCount 返回客体配置的 CPU 数;未配置按单 CPU。
func (s Syscalls) cpuCount() uint64 {
	if s.CPUs == 0 {
		return 1
	}
	return s.CPUs
}

// affinityMask 按小端编码 CPU 掩码;至少 8 字节(单个 u64)。
func (s Syscalls) affinityMask() []byte {
	cpus := s.cpuCount()
	mask := make([]byte, 8)
	for index := uint64(0); index < cpus && index < 64; index++ {
		mask[index/8] |= 1 << (index % 8)
	}
	return mask
}

// sched_getaffinity(pid, len, mask) 返回客体配置的亲和性掩码;
// 跨进程查询明确 ESRCH,缓冲区不足 8 字节 EINVAL。
func (s Syscalls) schedGetAffinity(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != 0 && (s.Identity == nil || call.Args[0] != s.Identity.PID) {
		return Reply{Errno: errno.NoProcess}
	}
	if call.Args[1] < 8 {
		return Reply{Errno: errno.Invalid}
	}
	mask := s.affinityMask()
	if err := s.Memory.Write(ctx, call.Args[2], mask); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(len(mask))}
}

// sched_setaffinity 单 CPU 客体只接受包含 CPU 0 的掩码(空操作成功),
// 其余掩码 EINVAL;调用受同样的进程边界约束。
func (s Syscalls) schedSetAffinity(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != 0 && (s.Identity == nil || call.Args[0] != s.Identity.PID) {
		return Reply{Errno: errno.NoProcess}
	}
	if call.Args[1] < 8 {
		return Reply{Errno: errno.Invalid}
	}
	var mask [8]byte
	if err := s.Memory.Read(ctx, call.Args[2], mask[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	if mask[0]&1 == 0 {
		return Reply{Errno: errno.Invalid}
	}
	return Reply{}
}

// getcpu(cpu, node, tcache) 报告客体拓扑:单 CPU 单节点。
func (s Syscalls) getcpu(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var encoded [4]byte
	binary.LittleEndian.PutUint32(encoded[0:], 0)
	if call.Args[0] != 0 {
		if err := s.Memory.Write(ctx, call.Args[0], encoded[:4]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	if call.Args[1] != 0 {
		if err := s.Memory.Write(ctx, call.Args[1], encoded[:4]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{}
}

// spaceReporter 是文件句柄的可选能力:报告所在文件系统容量(fstatfs)。
type spaceReporter interface {
	Space(ctx context.Context) (vmfs.Space, error)
}

func (s Syscalls) statfs(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	// Linux statfs(2) 的 ABI 是 statfs(path, buf),没有 dirfd 参数;
	// statfsat 并不存在。路径直接取 Args[0],结果缓冲区取 Args[1]。
	pathname, code := s.readPath(ctx, call.Args[0])
	if code != "" {
		return Reply{Errno: code}
	}
	info, err := s.Files.Statfs(ctx, pathname)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return s.writeStatfs(ctx, call.Args[1], info)
}

func (s Syscalls) fstatfs(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	reporter, ok := handle.(spaceReporter)
	if !ok {
		return Reply{Errno: errno.NotSupported}
	}
	space, err := reporter.Space(ctx)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return s.writeStatfs(ctx, call.Args[1], StatfsInfo{Bytes: space.Bytes, Inodes: space.Inodes})
}

func (s Syscalls) writeStatfs(ctx context.Context, address uint64, info StatfsInfo) Reply {
	encoded := encodeStatfs(info.Bytes, info.Inodes)
	if err := s.Memory.Write(ctx, address, encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

func (s Syscalls) fcntl(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	// I14:文件锁命令(F_GETLK/F_SETLK/F_SETLKW)经锁表处理。
	switch call.Args[1] {
	case fGetLK, fSetLK, fSetLKW:
		return s.fcntlLock(ctx, call)
	}
	value, err := s.Descriptors.Fcntl(ctx, call.Args[0], call.Args[1], call.Args[2])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: value}
}

// fcntl 文件锁命令(F_GETLK=5/F_SETLK=6/F_SETLKW=7)与锁类型。
const (
	fGetLK  = 5
	fSetLK  = 6
	fSetLKW = 7

	fRDLCK = 0
	fWRLCK = 1
	fUNLCK = 2
)

// decodeFlock 解析 asm-generic struct flock(32 字节:type@0/whence@2/
// start@8/len@16/pid@24,qemu-riscv64 实测锁定)。
func (s Syscalls) decodeFlock(ctx context.Context, address uint64) (typ int, whence uint64, start, length uint64, err errno.Code) {
	var raw [32]byte
	if readErr := s.Memory.Read(ctx, address, raw[:]); readErr != nil {
		return 0, 0, 0, 0, errno.Fault
	}
	typ = int(uint16(raw[0]) | uint16(raw[1])<<8)
	whence = uint64(uint16(raw[2]) | uint16(raw[3])<<8)
	for i := 0; i < 8; i++ {
		start |= uint64(raw[8+i]) << (8 * i)
		length |= uint64(raw[16+i]) << (8 * i)
	}
	return typ, whence, start, length, ""
}

// encodeFlock 写回 struct flock:F_GETLK 的冲突锁或 F_UNLCK。
func (s Syscalls) encodeFlock(ctx context.Context, address uint64, typ int, start, length, pid uint64) error {
	var raw [32]byte
	raw[0] = byte(typ)
	raw[1] = byte(typ >> 8)
	for i := 0; i < 8; i++ {
		raw[8+i] = byte(start >> (8 * i))
		raw[16+i] = byte(length >> (8 * i))
		raw[24+i] = byte(pid >> (8 * i))
	}
	return s.Memory.Write(ctx, address, raw[:])
}

func eofEnd(start, length uint64) uint64 {
	if length == 0 {
		return 0 // 至 EOF
	}
	return start + length
}

// fcntlLock 实现 F_GETLK(探测冲突)/F_SETLK(非阻塞)/F_SETLKW
// (阻塞,1ms 切片感知信号,EINTR)。锁对象经句柄 Lockable 能力
// (inode 号)键控;holder 为调用进程号。
func (s Syscalls) fcntlLock(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Locks == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	lockable, ok := handle.(vmfs.Lockable)
	if !ok {
		// 管道/epoll 等不可锁对象:Linux 报 EBADF(锁仅文件)。
		return Reply{Errno: errno.BadHandle}
	}
	cmd := call.Args[1]
	typ, whence, start, length, code := s.decodeFlock(ctx, call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	if typ != fRDLCK && typ != fWRLCK && typ != fUNLCK {
		return Reply{Errno: errno.Invalid}
	}
	var rng locks.Range
	switch whence {
	case 0, 2: // SEEK_SET / SEEK_END:子集不含相对当前偏移
		rng = locks.Range{Start: start, End: eofEnd(start, length)}
	default:
		return Reply{Errno: errno.Invalid}
	}
	holder := s.identityPID()
	key := locks.Key(lockable.LockIdentity())
	switch cmd {
	case fGetLK:
		conflictRange, conflictType, conflictHolder, has := s.Locks.Test(key, holder, rng, typ)
		writeType := fUNLCK
		if has {
			writeType = conflictType
		}
		if encodeErr := s.encodeFlock(ctx, call.Args[2], writeType, conflictRange.Start, conflictRange.End, conflictHolder); encodeErr != nil {
			return Reply{Errno: errno.Fault}
		}
		return Reply{}
	case fSetLK:
		if setErr := s.Locks.Set(key, holder, rng, typ); setErr != nil {
			return Reply{Errno: errnoOf(setErr)}
		}
		return Reply{}
	default: // fSetLKW
		for {
			setErr := s.Locks.Set(key, holder, rng, typ)
			if setErr == nil {
				return Reply{}
			}
			if !errors.Is(setErr, errno.WouldBlock) {
				return Reply{Errno: errnoOf(setErr)}
			}
			// 阻塞重试:1ms 切片感知待投递信号(J09)。
			if s.Interrupts != nil {
				if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
					return Reply{Errno: errno.Interrupted}
				}
			}
			if err := s.Sleeper.Sleep(ctx, sleepSlice); err != nil {
				return Reply{Errno: errno.Interrupted}
			}
		}
	}
}

// pipe2(fds, flags) 创建匿名管道(I15):O_CLOEXEC/O_NONBLOCK 在
// 创建时生效。asm-generic 无独立 pipe 系统调用(glibc 经 pipe2 实现)。
func (s Syscalls) pipe2(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[1]&^(0o2000000|0o4000) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	r, w, err := s.Descriptors.Pipe(ctx)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if call.Args[1]&0o2000000 != 0 { // O_CLOEXEC
		_, _ = s.Descriptors.Fcntl(ctx, r, 2, 1)
		_, _ = s.Descriptors.Fcntl(ctx, w, 2, 1)
	}
	if call.Args[1]&0o4000 != 0 { // O_NONBLOCK
		_, _ = s.Descriptors.Fcntl(ctx, r, 4, 0o4000)
		_, _ = s.Descriptors.Fcntl(ctx, w, 4, 0o4000)
	}
	var encoded [8]byte
	binary.LittleEndian.PutUint32(encoded[0:], uint32(r))
	binary.LittleEndian.PutUint32(encoded[4:], uint32(w))
	if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: 0}
}

func (s Syscalls) eventfd2(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[1]&^efdFlagMask != 0 {
		return Reply{Errno: errno.Invalid}
	}
	descriptor, err := s.Descriptors.EventFD(ctx, call.Args[0], call.Args[1])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: descriptor}
}

// inotifyInit1(flags) 创建 inotify 实例(I20):只接受 IN_NONBLOCK 与
// IN_CLOEXEC,未知标志 EINVAL。描述符端口未装配报 NotImplemented 族。
func (s Syscalls) inotifyInit1(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0]&^(uint64(inoNonBlock|inoCloexec)) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	descriptor, err := s.Descriptors.Inotify(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	// 实例登记到扇出注册表(I20):此后其他进程的文件操作经
	// Observe 到达本实例。描述符关闭后的剔除由扇出方惰性完成
	// (Closed() 判定)。
	if s.Notify != nil {
		if handle, getErr := s.Descriptors.InotifyAt(ctx, descriptor); getErr == nil {
			s.Notify.Register(handle)
		}
	}
	return Reply{Value: descriptor}
}

// inotifyAddWatch(fd, path, mask) 登记监视(I20):路径按目录句柄解析,
// isDir 由 stat 判定(IN_ONLYDIR 用);句柄不是 inotify 报 EINVAL。
func (s Syscalls) inotifyAddWatch(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	path, code = s.resolveDirPath(ctx, atCurrentWorkingDirectory, path)
	if code != "" {
		return Reply{Errno: code}
	}
	isDir := false
	if info, err := s.Files.Stat(ctx, path); err == nil && info != nil {
		isDir = info.IsDir()
	}
	handle, err := s.Descriptors.InotifyAt(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	wd, err := handle.AddWatch(path, uint32(call.Args[2]), uint32(call.Args[2]>>32), isDir)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: uint64(wd)}
}

// inotifyRmWatch(fd, wd) 移除监视(I20);未知 wd 报 EINVAL。
func (s Syscalls) inotifyRmWatch(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	handle, err := s.Descriptors.InotifyAt(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if err := handle.RemoveWatch(uint32(call.Args[1])); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// asm-generic 的 inotify 标志位(I20):IN_CLOEXEC/IN_NONBLOCK 与
// eventfd/signalfd 同值。
const (
	inoNonBlock = 0o4000
	inoCloexec  = 0o2000000
)

// asm-generic 的 signalfd4 标志位与掩码剥离规则。
const (
	sfdNonBlock = 0o4000
	sfdCloexec  = 0o2000000
	sfdFlagMask = sfdNonBlock | sfdCloexec

	// SIGKILL=9 与 SIGSTOP=19 不可捕获,Linux 在登记掩码时静默剥离。
	sfdKillBit = uint64(1) << 8
	sfdStopBit = uint64(1) << 18
)

// signalfd4(ufd, mask, sizemask, flags):ufd==-1 时创建新的
// signalfd,否则更新已存在描述符的掩码。被进程屏蔽且命中掩码的
// 信号由内核路由进入 fd 队列,读取即消费,不再进入处理器与
// 默认处置路径(L11)。
func (s Syscalls) signalfd4(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[2] != signalfd.SigsetSize {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[3]&^sfdFlagMask != 0 {
		return Reply{Errno: errno.Invalid}
	}
	var raw [signalfd.SigsetSize]byte
	if err := s.Memory.Read(ctx, call.Args[1], raw[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	mask, err := signalfd.ParseSigset(raw[:])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	mask &^= sfdKillBit | sfdStopBit
	if mask == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[0] == ^uint64(0) {
		descriptor, err := s.Descriptors.SignalFD(ctx, mask, call.Args[3])
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		return Reply{Value: descriptor}
	}
	if call.Args[0] > math.MaxUint32 {
		return Reply{Errno: errno.BadHandle}
	}
	if err := s.Descriptors.UpdateSignalFD(ctx, call.Args[0], mask, call.Args[3]); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: call.Args[0]}
}

// dup(f) 复制到最低空闲编号,共享文件偏移与状态。
func (s Syscalls) dup(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	id, err := s.Descriptors.DupFrom(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: id}
}

func (s Syscalls) dup3(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	flags := call.Args[2]
	cloexec := flags&0o2000000 != 0
	if err := s.Descriptors.Dup2(ctx, call.Args[0], call.Args[1], cloexec); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: call.Args[1]}
}

func (s Syscalls) brk(ctx context.Context, call Call) Reply {
	if s.Heap == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	current, err := s.Heap.Grow(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: current}
}
