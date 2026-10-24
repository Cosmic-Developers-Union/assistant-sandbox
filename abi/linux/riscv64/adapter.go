package riscv64

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/common"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const (
	regArgument0  = 10
	regArgument5  = 15
	regNumber     = 17
	regReturn     = regArgument0
	argumentCount = regArgument5 - regArgument0 + 1
)

const (
	numberOpenAt           = 56
	numberClose            = 57
	numberLseek            = 62
	numberRead             = 63
	numberWrite            = 64
	numberExit             = 93
	numberExitGroup        = 94
	numberClockGettime     = 113
	numberClockGetres      = 114
	numberGettimeofday     = 169
	numberGetPID           = 172
	numberGetPPID          = 173
	numberGetUID           = 174
	numberGetEUID          = 175
	numberGetGID           = 176
	numberGetEGID          = 177
	numberGetTID           = 178
	numberBrk              = 214
	numberMmap             = 222
	numberMunmap           = 215
	numberMprotect         = 226
	numberMremap           = 216
	numberReadv            = 65
	numberWritev           = 66
	numberUname            = 160
	numberClockNanosleep   = 115
	numberGetcwd           = 17
	numberSysinfo          = 179
	numberGetpgid          = 155
	numberGetsid           = 156
	numberTimes            = 153
	numberGetgroups        = 158
	numberSetpgid          = 154
	numberSetsid           = 157
	numberSchedYield       = 124
	numberPrlimit64        = 261
	numberGetrlimit        = 163
	numberSetrlimit        = 164
	numberGetrusage        = 165
	numberGetitimer        = 102
	numberSetitimer        = 103
	numberGetpriority      = 141
	numberSetpriority      = 140
	numberMlock            = 228
	numberMunlock          = 229
	numberMlockall         = 230
	numberMunlockall       = 231
	numberMincore          = 232
	numberMadvise          = 233
	numberFaccessat        = 48
	numberMkdirat          = 34
	numberUnlinkat         = 35
	numberSymlinkat        = 36
	numberLinkat           = 37
	numberRenameat         = 38
	numberRenameat2        = 276
	numberFtruncate        = 46
	numberFcntl            = 25
	numberPipe             = 59
	numberPipe2            = 60
	numberDup3             = 24
	numberGetdents64       = 61
	numberNewfstatat       = 79
	numberFstat            = 80
	numberStatx            = 291
	numberUtimensat        = 88
	numberStatfs           = 43
	numberFstatfs          = 44
	numberSchedSetAffinity = 122
	numberSchedGetAffinity = 123
	numberGetCPU           = 168
	numberWaitid           = 95
	numberDup              = 23
	numberRtSigaction      = 134
	numberRtSigprocmask    = 135
	numberRtSigreturn      = 139
	numberSigaltstack      = 132
	numberSocket           = 198
	numberBind             = 200
	numberListen           = 201
	numberAccept           = 202
	numberConnect          = 203
	numberSocketpair       = 199
	numberSendto           = 206
	numberRecvfrom         = 207
	numberShutdown         = 210
	numberSetsockopt       = 208
	numberGetsockopt       = 209
	numberGetsockname      = 204
	numberGetpeername      = 205
	numberAccept4          = 242
	numberMemfdCreate      = 279
	numberUserfaultfd      = 282
	numberPerfEventOpen    = 241
	numberClone3           = 435
	numberPidfdOpen        = 434
	numberCloseRange       = 436
	numberOpenat2          = 437
	numberPidfdGetfd       = 438
	numberFaccessat2       = 439
	numberFallocate        = 47
	numberFadvise64        = 223
	numberMount            = 40
	numberUmount2          = 39
	numberPivotRoot        = 41
	numberChroot           = 51
	numberReboot           = 142
	numberKexecLoad        = 104
	numberSyslog           = 116
	numberAcct             = 89
	numberPtrace           = 117
	numberEpollCreate1     = 20
	numberEpollCtl         = 21
	numberEpollPwait       = 22
	numberPpoll            = 73
	numberPselect6         = 72
	numberEventfd2         = 19
	numberTimerfdCreate    = 85
	numberTimerfdSettime   = 86
	numberTimerfdGettime   = 87
	numberSignalfd4        = 74
	numberInotifyInit1     = 26
	numberInotifyAddWatch  = 27
	numberInotifyRmWatch   = 28
	numberReadlinkat       = 78
	numberUmask            = 166
	numberSync             = 81
	numberFsync            = 82
	numberFdatasync        = 83
	numberSetuid           = 146
	numberSetgid           = 144
	numberSetreuid         = 145
	numberSetregid         = 143
	numberSetresuid        = 147
	numberSetresgid        = 149
	numberSetfsuid         = 151
	numberSetfsgid         = 152
	numberGetrandom        = 278
)

