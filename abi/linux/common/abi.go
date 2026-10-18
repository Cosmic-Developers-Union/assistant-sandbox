package common

import (
	"context"
	"errors"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Syscall uint16

const (
	SysUnknown Syscall = iota

	SysExit

	SysExitGroup

	SysRead

	SysWrite

	SysOpenAt

	SysClose

	SysLseek

	SysGetPID
	SysGetPPID
	SysGetTID

	SysGetUID
	SysGetEUID
	SysGetGID
	SysGetEGID

	SysClockGettime

	SysClockGetres

	SysGettimeofday

	SysBrk

	SysUname

	SysSysinfo

	SysGetcwd

	SysGetrandom

	SysGetpgid
	SysGetsid

	SysTimes

	SysGetgroups

	SysSetpgid
	SysSetsid

	SysSchedYield

	SysPrlimit64

	SysGetrusage

	SysGetitimer
	SysSetitimer

	SysGetpriority
	SysSetpriority
	SysMlock
	SysMunlock
	SysMlockall
	SysMunlockall
	SysMincore
	SysMadvise

	SysFaccessat

	SysGetrlimit
	SysSetrlimit

	SysSetuid
	SysSetgid
	SysSetreuid
	SysSetregid
	SysSetresuid
	SysSetresgid
	SysSetfsuid
	SysSetfsgid

	SysReadlinkat

	SysUmask

	SysSync
	SysFsync
	SysFdatasync

	SysMkdirat
	SysUnlinkat
	SysSymlinkat
	SysLinkat
	SysRenameat
	SysRenameat2

	SysFtruncate

	SysFcntl

	SysPipe
	SysPipe2

	SysDup3

	SysGetdents64

	SysSocket
	SysBind
	SysListen
	SysAccept
	SysConnect
	SysSocketpair
	SysSendto
	SysRecvfrom
	SysShutdown
	SysSetsockopt
	SysGetsockopt
	SysGetsockname
	SysGetpeername
)

func (s Syscall) String() string {
	switch s {
	case SysUnknown:
		return "unknown"
	case SysExit:
		return "exit"
	case SysExitGroup:
		return "exit_group"
	case SysRead:
		return "read"
	case SysWrite:
		return "write"
	case SysOpenAt:
		return "openat"
	case SysClose:
		return "close"
	case SysLseek:
		return "lseek"
	case SysGetPID:
		return "getpid"
	case SysGetPPID:
		return "getppid"
	case SysGetTID:
		return "gettid"
	case SysGetUID:
		return "getuid"
	case SysGetEUID:
		return "geteuid"
	case SysGetGID:
		return "getgid"
	case SysGetEGID:
		return "getegid"
	case SysClockGettime:
		return "clock_gettime"
	case SysClockGetres:
		return "clock_getres"
	case SysGettimeofday:
		return "gettimeofday"
	case SysBrk:
		return "brk"
	case SysUname:
		return "uname"
	case SysSysinfo:
		return "sysinfo"
	case SysGetpgid:
		return "getpgid"
	case SysGetsid:
		return "getsid"
	case SysTimes:
		return "times"
	case SysGetgroups:
		return "getgroups"
	case SysSetpgid:
		return "setpgid"
	case SysSetsid:
		return "setsid"
	case SysSchedYield:
		return "sched_yield"
	case SysPrlimit64:
		return "prlimit64"
	case SysGetrlimit:
		return "getrlimit"
	case SysSetrlimit:
		return "setrlimit"
	case SysGetrusage:
		return "getrusage"
	case SysGetitimer:
		return "getitimer"
	case SysSetitimer:
		return "setitimer"
	case SysGetpriority:
		return "getpriority"
	case SysSetpriority:
		return "setpriority"
	case SysMlock:
		return "mlock"
	case SysMunlock:
		return "munlock"
	case SysMlockall:
		return "mlockall"
	case SysMunlockall:
		return "munlockall"
	case SysMincore:
		return "mincore"
	case SysMadvise:
		return "madvise"
	case SysFaccessat:
		return "faccessat"
	case SysSetuid:
		return "setuid"
	case SysSetgid:
		return "setgid"
	case SysSetreuid:
		return "setreuid"
	case SysSetregid:
		return "setregid"
	case SysSetresuid:
		return "setresuid"
	case SysSetresgid:
		return "setresgid"
	case SysSetfsuid:
		return "setfsuid"
	case SysSetfsgid:
		return "setfsgid"
	case SysMkdirat:
		return "mkdirat"
	case SysUnlinkat:
		return "unlinkat"
	case SysSymlinkat:
		return "symlinkat"
	case SysLinkat:
		return "linkat"
	case SysRenameat:
		return "renameat"
	case SysRenameat2:
		return "renameat2"
	case SysFtruncate:
		return "ftruncate"
	case SysFcntl:
		return "fcntl"
	case SysPipe:
		return "pipe"
	case SysPipe2:
		return "pipe2"
	case SysDup3:
		return "dup3"
	case SysGetdents64:
		return "getdents64"
	case SysSocket:
		return "socket"
	case SysBind:
		return "bind"
	case SysListen:
		return "listen"
	case SysAccept:
		return "accept"
	case SysConnect:
		return "connect"
	case SysSocketpair:
		return "socketpair"
	case SysSendto:
		return "sendto"
	case SysRecvfrom:
		return "recvfrom"
	case SysShutdown:
		return "shutdown"
	case SysSetsockopt:
		return "setsockopt"
	case SysGetsockopt:
		return "getsockopt"
	case SysGetsockname:
		return "getsockname"
	case SysGetpeername:
		return "getpeername"
	case SysReadlinkat:
		return "readlinkat"
	case SysUmask:
		return "umask"
	case SysSync:
		return "sync"
	case SysFsync:
		return "fsync"
	case SysFdatasync:
		return "fdatasync"
	case SysGetcwd:
		return "getcwd"
	case SysGetrandom:
		return "getrandom"
	default:
		return fmt.Sprintf("syscall(%d)", uint16(s))
	}
}

type Call struct {
	Syscall Syscall
	Number  uint64
	Args    [6]uint64

	OpenFlags *OpenFlags
}

type Reply struct {
	Value uint64

	Errno errno.Code

	Exit bool
}

type Handler interface {
	Handle(ctx context.Context, call Call) Reply
}

type ExitOnly struct{}

func (ExitOnly) Handle(_ context.Context, call Call) Reply {
	switch call.Syscall {
	case SysExit, SysExitGroup:
		return Reply{Exit: true, Value: call.Args[0]}
	default:
		return Reply{Errno: errno.NotImplemented}
	}
}

type Exit struct {
	Requested bool
	Code      uint64
}

type Dispatcher interface {
	Dispatch(ctx context.Context, trap cpu.Trap) (Exit, error)
}

type Fault struct {
	Trap cpu.Trap
}

func (f Fault) Error() string {
	return fmt.Sprintf("Linux 程序故障: %v@%#x（指令 %#x）", f.Trap.Kind, f.Trap.Address, f.Trap.Instruction)
}

func (f Fault) Unwrap() error { return f.Trap }

type Config struct {
	Core cpu.Port

	Dispatcher Dispatcher
}

type Runner struct {
	core       cpu.Port
	dispatcher Dispatcher
}

func NewRunner(config Config) (*Runner, error) {
	if config.Core == nil || config.Dispatcher == nil {
		return nil, errno.Wrap(errno.Invalid, "Runner 需要执行端口与 trap 分发器")
	}
	return &Runner{core: config.Core, dispatcher: config.Dispatcher}, nil
}

func (r *Runner) Run(ctx context.Context) (uint64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		outcome, stepErr := r.core.Step(ctx)
		if stepErr != nil {
			var trap cpu.Trap
			if !errors.As(stepErr, &trap) {
				return 0, stepErr
			}
			exit, err := r.dispatcher.Dispatch(ctx, trap)
			if err != nil {
				return 0, err
			}
			if exit.Requested {
				return exit.Code, nil
			}
			continue
		}
		switch outcome {
		case cpu.Continued:
		case cpu.Halted:

			return 0, nil
		case cpu.Trapped:

			return 0, errno.Wrap(errno.Invalid, "执行器报告 trap 但没有给出 trap 详情")
		default:
			return 0, errno.Wrap(errno.Invalid, fmt.Sprintf("未知执行结果 %v", outcome))
		}
	}
}
