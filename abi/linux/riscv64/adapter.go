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
	numberOpenAt        = 56
	numberClose         = 57
	numberLseek         = 62
	numberRead          = 63
	numberWrite         = 64
	numberExit          = 93
	numberExitGroup     = 94
	numberClockGettime  = 113
	numberGetPID        = 172
	numberGetPPID       = 173
	numberGetUID        = 174
	numberGetEUID       = 175
	numberGetGID        = 176
	numberGetEGID       = 177
	numberGetTID        = 178
	numberBrk           = 214
	numberUname         = 160
	numberGetcwd        = 17
	numberSysinfo       = 179
	numberGetpgid       = 155
	numberGetsid        = 156
	numberGetpgrp       = 158
	numberGethostname   = 161
	numberGetdomainname = 163
	numberGetrandom     = 278
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
	case numberGetpgrp:
		return common.SysGetpgrp
	case numberGethostname:
		return common.SysGethostname
	case numberGetdomainname:
		return common.SysGetdomainname
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
