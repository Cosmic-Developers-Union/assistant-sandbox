package riscv64

import (
	"context"
	"encoding/binary"
	"math/bits"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Core struct {
	machine cpu.Machine
	memory  cpu.Memory
	meter   cpu.Meter
	cancel  cpu.Canceller
}

const (
	regZero = 0
	regRA   = 1
	regSP   = 2
	regA0   = 10
	regA7   = 17
	numRegs = 32
)

const mask32 = 0xFFFFFFFF

func New(config cpu.Config) (*Core, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	core := &Core{
		memory: config.Memory,
		meter:  config.Meter,
		cancel: config.Canceller,
	}

	core.machine = cpu.Machine{PC: config.Entry, Registers: make([]uint64, numRegs)}
	return core, nil
}

func (c *Core) WordSize() cpu.WordSize {
	return cpu.WordSize{Bits: 64, Bytes: 8, LittleEndian: true}
}

func (c *Core) Reset(ctx context.Context, machine cpu.Machine) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registers := make([]uint64, numRegs)
	for index := 0; index < numRegs && index < len(machine.Registers); index++ {
		registers[index] = machine.Registers[index]
	}
	registers[regZero] = 0
	c.machine = cpu.Machine{PC: machine.PC, Registers: registers}
	return nil
}

func (c *Core) State(ctx context.Context) (cpu.Machine, error) {
	if err := ctx.Err(); err != nil {
		return cpu.Machine{}, err
	}
	registers := make([]uint64, len(c.machine.Registers))
	copy(registers, c.machine.Registers)
	return cpu.Machine{PC: c.machine.PC, Registers: registers}, nil
}

func (c *Core) ReadRegister(index int) (uint64, error) {
	if index < 0 || index >= numRegs {
		return 0, errno.Wrap(errno.Invalid, "寄存器编号越界")
	}
	return c.machine.Registers[index], nil
}

func (c *Core) WriteRegister(index int, value uint64) error {
	if index < 0 || index >= numRegs {
		return errno.Wrap(errno.Invalid, "寄存器编号越界")
	}
	c.write(index, value)
	return nil
}

func (c *Core) Step(ctx context.Context) (cpu.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	if c.cancel != nil && c.cancel.Cancelled() {
		return 0, errno.Wrap(errno.Cancelled, "执行被取消")
	}
	if c.meter != nil {
		if err := c.meter.Charge(1); err != nil {
			return 0, err
		}
	}
	pc := c.machine.PC

	var encoded [4]byte
	if err := c.memory.Read(ctx, pc, encoded[:]); err != nil {
		return cpu.Trapped, cpu.Trap{Kind: cpu.TrapMemoryFault, Address: pc, Cause: err}
	}
	instruction := binary.LittleEndian.Uint32(encoded[:])

	if instruction&0x3 != 0x3 {
		return cpu.Trapped, cpu.Trap{
			Kind: cpu.TrapNotImplemented, Address: pc, Instruction: instruction,
			Detail: uint64(instruction & 0x3),
		}
	}

	c.machine.PC = pc + 4
	outcome, trap, err := c.execute(ctx, pc, instruction)
	if err != nil {
		return 0, err
	}
	if trap != nil {
		return cpu.Trapped, *trap
	}
	return outcome, nil
}

