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

// InstructionFetcher 是可选的高速取指能力:在同一个地址空间快照内
// 完成执行权限校验与取指,并返回指令原始编码与 2/4 字节长度。
// 未提供时执行核心退回通用 Read 加 Executable 检查。
type InstructionFetcher interface {
	FetchInstruction(ctx context.Context, address uint64) (uint32, uint64, error)
}

// InstructionPageFetcher 是批量执行所需的整页取指能力。返回的 Data
// 在本次 VM 生命周期内保持不可变；失效由 version 变化表达。
type InstructionPageFetcher interface {
	FetchInstructionPage(ctx context.Context, address uint64) (base uint64, data []byte, version uint64, ok bool, err error)
}

// BatchStepper 是可选的批量指令执行能力。实现必须在无法继续批量
// 执行时尽量少执行并于下一条指令前返回，由调用方回退到单步执行。
type BatchStepper interface {
	BatchEnabled() bool

	StepBatch(ctx context.Context, limit uint64) (uint64, Outcome, error)
}

// BatchMeter 报告当前余量是否适合批量计费；未实现时批量执行禁用。
type BatchMeter interface {
	BatchAllowed() bool
}

// AtomicMemory 是可选的内存原子读改写能力(K02/K03):AMO 与由
// LR/SC 实现的比较交换必须在同一内存锁内完成读改写,否则多个
// hart 的原子操作会相互覆盖。width 为 4 或 8,operation 返回新值。
type AtomicMemory interface {
	AtomicModify(ctx context.Context, address uint64, width int, operation func(uint64) uint64) (uint64, error)
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

// Attacher 是可换绑地址空间的执行端口能力:任务切换时把核心指向
// 另一个任务的内存(执行权限检查随之切换),机器状态由调用方另行
// 保存与恢复。
type Attacher interface {
	Attach(Memory)
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
