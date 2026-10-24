package cpu

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Port interface {
	Reset(context.Context, Machine) error

	Step(context.Context) (Outcome, error)

	State(context.Context) (Machine, error)

	WordSize() WordSize
}

type Machine struct {
	PC uint64

	Registers []uint64

	// Floats 保存 32 个浮点寄存器的原始位型;S 值按 RISC-V 约定高位填 1。
	Floats []uint64

	// FCSR 是浮点控制状态寄存器:bits[4:0] fflags,bits[7:5] frm。
	FCSR uint32
}

type WordSize struct {
	Bits int

	Bytes int

	LittleEndian bool
}

type Outcome uint8

const (
	Continued Outcome = iota + 1

	Halted

	Trapped
)

func (o Outcome) String() string {
	switch o {
	case Continued:
		return "continued"
	case Halted:
		return "halted"
	case Trapped:
		return "trapped"
	default:
		return "outcome(" + utoa(uint64(o)) + ")"
	}
}

type TrapKind uint8

const (
	TrapIllegalInstruction TrapKind = iota + 1

	TrapNotImplemented

	TrapEcall

	TrapBreakpoint

	TrapMemoryFault
)

func (k TrapKind) String() string {
	switch k {
	case TrapIllegalInstruction:
		return "illegal-instruction"
	case TrapNotImplemented:
		return "not-implemented"
	case TrapEcall:
		return "ecall"
	case TrapBreakpoint:
		return "breakpoint"
	case TrapMemoryFault:
		return "memory-fault"
	default:
		return "trap(" + utoa(uint64(k)) + ")"
	}
}

type Trap struct {
	Kind TrapKind

	Address uint64

	Instruction uint32

	Detail uint64

	Cause error
}

func (t Trap) Error() string {
	return t.Kind.String() + "@" + hex(t.Address) + ": instruction " + hex(uint64(t.Instruction))
}

func (t Trap) Unwrap() error { return t.Cause }

type RegisterFile interface {
	ReadRegister(index int) (uint64, error)

	WriteRegister(index int, value uint64) error
}

type Memory interface {
	Read(ctx context.Context, address uint64, buffer []byte) error

	Write(ctx context.Context, address uint64, buffer []byte) error
}

type Canceller interface {
	Cancelled() bool
}

// FloatRegisterFile 暴露浮点寄存器堆与 fcsr,供调试与任务切换使用。
type FloatRegisterFile interface {
	ReadFP(index int) (uint64, error)

	WriteFP(index int, value uint64) error

	ReadFCSR() (uint32, error)

	WriteFCSR(value uint32) error
}

// ExecutableChecker 由带权限位的内存实现提供;不可执行页取指报错。
// Core 通过类型断言发现该能力,缺失时不做取指权限检查。
type ExecutableChecker interface {
	Executable(ctx context.Context, address uint64, length uint64) error
}

type Meter interface {
	Charge(instructions uint64) error
}

type Config struct {
	Memory Memory

	Meter Meter

	Canceller Canceller

	Entry uint64
}

func (c Config) Validate() error {
	if c.Memory == nil {
		return errno.Wrap(errno.Invalid, "CPU 需要客体内存端口")
	}
	return nil
}

func hex(value uint64) string {
	const digits = "0123456789abcdef"
	if value == 0 {
		return "0x0"
	}
	var buffer [18]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = digits[value%16]
		value /= 16
	}
	index -= 2
	buffer[index] = '0'
	buffer[index+1] = 'x'
	return string(buffer[index:])
}

func utoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