func (c *Core) execute(ctx context.Context, pc uint64, instruction uint32) (cpu.Outcome, *cpu.Trap, error) {
	opcode := instruction & 0x7F
	rd := int((instruction >> 7) & 0x1F)
	funct3 := (instruction >> 12) & 0x7
	rs1 := int((instruction >> 15) & 0x1F)
	rs2 := int((instruction >> 20) & 0x1F)
	funct7 := (instruction >> 25) & 0x7F

	switch opcode {
	case 0x37:
		c.write(rd, signExtend(uint64(instruction&0xFFFFF000), 32))
		return cpu.Continued, nil, nil
	case 0x17:
		c.write(rd, pc+signExtend(uint64(instruction&0xFFFFF000), 32))
		return cpu.Continued, nil, nil
	case 0x6F:
		offset := signExtend(
			uint64(instruction>>31)<<20|
				uint64((instruction>>12)&0xFF)<<12|
				uint64((instruction>>20)&0x1)<<11|
				uint64((instruction>>21)&0x3FF)<<1, 21)
		c.write(rd, pc+4)
		c.machine.PC = pc + offset
		return cpu.Continued, nil, nil
	case 0x67:
		if funct3 != 0 {
			return 0, illegal(pc, instruction), nil
		}
		offset := signExtend(uint64(instruction)>>20, 12)
		target := (c.read(rs1) + offset) &^ 1
		c.write(rd, pc+4)
		c.machine.PC = target
		return cpu.Continued, nil, nil
	case 0x63:
		return c.branch(pc, instruction, funct3, rs1, rs2)
	case 0x03:
		return c.load(ctx, pc, instruction, funct3, rd, rs1)
	case 0x23:
		return c.store(ctx, pc, instruction, funct3, rs1, rs2)
	case 0x13:
		return c.opImm(pc, instruction, funct3, rd, rs1)
	case 0x1B:
		return c.opImmWord(pc, instruction, funct3, rd, rs1)
	case 0x33:
		return c.op(pc, instruction, funct3, funct7, rd, rs1, rs2)
	case 0x3B:
		return c.opWord(pc, instruction, funct3, funct7, rd, rs1, rs2)
	case 0x0F:
		if funct3 == 0x1 {
			return cpu.Trapped, notImplemented(pc, instruction), nil
		}
		return cpu.Continued, nil, nil
	case 0x73:
		return c.system(pc, instruction, funct3, rd, rs1)
	case 0x2F, 0x07, 0x27, 0x43, 0x47, 0x4B, 0x4F, 0x53:

		return cpu.Trapped, notImplemented(pc, instruction), nil
	default:
		return 0, illegal(pc, instruction), nil
	}
}

func (c *Core) branch(pc uint64, instruction uint32, funct3 uint32, rs1, rs2 int) (cpu.Outcome, *cpu.Trap, error) {

	offset := signExtend(
		uint64(instruction>>31)<<12|
			uint64((instruction>>7)&0x1)<<11|
			uint64((instruction>>25)&0x3F)<<5|
			uint64((instruction>>8)&0xF)<<1, 13)
	left, right := c.read(rs1), c.read(rs2)
	var taken bool
	switch funct3 {
	case 0x0:
		taken = left == right
	case 0x1:
		taken = left != right
	case 0x4:
		taken = int64(left) < int64(right)
	case 0x5:
		taken = int64(left) >= int64(right)
	case 0x6:
		taken = left < right
	case 0x7:
		taken = left >= right
	default:
		return 0, illegal(pc, instruction), nil
	}
	if taken {
		c.machine.PC = pc + offset
	}
	return cpu.Continued, nil, nil
}

func (c *Core) load(ctx context.Context, pc uint64, instruction uint32, funct3 uint32, rd, rs1 int) (cpu.Outcome, *cpu.Trap, error) {
	offset := signExtend(uint64(instruction)>>20, 12)
	address := c.read(rs1) + offset
	width, signed, ok := loadShape(funct3)
	if !ok {
		return 0, illegal(pc, instruction), nil
	}
	var raw [8]byte
	if err := c.memory.Read(ctx, address, raw[:width]); err != nil {
		return cpu.Trapped, &cpu.Trap{Kind: cpu.TrapMemoryFault, Address: pc, Instruction: instruction, Detail: address, Cause: err}, nil
	}
	c.write(rd, loadValue(raw[:width], signed))
	return cpu.Continued, nil, nil
}

func loadShape(funct3 uint32) (int, bool, bool) {
	switch funct3 {
	case 0x0:
		return 1, true, true
	case 0x1:
		return 2, true, true
	case 0x2:
		return 4, true, true
	case 0x3:
		return 8, true, true
	case 0x4:
		return 1, false, true
	case 0x5:
		return 2, false, true
	case 0x6:
		return 4, false, true
	default:
		return 0, false, false
	}
}

