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
	numberOpenAt       = 56
	numberClose        = 57
	numberLseek        = 62
	numberRead         = 63
	numberWrite        = 64
	numberExit         = 93
	numberExitGroup    = 94
	numberClockGettime = 113
	numberGetPID       = 172
	numberGetPPID      = 173
	numberGetUID       = 174
	numberGetEUID      = 175
	numberGetGID       = 176
	numberGetEGID      = 177
	numberGetTID       = 178
	numberBrk          = 214
	numberUname        = 160
	numberGetcwd       = 17
	numberSysinfo      = 179
	numberGetpgid      = 155
	numberGetsid       = 156
	numberTimes        = 153
	numberGetgroups    = 158
	numberSetpgid      = 154
	numberSetsid       = 157
	numberSchedYield   = 124
	numberPrlimit64    = 261
	numberGetrlimit    = 163
	numberSetrlimit    = 164
	numberGetrusage    = 165
	numberGetitimer    = 102
	numberSetitimer    = 103
	numberGetpriority  = 141
	numberSetpriority  = 140
	numberMlock        = 228
	numberMunlock      = 229
	numberMlockall     = 230
	numberMunlockall   = 231
	numberMincore      = 232
	numberMadvise      = 233
	numberFaccessat    = 48
	numberMkdirat      = 34
	numberUnlinkat     = 35
	numberSymlinkat    = 36
	numberLinkat       = 37
	numberRenameat     = 38
	numberRenameat2    = 276
	numberFtruncate    = 46
	numberReadlinkat   = 78
	numberUmask        = 166
	numberSync         = 81
	numberFsync        = 82
	numberFdatasync    = 83
	numberSetuid       = 146
	numberSetgid       = 144
	numberSetreuid     = 145
	numberSetregid     = 143
	numberSetresuid    = 147
	numberSetresgid    = 149
	numberSetfsuid     = 151
	numberSetfsgid     = 152
	numberGetrandom    = 278
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
	case numberMadvise:
		return common.SysMadvise
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