const ecallInstruction = 0x00000073

type Adapter struct {
	Handler common.Handler

	Registers cpu.RegisterFile
}

func (a *Adapter) Dispatch(ctx context.Context, trap cpu.Trap) (common.Exit, error) {
	if a.Handler == nil || a.Registers == nil {
		return common.Exit{}, errno.Wrap(errno.Invalid, "Linux ABI 适配器需要 Handler 与寄存器端口")
	}
	if trap.Kind != cpu.TrapEcall || trap.Instruction != ecallInstruction {
		return common.Exit{}, common.Fault{Trap: trap}
	}
	call, err := a.call(ctx)
	if err != nil {
		return common.Exit{}, err
	}
	if call.Syscall == common.SysRtSigreturn {
		// rt_sigreturn 无参数:把帧基址(sp)注入第一个参数槽,
		// 恢复后的寄存器状态不再被返回值覆写。
		sp, err := a.Registers.ReadRegister(2)
		if err != nil {
			return common.Exit{}, err
		}
		call.Args[0] = sp
		reply := a.Handler.Handle(ctx, call)
		if reply.Errno != "" {
			number, _ := errno.Linux(reply.Errno)
			if err := a.Registers.WriteRegister(regReturn, uint64(-int64(number))); err != nil {
				return common.Exit{}, err
			}
		}
		return common.Exit{}, nil
	}
	reply := a.Handler.Handle(ctx, call)
	if reply.Exit {

		return common.Exit{Requested: true, Code: reply.Value}, nil
	}
	value := reply.Value
	if reply.Errno != "" {
		number, _ := errno.Linux(reply.Errno)

		value = uint64(-int64(number))
	}
	if err := a.Registers.WriteRegister(regReturn, value); err != nil {
		return common.Exit{}, err
	}
	return common.Exit{}, nil
}

func (a *Adapter) call(ctx context.Context) (common.Call, error) {
	if err := ctx.Err(); err != nil {
		return common.Call{}, err
	}
	number, err := a.Registers.ReadRegister(regNumber)
	if err != nil {
		return common.Call{}, err
	}
	call := common.Call{Number: number, Syscall: syscallOf(number)}
	for index := 0; index < argumentCount; index++ {
		value, err := a.Registers.ReadRegister(regArgument0 + index)
		if err != nil {
			return common.Call{}, err
		}
		call.Args[index] = value
	}
	if call.Syscall == common.SysOpenAt {

		call.OpenFlags = openFlags(call.Args[2])
	}
	return call, nil
}

const (
	openAccessMask  = 0b11
	openCreate      = 0o100
	openExclusive   = 0o200
	openTruncate    = 0o1000
	openAppend      = 0o2000
	openCloseOnExec = 0o2000000

	openKnown = openAccessMask | openCreate | openExclusive | openTruncate | openAppend | openCloseOnExec
)

func openFlags(raw uint64) *common.OpenFlags {
	if raw&^uint64(openKnown) != 0 {
		return nil
	}
	flags := common.OpenFlags{
		Mode:        common.OpenMode(raw & openAccessMask),
		Create:      raw&openCreate != 0,
		Exclusive:   raw&openExclusive != 0,
		Truncate:    raw&openTruncate != 0,
		Append:      raw&openAppend != 0,
		CloseOnExec: raw&openCloseOnExec != 0,
	}
	return &flags
}