func loadValue(raw []byte, signed bool) uint64 {
	switch len(raw) {
	case 1:
		if signed {
			return uint64(int64(int8(raw[0])))
		}
		return uint64(raw[0])
	case 2:
		half := binary.LittleEndian.Uint16(raw)
		if signed {
			return uint64(int64(int16(half)))
		}
		return uint64(half)
	case 4:
		word := binary.LittleEndian.Uint32(raw)
		if signed {
			return uint64(int64(int32(word)))
		}
		return uint64(word)
	default:
		return binary.LittleEndian.Uint64(raw)
	}
}

func (c *Core) store(ctx context.Context, pc uint64, instruction uint32, funct3 uint32, rs1, rs2 int) (cpu.Outcome, *cpu.Trap, error) {

	offset := signExtend(
		uint64(instruction>>25)<<5|
			uint64(instruction>>7)&0x1F, 12)
	address := c.read(rs1) + offset
	value := c.read(rs2)
	var raw [8]byte
	var width int
	switch funct3 {
	case 0x0:
		width = 1
		raw[0] = byte(value)
	case 0x1:
		width = 2
		binary.LittleEndian.PutUint16(raw[:2], uint16(value))
	case 0x2:
		width = 4
		binary.LittleEndian.PutUint32(raw[:4], uint32(value))
	case 0x3:
		width = 8
		binary.LittleEndian.PutUint64(raw[:8], value)
	default:
		return 0, illegal(pc, instruction), nil
	}
	if err := c.memory.Write(ctx, address, raw[:width]); err != nil {
		return cpu.Trapped, &cpu.Trap{Kind: cpu.TrapMemoryFault, Address: pc, Instruction: instruction, Detail: address, Cause: err}, nil
	}
	return cpu.Continued, nil, nil
}

func (c *Core) opImm(pc uint64, instruction uint32, funct3 uint32, rd, rs1 int) (cpu.Outcome, *cpu.Trap, error) {
	source := c.read(rs1)
	immediate := signExtend(uint64(instruction)>>20, 12)
	shiftAmount := (instruction >> 20) & 0x3F
	shiftKind := (instruction >> 26) & 0x3F
	switch funct3 {
	case 0x0:
		c.write(rd, source+immediate)
	case 0x2:
		c.write(rd, boolValue(int64(source) < int64(immediate)))
	case 0x3:
		c.write(rd, boolValue(source < immediate))
	case 0x4:
		c.write(rd, source^immediate)
	case 0x6:
		c.write(rd, source|immediate)
	case 0x7:
		c.write(rd, source&immediate)
	case 0x1:
		if shiftKind != 0x00 {
			return 0, illegal(pc, instruction), nil
		}
		c.write(rd, source<<shiftAmount)
	case 0x5:
		switch shiftKind {
		case 0x00:
			c.write(rd, source>>shiftAmount)
		case 0x10:
			c.write(rd, uint64(int64(source)>>shiftAmount))
		default:
			return 0, illegal(pc, instruction), nil
		}
	default:
		return 0, illegal(pc, instruction), nil
	}
	return cpu.Continued, nil, nil
}

func (c *Core) opImmWord(pc uint64, instruction uint32, funct3 uint32, rd, rs1 int) (cpu.Outcome, *cpu.Trap, error) {
	source := uint32(c.read(rs1))
	immediate := uint32(signExtend(uint64(instruction)>>20, 12))
	shiftAmount := (instruction >> 20) & 0x1F
	shiftKind := (instruction >> 25) & 0x7F
	switch funct3 {
	case 0x0:
		c.write(rd, signExtend(uint64(source+immediate), 32))
	case 0x1:
		if shiftKind != 0x00 {
			return 0, illegal(pc, instruction), nil
		}
		c.write(rd, signExtend(uint64(source<<shiftAmount), 32))
	case 0x5:
		switch shiftKind {
		case 0x00:
			c.write(rd, signExtend(uint64(source>>shiftAmount), 32))
		case 0x20:
			c.write(rd, signExtend(uint64(uint32(int32(source)>>shiftAmount)), 32))
		default:
			return 0, illegal(pc, instruction), nil
		}
	default:
		return 0, illegal(pc, instruction), nil
	}
	return cpu.Continued, nil, nil
}

