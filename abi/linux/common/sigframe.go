package common

import (
	"context"
	"encoding/binary"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// asm-generic/RISC-V 信号帧布局:rt_sigframe = siginfo(128) + ucontext(576)。
const (
	rtSigframeUCOff = siginfoSize // 128
	rtSigframeSize  = siginfoSize + ucontextSize

	ucFlagsOff    = 0
	ucLinkOff     = 8
	ucStackOff    = 16 // stack_t: ss_sp(8) + ss_flags(4) + pad(4) + ss_size(8)
	ucMaskOff     = 40
	ucMcontextOff = 48
	ucontextSize  = 576

	// sigcontext:pc + 31 个通用寄存器(264)+ 32 浮点寄存器与 fcsr(264)。
	sigcontextRegsOff = 0
	sigcontextFPOff   = 264
	sigcontextSize    = 528

	// asm-generic 的 SA_* 标志。
	saNoCLDStop = 0x00000001
	saNoCLDWait = 0x00000002
	saSigInfo   = 0x00000004
	saOnStack   = 0x08000000
	saRestart   = 0x10000000
	saNodefer   = 0x40000000
	saRestorer  = 0x04000000

	// asm-generic 的 SS_* 标志。
	ssDisable = 0x2
)

// 对外暴露投递与登记判断所需的标志别名。
const (
	SigactionSigInfo   = saSigInfo
	SigactionOnStack   = saOnStack
	SigactionNoDefer   = saNodefer
	SigactionRestorer  = saRestorer
	SigactionRestart   = saRestart
	SignalStackDisable = ssDisable
)

// SI_USER 是 kill/raise 发送信号时的 si_code;同步故障携带
// asm-generic 的故障类别码与故障地址。
const siUser = 0

// asm-generic si_code:同步故障的类别(与信号无关,各类内部自 1 起编)。
const (
	SigInfoIllIllegalOp = 1 // ILL_ILLOPC:非法操作码
	SigInfoSegvMapErr   = 1 // SEGV_MAPERR:地址未映射
	SigInfoSegvAccErr   = 2 // SEGV_ACCERR:映射存在但权限不足
	SigInfoBusAdraln    = 1 // BUS_ADRALN:地址不对齐
	SigInfoTrapBreak    = 1 // TRAP_BRKPT:断点陷阱
)

// FaultInfo 是同步故障投递的 siginfo 类别与地址。
type FaultInfo struct {
	Code int32
	Addr uint64
}

// SignalAltStack 是 sigaltstack 登记的备用栈(stack_t 的端口表示)。
type SignalAltStack struct {
	Sp      uint64
	Size    uint64
	Present bool
}

// SignalFrameInput 是写入 rt_sigframe 所需的完整客体状态。
type SignalFrameInput struct {
	Signal    uint64
	Action    SignalAction
	FrameBase uint64
	Machine   cpu.Machine
	OldMask   uint64
	AltStack  SignalAltStack

	// Fault 是同步故障投递的类别与地址;为空时 siginfo 记 SI_USER。
	Fault *FaultInfo
}

// SignalReturnState 是 rt_sigreturn 从帧中恢复的状态。
type SignalReturnState struct {
	Machine cpu.Machine
	Mask    uint64
}

// AlignFrameBase 把帧基址对齐到 16 字节并预留帧空间。
func AlignFrameBase(sp uint64) uint64 {
	return (sp - rtSigframeSize) &^ 0xF
}

// WriteSignalFrame 把 siginfo 与 ucontext(含通用/浮点寄存器与屏蔽集)
// 写入 FrameBase,返回 siginfo 与 ucontext 的客体地址。
func WriteSignalFrame(ctx context.Context, memory Memory, input SignalFrameInput) (uint64, uint64, error) {
	var frame [rtSigframeSize]byte
	putU64 := func(offset int, value uint64) {
		binary.LittleEndian.PutUint64(frame[offset:], value)
	}
	// siginfo:si_signo、si_errno、si_code 与 _sigfault._addr;
	// 异步投递(Fault 为空)按 SI_USER 记类别、地址置零。
	code, faultAddress := int32(siUser), uint64(0)
	if input.Fault != nil {
		code, faultAddress = input.Fault.Code, input.Fault.Addr
	}
	binary.LittleEndian.PutUint32(frame[0:], uint32(input.Signal))
	binary.LittleEndian.PutUint32(frame[8:], uint32(code))
	binary.LittleEndian.PutUint64(frame[16:], faultAddress)

	putU64(rtSigframeUCOff+ucFlagsOff, 0)
	putU64(rtSigframeUCOff+ucLinkOff, 0)
	putU64(rtSigframeUCOff+ucStackOff, input.AltStack.Sp)
	ssFlags := uint32(0)
	if !input.AltStack.Present {
		ssFlags = ssDisable
	}
	binary.LittleEndian.PutUint32(frame[rtSigframeUCOff+ucStackOff+8:], ssFlags)
	putU64(rtSigframeUCOff+ucStackOff+16, input.AltStack.Size)
	putU64(rtSigframeUCOff+ucMaskOff, input.OldMask)

	// mcontext:pc 与 x1..x31(x0 恒零不入帧);寄存器序 = 寄存器号。
	regs := rtSigframeUCOff + ucMcontextOff + sigcontextRegsOff
	putU64(regs, input.Machine.PC)
	for index := 1; index < 32 && index < len(input.Machine.Registers); index++ {
		putU64(regs+8*index, input.Machine.Registers[index])
	}
	fps := rtSigframeUCOff + ucMcontextOff + sigcontextFPOff
	for index := 0; index < 32 && index < len(input.Machine.Floats); index++ {
		putU64(fps+8*index, input.Machine.Floats[index])
	}
	binary.LittleEndian.PutUint32(frame[fps+256:], input.Machine.FCSR)

	if err := memory.Write(ctx, input.FrameBase, frame[:]); err != nil {
		return 0, 0, errno.Fault
	}
	return input.FrameBase, input.FrameBase + rtSigframeUCOff, nil
}

// DecodeSignalFrame 从 FrameBase 读取信号帧,恢复完整机器状态与屏蔽集。
func DecodeSignalFrame(ctx context.Context, memory Memory, frameBase uint64) (SignalReturnState, error) {
	var frame [rtSigframeSize]byte
	if err := memory.Read(ctx, frameBase, frame[:]); err != nil {
		return SignalReturnState{}, errno.Fault
	}
	getU64 := func(offset int) uint64 {
		return binary.LittleEndian.Uint64(frame[offset:])
	}
	regs := rtSigframeUCOff + ucMcontextOff + sigcontextRegsOff
	machine := cpu.Machine{
		PC:        getU64(regs),
		Registers: make([]uint64, 32),
		Floats:    make([]uint64, 32),
	}
	machine.Registers[0] = 0
	for index := 1; index < 32; index++ {
		machine.Registers[index] = getU64(regs + 8*index)
	}
	fps := rtSigframeUCOff + ucMcontextOff + sigcontextFPOff
	for index := 0; index < 32; index++ {
		machine.Floats[index] = getU64(fps + 8*index)
	}
	machine.FCSR = binary.LittleEndian.Uint32(frame[fps+256:])
	return SignalReturnState{
		Machine: machine,
		Mask:    getU64(rtSigframeUCOff + ucMaskOff),
	}, nil
}