func syscallOf(number uint64) common.Syscall {
	switch number {
	case numberOpenAt:
		return common.SysOpenAt
	case numberClose:
		return common.SysClose
	case numberLseek:
		return common.SysLseek
	case numberClockGettime:
		return common.SysClockGettime
	case numberClockNanosleep:
		return common.SysClockNanosleep
	case numberClockGetres:
		return common.SysClockGetres
	case numberGettimeofday:
		return common.SysGettimeofday
	case numberGetPID:
		return common.SysGetPID
	case numberGetPPID:
		return common.SysGetPPID
	case numberGetUID:
		return common.SysGetUID
	case numberGetEUID:
		return common.SysGetEUID
	case numberGetGID:
		return common.SysGetGID
	case numberGetEGID:
		return common.SysGetEGID
	case numberGetTID:
		return common.SysGetTID
	case numberBrk:
		return common.SysBrk
	case numberMmap:
		return common.SysMmap
	case numberMunmap:
		return common.SysMunmap
	case numberMprotect:
		return common.SysMprotect
	case numberMremap:
		return common.SysMremap
	case numberReadv:
		return common.SysReadv
	case numberWritev:
		return common.SysWritev
	case numberMadvise:
		return common.SysMadvise
	case numberRead:
		return common.SysRead
	case numberWrite:
		return common.SysWrite
	case numberUname:
		return common.SysUname
	case numberGetcwd:
		return common.SysGetcwd
	case numberSysinfo:
		return common.SysSysinfo
	case numberGetpgid:
		return common.SysGetpgid
	case numberGetsid:
		return common.SysGetsid
	case numberTimes:
		return common.SysTimes
	case numberGetgroups:
		return common.SysGetgroups
	case numberSetpgid:
		return common.SysSetpgid
	case numberSetsid:
		return common.SysSetsid
	case numberSchedYield:
		return common.SysSchedYield
	case numberPrlimit64:
		return common.SysPrlimit64
	case numberGetrlimit:
		return common.SysGetrlimit
	case numberSetrlimit:
		return common.SysSetrlimit
	case numberGetrusage:
		return common.SysGetrusage
	case numberGetitimer:
		return common.SysGetitimer
	case numberSetitimer:
		return common.SysSetitimer
	case numberGetpriority:
		return common.SysGetpriority
	case numberSetpriority:
		return common.SysSetpriority
	case numberMlock:
		return common.SysMlock
	case numberMunlock:
		return common.SysMunlock
	case numberMlockall:
		return common.SysMlockall
	case numberMunlockall:
		return common.SysMunlockall
	case numberMincore:
		return common.SysMincore
	case numberFaccessat:
		return common.SysFaccessat
	case numberMkdirat:
		return common.SysMkdirat
	case numberUnlinkat:
		return common.SysUnlinkat
	case numberSymlinkat:
		return common.SysSymlinkat
	case numberLinkat:
		return common.SysLinkat
	case numberRenameat, numberRenameat2:
		return common.SysRenameat
	case numberFtruncate:
		return common.SysFtruncate
	case numberFcntl:
		return common.SysFcntl
	case numberPipe:
		return common.SysPipe
	case numberPipe2:
		return common.SysPipe2
	case numberDup3:
		return common.SysDup3
	case numberGetdents64:
		return common.SysGetdents64
	case numberNewfstatat:
		return common.SysNewfstatat
	case numberFstat:
		return common.SysFstat
	case numberStatx:
		return common.SysStatx
	case numberUtimensat:
		return common.SysUtimensat
	case numberStatfs:
		return common.SysStatfs
	case numberFstatfs:
		return common.SysFstatfs
	case numberSchedSetAffinity:
		return common.SysSchedSetAffinity
	case numberSchedGetAffinity:
		return common.SysSchedGetAffinity
	case numberGetCPU:
		return common.SysGetCPU
	case numberWaitid:
		return common.SysWaitid
	case numberDup:
		return common.SysDup
	case numberRtSigaction:
		return common.SysRtSigaction
	case numberRtSigprocmask:
		return common.SysRtSigprocmask
	case numberRtSigreturn:
		return common.SysRtSigreturn
	case numberSigaltstack:
		return common.SysSigaltstack
	case numberSocket:
		return common.SysSocket
	case numberBind:
		return common.SysBind
	case numberListen:
		return common.SysListen
	case numberAccept:
		return common.SysAccept
	case numberConnect:
		return common.SysConnect
	case numberSocketpair:
		return common.SysSocketpair
	case numberSendto:
		return common.SysSendto
	case numberRecvfrom:
		return common.SysRecvfrom
	case numberShutdown:
		return common.SysShutdown
	case numberSetsockopt:
		return common.SysSetsockopt
	case numberGetsockopt:
		return common.SysGetsockopt
	case numberGetsockname:
		return common.SysGetsockname
	case numberGetpeername:
		return common.SysGetpeername
	case numberAccept4:
		return common.SysAccept4
	case numberMemfdCreate:
		return common.SysMemfdCreate
	case numberUserfaultfd:
		return common.SysUserfaultfd
	case numberPerfEventOpen:
		return common.SysPerfEventOpen
	case numberClone3:
		return common.SysClone3
	case numberPidfdOpen:
		return common.SysPidfdOpen
	case numberCloseRange:
		return common.SysCloseRange
	case numberOpenat2:
		return common.SysOpenat2
	case numberPidfdGetfd:
		return common.SysPidfdGetfd
	case numberFaccessat2:
		return common.SysFaccessat2
	case numberFallocate:
		return common.SysFallocate
	case numberFadvise64:
		return common.SysFadvise64
	case numberMount:
		return common.SysMount
	case numberUmount2:
		return common.SysUmount2
	case numberPivotRoot:
		return common.SysPivotRoot
	case numberChroot:
		return common.SysChroot
	case numberReboot:
		return common.SysReboot
	case numberKexecLoad:
		return common.SysKexecLoad
	case numberSyslog:
		return common.SysSyslog
	case numberAcct:
		return common.SysAcct
	case numberPtrace:
		return common.SysPtrace
	case numberEpollCreate1:
		return common.SysEpollCreate1
	case numberEpollCtl:
		return common.SysEpollCtl
	case numberEpollPwait:
		return common.SysEpollPwait
	case numberPpoll:
		return common.SysPpoll
	case numberPselect6:
		return common.SysPselect6
	case numberEventfd2:
		return common.SysEventfd2
	case numberTimerfdCreate:
		return common.SysTimerfdCreate
	case numberTimerfdSettime:
		return common.SysTimerfdSettime
	case numberTimerfdGettime:
		return common.SysTimerfdGettime
	case numberSignalfd4:
		return common.SysSignalfd4
	case numberInotifyInit1:
		return common.SysInotifyInit1
	case numberInotifyAddWatch:
		return common.SysInotifyAddWatch
	case numberInotifyRmWatch:
		return common.SysInotifyRmWatch
	case numberReadlinkat:
		return common.SysReadlinkat
	case numberUmask:
		return common.SysUmask
	case numberSync:
		return common.SysSync
	case numberFsync:
		return common.SysFsync
	case numberFdatasync:
		return common.SysFdatasync
	case numberSetuid:
		return common.SysSetuid
	case numberSetgid:
		return common.SysSetgid
	case numberSetreuid:
		return common.SysSetreuid
	case numberSetregid:
		return common.SysSetregid
	case numberSetresuid:
		return common.SysSetresuid
	case numberSetresgid:
		return common.SysSetresgid
	case numberSetfsuid:
		return common.SysSetfsuid
	case numberSetfsgid:
		return common.SysSetfsgid
	case numberGetrandom:
		return common.SysGetrandom
	case numberExit:
		return common.SysExit
	case numberExitGroup:
		return common.SysExitGroup
	default:
		return common.SysUnknown
	}
}

var _ common.Dispatcher = (*Adapter)(nil)