func (c *Core) op(pc uint64, instruction uint32, funct3 uint32, funct7 uint32, rd, rs1, rs2 int) (cpu.Outcome, *cpu.Trap, error) {
	left, right := c.read(rs1), c.read(rs2)
	switch funct7 {
	case 0x00:
		switch funct3 {
		case 0x0:
			c.write(rd, left+right)
		case 0x1:
			c.write(rd, left<<(right&0x3F))
		case 0x2:
			c.write(rd, boolValue(int64(left) < int64(right)))
		case 0x3:
			c.write(rd, boolValue(left < right))
		case 0x4:
			c.write(rd, left^right)
		case 0x5:
			c.write(rd, left>>(right&0x3F))
		case 0x6:
			c.write(rd, left|right)
		case 0x7:
			c.write(rd, left&right)
		default:
			return 0, illegal(pc, instruction), nil
		}
	case 0x20:
		switch funct3 {
		case 0x0:
			c.write(rd, left-right)
		case 0x5:
			c.write(rd, uint64(int64(left)>>(right&0x3F)))
		default:
			return 0, illegal(pc, instruction), nil
		}
	case 0x01:
		return c.multiply(pc, instruction, funct3, rd, left, right)
	default:
		return 0, illegal(pc, instruction), nil
	}
	return cpu.Continued, nil, nil
}

func (c *Core) multiply(pc uint64, instruction uint32, funct3 uint32, rd int, left, right uint64) (cpu.Outcome, *cpu.Trap, error) {
	switch funct3 {
	case 0x0:
		_, low := bits.Mul64(left, right)
		c.write(rd, low)
	case 0x1:
		c.write(rd, mulHighSigned(left, right))
	case 0x2:
		c.write(rd, mulHighSignedUnsigned(left, right))
	case 0x3:
		high, _ := bits.Mul64(left, right)
		c.write(rd, high)
	case 0x4:
		c.write(rd, divideSigned(left, right))
	case 0x5:
		c.write(rd, divideUnsigned(left, right))
	case 0x6:
		c.write(rd, remainderSigned(left, right))
	case 0x7:
		c.write(rd, remainderUnsigned(left, right))
	default:
		return 0, illegal(pc, instruction), nil
	}
	return cpu.Continued, nil, nil
}

func (c *Core) opWord(pc uint64, instruction uint32, funct3 uint32, funct7 uint32, rd, rs1, rs2 int) (cpu.Outcome, *cpu.Trap, error) {
	left := uint32(c.read(rs1))
	right := uint32(c.read(rs2))
	switch funct7 {
	case 0x00:
		switch funct3 {
		case 0x0:
			c.write(rd, signExtend(uint64(left+right), 32))
		case 0x1:
			c.write(rd, signExtend(uint64(left<<(right&0x1F)), 32))
		case 0x5:
			c.write(rd, signExtend(uint64(left>>(right&0x1F)), 32))
		default:
			return 0, illegal(pc, instruction), nil
		}
	case 0x20:
		switch funct3 {
		case 0x0:
			c.write(rd, signExtend(uint64(left-right), 32))
		case 0x5:
			c.write(rd, signExtend(uint64(uint32(int32(left)>>(right&0x1F))), 32))
		default:
			return 0, illegal(pc, instruction), nil
		}
	case 0x01:
		switch funct3 {
		case 0x0:
			c.write(rd, signExtend(uint64(left*right), 32))
		case 0x4:
			c.write(rd, signExtend(uint64(divideSignedWord(left, right)), 32))
		case 0x5:
			c.write(rd, signExtend(uint64(divideUnsignedWord(left, right)), 32))
		case 0x6:
			c.write(rd, signExtend(uint64(remainderSignedWord(left, right)), 32))
		case 0x7:
			c.write(rd, signExtend(uint64(remainderUnsignedWord(left, right)), 32))
		default:
			return 0, illegal(pc, instruction), nil
		}
	default:
		return 0, illegal(pc, instruction), nil
	}
	return cpu.Continued, nil, nil
}

