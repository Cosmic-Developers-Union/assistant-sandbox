package riscv64

import (
	"context"
	"encoding/binary"
	"math/big"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// nanBoxS 把 32 位浮点位型按 RISC-V NaN 装箱规则写入 64 位寄存器。
func nanBoxS(bits uint32) uint64 {
	return 0xFFFFFFFF00000000 | uint64(bits)
}

func (c *Core) fregS(index int) uint32 {
	return uint32(c.machine.Floats[index])
}

func (c *Core) fregD(index int) uint64 {
	return c.machine.Floats[index]
}

func (c *Core) setFRegS(index int, bits uint32) {
	c.machine.Floats[index] = nanBoxS(bits)
}

func (c *Core) setFRegD(index int, bits uint64) {
	c.machine.Floats[index] = bits
}

// fmode 解析指令的 rm 字段;DYN 取 fcsr.frm,保留值非法。
func (c *Core) fmode(instruction uint32) (fpMode, *cpu.Trap) {
	rm := (instruction >> 12) & 0x7
	if rm == 0x7 {
		rm = c.machine.FCSR >> 5 & 0x7
	}
	if rm > 0x4 {
		return 0, illegal(0, instruction)
	}
	return fpMode(rm), nil
}

func (c *Core) addFlags(flags uint32) {
	c.machine.FCSR |= flags & 0x1F
}

// fpOperand 读取浮点操作数:width=4 读低 32 位,8 读完整 64 位。
func (c *Core) fpOperand(index int, width uint, p fpParams) fpValue {
	if width == 4 {
		return decodeFP(uint64(c.fregS(index)), p)
	}
	return decodeFP(c.fregD(index), p)
}

// fpLoad 实现 FLW/FLD(opcode 0x07)。
func (c *Core) fpLoad(ctx context.Context, pc uint64, instruction uint32, funct3 uint32, rd, rs1 int) (cpu.Outcome, *cpu.Trap, error) {
	if funct3 != 0x2 && funct3 != 0x3 {
		return 0, illegal(pc, instruction), nil
	}
	offset := signExtend(uint64(instruction)>>20, 12)
	address := c.read(rs1) + offset
	// funct3=2 是 32 位字(4 字节),3 是双字(8 字节)。
	width := 4
	if funct3 == 0x3 {
		width = 8
	}
	var raw [8]byte
	if err := c.memory.Read(ctx, address, raw[:width]); err != nil {
		return cpu.Trapped, &cpu.Trap{Kind: cpu.TrapMemoryFault, Address: pc, Instruction: instruction, Detail: address, Cause: err}, nil
	}
	if width == 4 {
		c.setFRegS(rd, binary.LittleEndian.Uint32(raw[:4]))
	} else {
		c.setFRegD(rd, binary.LittleEndian.Uint64(raw[:8]))
	}
	return cpu.Continued, nil, nil
}

// fpStore 实现 FSW/FSD(opcode 0x27)。
func (c *Core) fpStore(ctx context.Context, pc uint64, instruction uint32, funct3 uint32, rs1, rs2 int) (cpu.Outcome, *cpu.Trap, error) {
	if funct3 != 0x2 && funct3 != 0x3 {
		return 0, illegal(pc, instruction), nil
	}
	offset := signExtend(
		uint64(instruction>>25)<<5|
			uint64(instruction>>7)&0x1F, 12)
	address := c.read(rs1) + offset
	width := 4
	if funct3 == 0x3 {
		width = 8
	}
	var raw [8]byte
	if width == 4 {
		binary.LittleEndian.PutUint32(raw[:4], c.fregS(rs2))
	} else {
		binary.LittleEndian.PutUint64(raw[:8], c.fregD(rs2))
	}
	if err := c.memory.Write(ctx, address, raw[:width]); err != nil {
		return cpu.Trapped, &cpu.Trap{Kind: cpu.TrapMemoryFault, Address: pc, Instruction: instruction, Detail: address, Cause: err}, nil
	}
	return cpu.Continued, nil, nil
}

// fpFused 实现 FMADD/FMSUB/FNMSUB/FNMADD(opcode 0x43/0x47/0x4B/0x4F)。
// kind:bit1 = 乘积取负,bit0 = 加数取负(FMSUB 对加数取负)。
func (c *Core) fpFused(pc uint64, instruction uint32, width uint, p fpParams) (cpu.Outcome, *cpu.Trap, error) {
	mode, trap := c.fmode(instruction)
	if trap != nil {
		trap.Address = pc
		return cpu.Trapped, trap, nil
	}
	a := c.fpOperand(rs1Idx(instruction), width, p)
	b := c.fpOperand(int((instruction>>20)&0x1F), width, p)
	cV := c.fpOperand(int(instruction>>27), width, p)
	rd := int((instruction >> 7) & 0x1F)
	// R4 类型的 funct7 含 rs3 与 fmt,操作种类必须由 opcode 决定:
	// 0x43=FMADD、0x47=FMSUB、0x4B=FNMSUB、0x4F=FNMADD。
	kind := (instruction&0x7F - 0x43) / 4
	value, flags := fpMulAdd(a, b, cV, kind, p, mode)
	c.addFlags(flags)
	c.writeFP(rd, width, value)
	return cpu.Continued, nil, nil
}

// signedRat 返回带符号的精确值,零值返回 0。
func signedRat(v fpValue) *big.Rat {
	if v.class == classZero {
		return new(big.Rat)
	}
	if v.neg {
		return new(big.Rat).Neg(v.rat)
	}
	return v.rat
}

// fpMulAdd 计算 ±a×b ± c(单次舍入),特殊值按 IEEE 规则。
func fpMulAdd(a, b, cV fpValue, kind uint32, p fpParams, mode fpMode) (uint64, uint32) {
	if a.class == classNaN || b.class == classNaN || cV.class == classNaN {
		value, flags := nanResult(a, b, cV)
		return p.qnan | value, flags
	}
	prodNeg := a.neg != b.neg
	if kind&0x2 != 0 {
		prodNeg = !prodNeg
	}
	addNeg := cV.neg
	if kind&0x1 != 0 {
		addNeg = !addNeg
	}
	if a.class == classInf || b.class == classInf {
		other := b
		if a.class == classInf {
			other = b
			if b.class == classInf {
				// Inf×Inf:乘积仍为 Inf。
				if cV.class == classInf && addNeg != prodNeg {
					return p.qnan, flagNV
				}
				return encodeInf(prodNeg, p), 0
			}
		}
		if other.class == classZero {
			return p.qnan, flagNV
		}
		if cV.class == classInf && addNeg != prodNeg {
			return p.qnan, flagNV
		}
		return encodeInf(prodNeg, p), 0
	}
	if cV.class == classInf {
		return encodeInf(addNeg, p), 0
	}
	exact := new(big.Rat)
	if a.class != classZero && b.class != classZero {
		exact.Mul(signedRat(a), signedRat(b))
	}
	if kind&0x2 != 0 {
		exact.Neg(exact)
	}
	if cV.class != classZero {
		if addNeg {
			exact.Sub(exact, signedRat(cV))
		} else {
			exact.Add(exact, signedRat(cV))
		}
	}
	return fpRound(exact, p, mode, nil)
}

func rs1Idx(instruction uint32) int {
	return int((instruction >> 15) & 0x1F)
}

// fpOp 实现 OP-FP(opcode 0x53)的全部 F/D 指令。
// funct7 bit0 区分 S/D,共用分派按 funct7>>1 归组。
func (c *Core) fpOp(ctx context.Context, pc uint64, instruction uint32) (cpu.Outcome, *cpu.Trap, error) {
	funct7 := instruction >> 25
	rs2 := int((instruction >> 20) & 0x1F)
	rd := int((instruction >> 7) & 0x1F)
	funct3 := (instruction >> 12) & 0x7

	single := funct7&0x1 == 0
	p, width := fpD, uint(8)
	if single {
		p, width = fpS, uint(4)
	}
	switch funct7 >> 1 {
	case 0x00: // FADD.S/D
		return c.fpAddSub(pc, instruction, width, p, false)
	case 0x02: // FSUB.S/D
		return c.fpAddSub(pc, instruction, width, p, true)
	case 0x04: // FMUL.S/D
		return c.fpBinaryOp(pc, instruction, width, p, func(a, b *big.Rat) *big.Rat { return new(big.Rat).Mul(a, b) })
	case 0x06: // FDIV.S/D
		return c.fpDiv(pc, instruction, width, p)
	case 0x16: // FSQRT.S/D(rs2 必须为 0)
		if rs2 != 0 {
			return 0, illegal(pc, instruction), nil
		}
		return c.fpSqrtOp(pc, instruction, width, p)
	case 0x10: // FCVT.S.D / FCVT.D.S(rs2 必须为 0)
		if rs2 != 0 {
			return 0, illegal(pc, instruction), nil
		}
		return c.fpConvertWidth(pc, instruction, single)
	case 0x28: // FLE/FLT/FEQ
		return c.fpCompare(pc, instruction, width, p, funct3)
	case 0x08: // FSGNJ/FSGNJN/FSGNJX
		return c.fpSignInject(pc, instruction, width, funct3)
	case 0x0A: // FMIN/FMAX
		return c.fpMinMax(pc, instruction, width, p, funct3)
	case 0x38: // FMV.X.W/X.D 与 FCLASS.S/D
		return c.fpMoveToInt(pc, instruction, width, p, rs2, rd)
	case 0x3C: // FMV.W.X / FMV.D.X
		if rs2 != 0 {
			return 0, illegal(pc, instruction), nil
		}
		if single {
			c.setFRegS(rd, uint32(c.read(rs1Idx(instruction))))
		} else {
			c.setFRegD(rd, c.read(rs1Idx(instruction)))
		}
		return cpu.Continued, nil, nil
	case 0x30: // FCVT.W/WU/L/LU.S 或 .D(浮点→整数)
		return c.fpToInt(pc, instruction, width, p, rs2, rd)
	case 0x34: // FCVT.S/D.W/WU/L/LU(整数→浮点)
		return c.fpFromInt(pc, instruction, single, p, rs2, rd)
	default:
		return 0, illegal(pc, instruction), nil
	}
}

// fpAddSub 处理加减:先翻转减数符号,统一按加法的 Inf/NaN 规则处理。
func (c *Core) fpAddSub(pc uint64, instruction uint32, width uint, p fpParams, sub bool) (cpu.Outcome, *cpu.Trap, error) {
	mode, trap := c.fmode(instruction)
	if trap != nil {
		trap.Address = pc
		return cpu.Trapped, trap, nil
	}
	a := c.fpOperand(rs1Idx(instruction), width, p)
	b := c.fpOperand(int((instruction>>20)&0x1F), width, p)
	rd := int((instruction >> 7) & 0x1F)
	if sub {
		b.neg = !b.neg
	}
	if a.class == classNaN || b.class == classNaN {
		value, flags := nanResult(a, b)
		c.addFlags(flags)
		c.writeFP(rd, width, p.qnan|value)
		return cpu.Continued, nil, nil
	}
	if a.class == classInf && b.class == classInf {
		if a.neg != b.neg {
			c.addFlags(flagNV)
			c.writeFP(rd, width, p.qnan)
			return cpu.Continued, nil, nil
		}
		c.writeFP(rd, width, encodeInf(a.neg, p))
		return cpu.Continued, nil, nil
	}
	if a.class == classInf {
		c.writeFP(rd, width, encodeInf(a.neg, p))
		return cpu.Continued, nil, nil
	}
	if b.class == classInf {
		c.writeFP(rd, width, encodeInf(b.neg, p))
		return cpu.Continued, nil, nil
	}
	exact := new(big.Rat).Add(signedRat(a), signedRat(b))
	var forced *bool
	if exact.Sign() == 0 && a.class == classZero && b.class == classZero {
		both := a.neg && b.neg
		forced = &both
	}
	value, flags := fpRound(exact, p, mode, forced)
	c.addFlags(flags)
	c.writeFP(rd, width, value)
	return cpu.Continued, nil, nil
}

// fpBinaryOp 执行乘法(精确有理数路径)。
func (c *Core) fpBinaryOp(pc uint64, instruction uint32, width uint, p fpParams,
	op func(a, b *big.Rat) *big.Rat) (cpu.Outcome, *cpu.Trap, error) {
	mode, trap := c.fmode(instruction)
	if trap != nil {
		trap.Address = pc
		return cpu.Trapped, trap, nil
	}
	a := c.fpOperand(rs1Idx(instruction), width, p)
	b := c.fpOperand(int((instruction>>20)&0x1F), width, p)
	rd := int((instruction >> 7) & 0x1F)
	if a.class == classNaN || b.class == classNaN {
		value, flags := nanResult(a, b)
		c.addFlags(flags)
		c.writeFP(rd, width, p.qnan|value)
		return cpu.Continued, nil, nil
	}
	if a.class == classInf || b.class == classInf {
		other := a
		if a.class == classInf {
			other = b
		}
		if other.class == classZero {
			c.addFlags(flagNV)
			c.writeFP(rd, width, p.qnan)
			return cpu.Continued, nil, nil
		}
		c.writeFP(rd, width, encodeInf(a.neg != b.neg, p))
		return cpu.Continued, nil, nil
	}
	exact := op(signedRat(a), signedRat(b))
	var forced *bool
	if exact.Sign() == 0 {
		sign := a.neg != b.neg
		forced = &sign
	}
	value, flags := fpRound(exact, p, mode, forced)
	c.addFlags(flags)
	c.writeFP(rd, width, value)
	return cpu.Continued, nil, nil
}

// fpDiv 处理除零的 DZ 与 Inf×0 的 NV 规则。
func (c *Core) fpDiv(pc uint64, instruction uint32, width uint, p fpParams) (cpu.Outcome, *cpu.Trap, error) {
	mode, trap := c.fmode(instruction)
	if trap != nil {
		trap.Address = pc
		return cpu.Trapped, trap, nil
	}
	a := c.fpOperand(rs1Idx(instruction), width, p)
	b := c.fpOperand(int((instruction>>20)&0x1F), width, p)
	rd := int((instruction >> 7) & 0x1F)
	if a.class == classNaN || b.class == classNaN {
		value, flags := nanResult(a, b)
		c.addFlags(flags)
		c.writeFP(rd, width, p.qnan|value)
		return cpu.Continued, nil, nil
	}
	neg := a.neg != b.neg
	switch {
	case a.class == classInf && b.class == classInf:
		c.addFlags(flagNV)
		c.writeFP(rd, width, p.qnan)
	case a.class == classInf:
		c.writeFP(rd, width, encodeInf(neg, p))
	case b.class == classInf:
		c.writeFP(rd, width, negBits(neg, p))
	case a.class == classZero && b.class == classZero:
		c.addFlags(flagNV)
		c.writeFP(rd, width, p.qnan)
	case b.class == classZero:
		c.addFlags(flagDZ)
		c.writeFP(rd, width, encodeInf(neg, p))
	default:
		exact := new(big.Rat).Quo(signedRat(a), signedRat(b))
		var forced *bool
		if exact.Sign() == 0 {
			forced = &neg
		}
		value, flags := fpRound(exact, p, mode, forced)
		c.addFlags(flags)
		c.writeFP(rd, width, value)
	}
	return cpu.Continued, nil, nil
}

// fpSqrtOp 实现 FSQRT:sqrt(±0) = ±0,sqrt(-x) = NaN+NV。
func (c *Core) fpSqrtOp(pc uint64, instruction uint32, width uint, p fpParams) (cpu.Outcome, *cpu.Trap, error) {
	mode, trap := c.fmode(instruction)
	if trap != nil {
		trap.Address = pc
		return cpu.Trapped, trap, nil
	}
	a := c.fpOperand(rs1Idx(instruction), width, p)
	rd := int((instruction >> 7) & 0x1F)
	switch {
	case a.class == classNaN:
		value, flags := nanResult(a)
		c.addFlags(flags)
		c.writeFP(rd, width, p.qnan|value)
	case a.class == classZero:
		c.writeFP(rd, width, negBits(a.neg, p))
	case a.class == classInf:
		c.writeFP(rd, width, encodeInf(a.neg, p))
	case a.neg:
		c.addFlags(flagNV)
		c.writeFP(rd, width, p.qnan)
	default:
		value, flags := fpSqrt(a.rat, p, mode)
		c.addFlags(flags)
		c.writeFP(rd, width, value)
	}
	return cpu.Continued, nil, nil
}

// fpConvertWidth 实现 FCVT.S.D(D 舍入到 S)与 FCVT.D.S(S 精确扩展)。
func (c *Core) fpConvertWidth(pc uint64, instruction uint32, single bool) (cpu.Outcome, *cpu.Trap, error) {
	mode, trap := c.fmode(instruction)
	if trap != nil {
		trap.Address = pc
		return cpu.Trapped, trap, nil
	}
	rd := int((instruction >> 7) & 0x1F)
	if single {
		a := c.fpOperand(rs1Idx(instruction), 8, fpD)
		switch a.class {
		case classNaN:
			value, flags := nanResult(a)
			c.addFlags(flags)
			c.setFRegS(rd, uint32(fpS.qnan|value))
		case classInf:
			c.setFRegS(rd, uint32(encodeInf(a.neg, fpS)))
		case classZero:
			c.setFRegS(rd, uint32(negBits(a.neg, fpS)))
		default:
			value, flags := fpRound(signedRat(a), fpS, mode, nil)
			c.addFlags(flags)
			c.setFRegS(rd, uint32(value))
		}
		return cpu.Continued, nil, nil
	}
	// FCVT.D.S:S 值精确扩展为真正的 D 编码(S→D 永不失真,无标志)。
	a := c.fpOperand(rs1Idx(instruction), 4, fpS)
	switch a.class {
	case classNaN:
		c.setFRegD(rd, fpD.qnan)
	case classInf:
		c.setFRegD(rd, encodeInf(a.neg, fpD))
	case classZero:
		c.setFRegD(rd, negBits(a.neg, fpD))
	default:
		value, flags := fpRound(signedRat(a), fpD, modeRNE, nil)
		c.addFlags(flags)
		c.setFRegD(rd, value)
	}
	return cpu.Continued, nil, nil
}

// fpCompare 实现 FLE/FLT/FEQ(FLT/FLE 遇 NaN 置 NV,FEQ 仅 sNaN)。
func (c *Core) fpCompare(pc uint64, instruction uint32, width uint, p fpParams, funct3 uint32) (cpu.Outcome, *cpu.Trap, error) {
	a := c.fpOperand(rs1Idx(instruction), width, p)
	b := c.fpOperand(int((instruction>>20)&0x1F), width, p)
	rd := int((instruction >> 7) & 0x1F)
	if a.class == classNaN || b.class == classNaN {
		flags := flagNV
		if funct3 == 0x2 {
			_, flags = nanResult(a, b)
		}
		c.addFlags(flags)
		c.write(rd, 0)
		return cpu.Continued, nil, nil
	}
	var result bool
	switch funct3 {
	case 0x0:
		result = fpLE(a, b)
	case 0x1:
		result = fpLT(a, b)
	default:
		result = fpEQ(a, b)
	}
	c.write(rd, boolValue(result))
	return cpu.Continued, nil, nil
}

func fpEQ(a, b fpValue) bool {
	if a.class == classZero && b.class == classZero {
		return true
	}
	if a.class != classFinite || b.class != classFinite {
		return false
	}
	return a.neg == b.neg && a.rat.Cmp(b.rat) == 0
}

func fpLT(a, b fpValue) bool {
	if a.class == classZero && b.class == classZero {
		return false
	}
	return fpOrder(a, b) < 0
}

func fpLE(a, b fpValue) bool {
	if a.class == classZero && b.class == classZero {
		return true
	}
	return fpOrder(a, b) <= 0
}

// fpOrder 按全序比较两个非 NaN 值:-Inf < ±0 < 有限值 < +Inf。
func fpOrder(a, b fpValue) int {
	rank := func(v fpValue) int {
		switch {
		case v.class == classInf:
			if v.neg {
				return 0
			}
			return 4
		case v.class == classZero:
			return 2
		case v.neg:
			return 1
		default:
			return 3
		}
	}
	ra, rb := rank(a), rank(b)
	if ra != rb {
		if ra < rb {
			return -1
		}
		return 1
	}
	switch ra {
	case 0, 2, 4:
		return 0
	case 1:
		return b.rat.Cmp(a.rat)
	default:
		return a.rat.Cmp(b.rat)
	}
}

// fpSignInject 实现 FSGNJ/FSGNJN/FSGNJX(无标志,保留载荷)。
func (c *Core) fpSignInject(pc uint64, instruction uint32, width uint, funct3 uint32) (cpu.Outcome, *cpu.Trap, error) {
	rs1 := rs1Idx(instruction)
	rs2 := int((instruction >> 20) & 0x1F)
	rd := int((instruction >> 7) & 0x1F)
	if width == 4 {
		a, b := c.fregS(rs1), c.fregS(rs2)
		var sign uint32
		switch funct3 {
		case 0x1: // FSGNJN
			sign = (b ^ 0x80000000) & 0x80000000
		case 0x2: // FSGNJX
			sign = (a ^ b) & 0x80000000
		default:
			sign = b & 0x80000000
		}
		c.setFRegS(rd, sign|a&0x7FFFFFFF)
		return cpu.Continued, nil, nil
	}
	a, b := c.fregD(rs1), c.fregD(rs2)
	var sign uint64
	switch funct3 {
	case 0x1:
		sign = (b ^ 0x8000000000000000) & 0x8000000000000000
	case 0x2:
		sign = (a ^ b) & 0x8000000000000000
	default:
		sign = b & 0x8000000000000000
	}
	c.setFRegD(rd, sign|a&0x7FFFFFFFFFFFFFFF)
	return cpu.Continued, nil, nil
}

// fpMinMax 实现 FMIN/FMAX:NaN → 规范 qNaN 无标志;Min(-0,+0) = -0,Max(-0,+0) = +0。
func (c *Core) fpMinMax(pc uint64, instruction uint32, width uint, p fpParams, funct3 uint32) (cpu.Outcome, *cpu.Trap, error) {
	a := c.fpOperand(rs1Idx(instruction), width, p)
	b := c.fpOperand(int((instruction>>20)&0x1F), width, p)
	rd := int((instruction >> 7) & 0x1F)
	isMin := funct3 == 0x0
	if a.class == classNaN || b.class == classNaN {
		c.writeFP(rd, width, p.qnan)
		return cpu.Continued, nil, nil
	}
	if a.class == classZero && b.class == classZero {
		sign := a.neg && b.neg
		if isMin {
			sign = a.neg || b.neg
		}
		c.writeFP(rd, width, negBits(sign, p))
		return cpu.Continued, nil, nil
	}
	takeA := fpOrder(a, b) <= 0
	if !isMin {
		takeA = fpOrder(a, b) >= 0
	}
	if width == 4 {
		if takeA {
			c.setFRegS(rd, c.fregS(rs1Idx(instruction)))
		} else {
			c.setFRegS(rd, c.fregS(int((instruction>>20)&0x1F)))
		}
		return cpu.Continued, nil, nil
	}
	if takeA {
		c.setFRegD(rd, c.fregD(rs1Idx(instruction)))
	} else {
		c.setFRegD(rd, c.fregD(int((instruction>>20)&0x1F)))
	}
	return cpu.Continued, nil, nil
}

// writeFP 写回浮点结果,S 值带 NaN 装箱。
func (c *Core) writeFP(rd int, width uint, value uint64) {
	if width == 4 {
		c.setFRegS(rd, uint32(value))
		return
	}
	c.setFRegD(rd, value)
}

// fclassBits 计算 FCLASS 的 10 位掩码。
func fclassBits(bits uint64, p fpParams) uint64 {
	neg := bits>>(p.prec-1)&1 != 0
	mant := bits & ((uint64(1) << (p.prec - 1)) - 1)
	rawExp := int(bits>>p.expShift()) & p.expMask()
	maxExp := p.expMask()
	switch {
	case rawExp == maxExp && mant != 0:
		if mant>>(p.prec-2)&1 == 0 {
			return 1 << 8 // signaling NaN
		}
		return 1 << 9 // quiet NaN
	case rawExp == maxExp:
		if neg {
			return 1 << 0 // -Inf
		}
		return 1 << 7 // +Inf
	case rawExp == 0 && mant == 0:
		if neg {
			return 1 << 3 // -0
		}
		return 1 << 4 // +0
	case rawExp == 0:
		if neg {
			return 1 << 2 // -subnormal
		}
		return 1 << 5 // +subnormal
	default:
		if neg {
			return 1 << 1 // -normal
		}
		return 1 << 6 // +normal
	}
}

// fpMoveToInt 实现 FMV.X.W/X.D 与 FCLASS.S/D。
func (c *Core) fpMoveToInt(pc uint64, instruction uint32, width uint, p fpParams, rs2, rd int) (cpu.Outcome, *cpu.Trap, error) {
	if rs2 == 1 {
		if width == 4 {
			c.write(rd, fclassBits(uint64(c.fregS(rs1Idx(instruction))), fpS))
		} else {
			c.write(rd, fclassBits(c.fregD(rs1Idx(instruction)), fpD))
		}
		return cpu.Continued, nil, nil
	}
	if rs2 != 0 {
		return 0, illegal(pc, instruction), nil
	}
	if width == 4 {
		c.write(rd, signExtend(uint64(c.fregS(rs1Idx(instruction))), 32))
		return cpu.Continued, nil, nil
	}
	c.write(rd, c.fregD(rs1Idx(instruction)))
	return cpu.Continued, nil, nil
}

// fpToInt 实现 FCVT.W/WU/L/LU.S 与 .D(浮点→整数)。
// NaN 或越界返回宽度内最大值并置 NV。
func (c *Core) fpToInt(pc uint64, instruction uint32, width uint, p fpParams, rs2, rd int) (cpu.Outcome, *cpu.Trap, error) {
	mode, trap := c.fmode(instruction)
	if trap != nil {
		trap.Address = pc
		return cpu.Trapped, trap, nil
	}
	a := c.fpOperand(rs1Idx(instruction), width, p)
	if rs2 > 0x3 {
		return 0, illegal(pc, instruction), nil
	}
	signed := rs2 == 0x0 || rs2 == 0x2
	bits := 32
	if rs2 >= 0x2 {
		bits = 64
	}
	if a.class == classNaN {
		c.addFlags(flagNV)
		c.write(rd, maxUint(bits, signed, false))
		return cpu.Continued, nil, nil
	}
	if a.class == classInf {
		c.addFlags(flagNV)
		negative := a.neg && signed
		c.write(rd, maxUint(bits, signed, negative))
		return cpu.Continued, nil, nil
	}
	value, exact := roundToInt(a.rat, mode, a.neg)
	if !inRange(value, bits, signed) {
		c.addFlags(flagNV)
		c.write(rd, maxUint(bits, signed, value.Sign() < 0))
		return cpu.Continued, nil, nil
	}
	_ = exact
	c.write(rd, widenInt(value, bits, signed))
	return cpu.Continued, nil, nil
}

// roundToInt 按目标舍入模式把精确有理数舍入为整数。
func roundToInt(r *big.Rat, mode fpMode, neg bool) (*big.Int, bool) {
	num, den := r.Num(), r.Denom()
	absNum := new(big.Int).Abs(num)
	q, rem := new(big.Int).QuoRem(absNum, den, new(big.Int))
	inexact := rem.Sign() != 0
	if inexact {
		twice := new(big.Int).Lsh(rem, 1)
		switch mode {
		case modeRNE:
			switch twice.Cmp(den) {
			case 1:
				q.Add(q, big.NewInt(1))
			case 0:
				if q.Bit(0) == 1 {
					q.Add(q, big.NewInt(1))
				}
			}
		case modeRMM:
			if twice.Cmp(den) >= 0 {
				q.Add(q, big.NewInt(1))
			}
		case modeRDN:
			// 向 -∞:负数向下补一。
			if neg {
				q.Add(q, big.NewInt(1))
			}
		case modeRUP:
			if !neg {
				q.Add(q, big.NewInt(1))
			}
		}
	}
	if neg {
		q.Neg(q)
	}
	return q, !inexact
}

func inRange(value *big.Int, bits int, signed bool) bool {
	if signed {
		limit := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
		return value.Cmp(new(big.Int).Sub(limit, big.NewInt(1))) <= 0 && value.Cmp(new(big.Int).Neg(limit)) >= 0
	}
	if value.Sign() < 0 {
		return false
	}
	return value.BitLen() <= bits
}

func maxUint(bits int, signed, negative bool) uint64 {
	if signed {
		limit := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
		if negative {
			value := new(big.Int).Neg(limit)
			return value.Uint64()
		}
		return new(big.Int).Sub(limit, big.NewInt(1)).Uint64()
	}
	limit := new(big.Int).Lsh(big.NewInt(1), uint(bits))
	return new(big.Int).Sub(limit, big.NewInt(1)).Uint64()
}

func widenInt(value *big.Int, bits int, signed bool) uint64 {
	// big.Int 负值的 Uint64 未定义;先按 64 位补码截断。
	raw := new(big.Int).And(value, new(big.Int).SetUint64(^uint64(0))).Uint64()
	if signed {
		if bits == 32 {
			return signExtend(raw&0xFFFFFFFF, 32)
		}
		return raw
	}
	if bits == 32 {
		return raw & 0xFFFFFFFF
	}
	return raw
}

// fpFromInt 实现 FCVT.S/D.W/WU/L/LU(整数→浮点)。
func (c *Core) fpFromInt(pc uint64, instruction uint32, single bool, p fpParams, rs2, rd int) (cpu.Outcome, *cpu.Trap, error) {
	if rs2 > 0x3 {
		return 0, illegal(pc, instruction), nil
	}
	mode, trap := c.fmode(instruction)
	if trap != nil {
		trap.Address = pc
		return cpu.Trapped, trap, nil
	}
	raw := c.read(rs1Idx(instruction))
	var value *big.Int
	switch rs2 {
	case 0x0:
		value = big.NewInt(int64(int32(raw)))
	case 0x1:
		value = new(big.Int).SetUint64(raw & 0xFFFFFFFF)
	case 0x2:
		value = big.NewInt(int64(raw))
	default:
		value = new(big.Int).SetUint64(raw)
	}
	rat := new(big.Rat).SetInt(value)
	fp := fpS
	if !single {
		fp = fpD
	}
	// 整数源非零时符号由值决定。
	out, flags := fpRound(rat, fp, mode, nil)
	c.addFlags(flags)
	if single {
		c.setFRegS(rd, uint32(out))
	} else {
		c.setFRegD(rd, out)
	}
	return cpu.Continued, nil, nil
}

// fpShape 由指令的 funct7 bit0 决定操作数宽度与格式参数。
func fpShape(instruction uint32) (uint, fpParams) {
	if instruction>>25&0x1 == 0 {
		return 4, fpS
	}
	return 8, fpD
}

// csrFloat 实现 fflags/frm/fcsr 的读写;写意图在只读语义下仍是非法指令,
// 但这三个寄存器可写,处理 CSRRW/S/C 与立即数形式。
func (c *Core) csrFloat(pc uint64, instruction uint32, funct3 uint32, rd, rs1 int, address uint64) (cpu.Outcome, *cpu.Trap, error) {
	var source uint64
	if funct3 < 0x5 {
		source = c.read(rs1)
	} else {
		source = uint64(rs1)
	}
	read := func() uint64 {
		switch address {
		case csrFFlags:
			return uint64(c.machine.FCSR & 0x1F)
		case csrFrm:
			return uint64(c.machine.FCSR >> 5 & 0x7)
		default:
			return uint64(c.machine.FCSR & 0xFF)
		}
	}
	write := func(value uint64) {
		switch address {
		case csrFFlags:
			c.machine.FCSR = c.machine.FCSR&^0x1F | uint32(value)&0x1F
		case csrFrm:
			c.machine.FCSR = c.machine.FCSR&^0xE0 | uint32(value&0x7)<<5
		default:
			c.machine.FCSR = uint32(value) & 0xFF
		}
	}
	old := read()
	switch funct3 {
	case 0x1, 0x5: // CSRRW:无条件写
		write(source)
	case 0x2, 0x6: // CSRRS:源非零才置位
		if source != 0 {
			write(old | source)
		}
	case 0x3, 0x7: // CSRRC:源非零才清位
		if source != 0 {
			write(old &^ source)
		}
	}
	c.write(rd, old)
	return cpu.Continued, nil, nil
}

// ReadFP 读浮点寄存器原始位型。
func (c *Core) ReadFP(index int) (uint64, error) {
	if index < 0 || index >= numFPRegs {
		return 0, errno.Wrap(errno.Invalid, "浮点寄存器编号越界")
	}
	return c.machine.Floats[index], nil
}

// WriteFP 写浮点寄存器原始位型。
func (c *Core) WriteFP(index int, value uint64) error {
	if index < 0 || index >= numFPRegs {
		return errno.Wrap(errno.Invalid, "浮点寄存器编号越界")
	}
	c.machine.Floats[index] = value
	return nil
}

// ReadFCSR 读浮点控制状态寄存器。
func (c *Core) ReadFCSR() (uint32, error) {
	return c.machine.FCSR, nil
}

// WriteFCSR 写浮点控制状态寄存器(仅低 8 位有效)。
func (c *Core) WriteFCSR(value uint32) error {
	c.machine.FCSR = value & 0xFF
	return nil
}

var _ cpu.FloatRegisterFile = (*Core)(nil)