func (c *Core) system(pc uint64, instruction uint32, funct3 uint32, rd, rs1 int) (cpu.Outcome, *cpu.Trap, error) {
	switch {
	case funct3 == 0x0 && rd == 0 && rs1 == 0:

		immediate := instruction >> 20
		if immediate == 0x0 {

			detail := c.read(regA7)<<32 | c.read(regA0)&mask32
			return cpu.Trapped, &cpu.Trap{Kind: cpu.TrapEcall, Address: pc, Instruction: instruction, Detail: detail}, nil
		}
		if immediate == 0x1 {

			return cpu.Trapped, &cpu.Trap{Kind: cpu.TrapBreakpoint, Address: pc, Instruction: instruction}, nil
		}
		return 0, illegal(pc, instruction), nil
	default:

		return cpu.Trapped, notImplemented(pc, instruction), nil
	}
}

func divideSigned(left, right uint64) uint64 {
	if right == 0 {
		return ^uint64(0)
	}
	return uint64(int64(left) / int64(right))
}

func divideUnsigned(left, right uint64) uint64 {
	if right == 0 {
		return ^uint64(0)
	}
	return left / right
}

func remainderSigned(left, right uint64) uint64 {
	if right == 0 {
		return left
	}
	return uint64(int64(left) % int64(right))
}

func remainderUnsigned(left, right uint64) uint64 {
	if right == 0 {
		return left
	}
	return left % right
}

func divideSignedWord(left, right uint32) uint32 {
	if right == 0 {
		return ^uint32(0)
	}
	return uint32(int32(left) / int32(right))
}

func divideUnsignedWord(left, right uint32) uint32 {
	if right == 0 {
		return ^uint32(0)
	}
	return left / right
}

func remainderSignedWord(left, right uint32) uint32 {
	if right == 0 {
		return left
	}
	return uint32(int32(left) % int32(right))
}

func remainderUnsignedWord(left, right uint32) uint32 {
	if right == 0 {
		return left
	}
	return left % right
}

func mulHighSigned(left, right uint64) uint64 {
	high, _ := bits.Mul64(left, right)
	if int64(left) < 0 {
		high -= right
	}
	if int64(right) < 0 {
		high -= left
	}
	return high
}

func mulHighSignedUnsigned(left, right uint64) uint64 {
	high, _ := bits.Mul64(left, right)
	if int64(left) < 0 {
		high -= right
	}
	return high
}

func (c *Core) read(index int) uint64 {
	if index == regZero {
		return 0
	}
	return c.machine.Registers[index]
}

func (c *Core) write(index int, value uint64) {
	if index == regZero {
		return
	}
	c.machine.Registers[index] = value
}

func boolValue(condition bool) uint64 {
	if condition {
		return 1
	}
	return 0
}

func signExtend(value uint64, width uint) uint64 {
	mask := uint64(1)<<(width-1) - 1
	value &= mask<<1 | mask
	if value&(uint64(1)<<(width-1)) != 0 {
		value |= ^((uint64(1) << width) - 1)
	}
	return value
}

func illegal(pc uint64, instruction uint32) *cpu.Trap {
	return &cpu.Trap{Kind: cpu.TrapIllegalInstruction, Address: pc, Instruction: instruction}
}

func notImplemented(pc uint64, instruction uint32) *cpu.Trap {
	return &cpu.Trap{Kind: cpu.TrapNotImplemented, Address: pc, Instruction: instruction}
}

var (
	_ cpu.Port         = (*Core)(nil)
	_ cpu.RegisterFile = (*Core)(nil)
)
