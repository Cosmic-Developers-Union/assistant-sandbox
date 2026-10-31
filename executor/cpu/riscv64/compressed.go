package riscv64

type compressedStatus uint8

const (
	compressedOK compressedStatus = iota
	compressedReserved
)

// expandCompressed 把 RV64C 编码翻译为等价的 32 位指令,由既有执行路径处理。
// compressedReserved 表示保留编码(含显式非法的 0x0000)。
// HINT 编码按其基指令展开,无副作用。
func expandCompressed(ins uint16) (uint32, compressedStatus) {
	switch ins & 0x3 {
	case 0x0:
		return expandQuadrant0(ins)
	case 0x1:
		return expandQuadrant1(ins)
	default:
		return expandQuadrant2(ins)
	}
}

func rvcIndex(ins uint16, shift uint) int {
	return 8 + (int(ins)>>int(shift))&0x7
}

func expandQuadrant0(ins uint16) (uint32, compressedStatus) {
	switch (ins >> 13) & 0x7 {
	case 0x0:
		// C.ADDI4SPN → ADDI rd', x2, nzuimm;nzuimm=0 为保留编码。
		rd := rvcIndex(ins, 2)
		imm := int32((uint32(ins>>11)&0x3)<<4 |
			(uint32(ins>>7)&0xF)<<6 |
			(uint32(ins>>6)&1)<<2 |
			(uint32(ins>>5)&1)<<3)
		if imm == 0 {
			return 0, compressedReserved
		}
		return encodeOpImm(0x0, rd, regSP, uint32(imm)), compressedOK
	case 0x1, 0x5:
		// C.FLD/C.FSD(D 扩展落地后可展开):与 C.LD/C.SD 同布局,
		// 展开为 32 位 FLD/FSD。
		offset := fpLoadStoreOffset(ins)
		if (ins>>13)&0x7 == 0x1 {
			return encodeFPLoad(0x3, rvcIndex(ins, 2), rvcIndex(ins, 7), offset), compressedOK
		}
		return encodeFPStore(0x3, rvcIndex(ins, 2), rvcIndex(ins, 7), offset), compressedOK
	case 0x2:
		return encodeLoad(0x2, rvcIndex(ins, 2), rvcIndex(ins, 7), loadStoreWordOffset(ins)), compressedOK
	case 0x3:
		return encodeLoad(0x3, rvcIndex(ins, 2), rvcIndex(ins, 7), loadStoreDoubleOffset(ins)), compressedOK
	case 0x6:
		return encodeStore(0x2, rvcIndex(ins, 2), rvcIndex(ins, 7), loadStoreWordOffset(ins)), compressedOK
	case 0x7:
		return encodeStore(0x3, rvcIndex(ins, 2), rvcIndex(ins, 7), loadStoreDoubleOffset(ins)), compressedOK
	default:
		return 0, compressedReserved
	}
}

func loadStoreWordOffset(ins uint16) uint32 {
	return (uint32(ins>>10)&0x7)<<3 |
		(uint32(ins>>6)&1)<<2 |
		(uint32(ins>>5)&1)<<6
}

func loadStoreDoubleOffset(ins uint16) uint32 {
	return (uint32(ins>>10)&0x7)<<3 |
		(uint32(ins>>5)&0x3)<<6
}

// fpLoadStoreOffset 是 C.FLD/C.FSD 的偏移:uimm[5:3|7:6],与整数
// C.LD/C.SD 的位布局一致。
func fpLoadStoreOffset(ins uint16) uint32 {
	return (uint32(ins>>10)&0x7)<<3 |
		(uint32(ins>>5)&0x3)<<6
}

func expandQuadrant1(ins uint16) (uint32, compressedStatus) {
	rd := int((ins >> 7) & 0x1F)
	switch (ins >> 13) & 0x7 {
	case 0x0:
		// C.ADDI / C.NOP(rd=x0 时按 HINT 展开为基指令)。
		return encodeOpImm(0x0, rd, rd, uint32(signExtend6(ins))), compressedOK
	case 0x1:
		// RV64: C.ADDIW 展开 addiw rd, rd, imm(sext.w 即 imm=0 的形式)。
		return encodeOpImmWord(0x0, rd, rd, uint32(signExtend6(ins))), compressedOK
	case 0x2:
		// C.LI。
		return encodeOpImm(0x0, rd, regZero, uint32(signExtend6(ins))), compressedOK
	case 0x3:
		if rd == regSP {
			// C.ADDI16SP;nzimm=0 为保留编码。
			imm := addi16spImmediate(ins)
			if imm == 0 {
				return 0, compressedReserved
			}
			return encodeOpImm(0x0, regSP, regSP, uint32(imm)), compressedOK
		}
		// C.LUI;rd=x0 或 imm=0 为保留编码。
		imm := int32(signExtend(
			uint64(uint32(ins>>12)&1)<<17|uint64(uint32(ins>>2)&0x1F)<<12, 18))
		if rd == regZero || imm == 0 {
			return 0, compressedReserved
		}
		return encodeLUI(rd, uint32(imm)), compressedOK
	case 0x4:
		return expandArithmetic(ins)
	case 0x5:
		return encodeJAL(regZero, jumpOffset(ins)), compressedOK
	case 0x6:
		return encodeBranch(0x0, rvcIndex(ins, 7), regZero, branchOffset(ins)), compressedOK
	default:
		return encodeBranch(0x1, rvcIndex(ins, 7), regZero, branchOffset(ins)), compressedOK
	}
}

func expandArithmetic(ins uint16) (uint32, compressedStatus) {
	rd := rvcIndex(ins, 7)
	rs2 := rvcIndex(ins, 2)
	shamt := uint32(ins>>12)&1<<5 | uint32(ins>>2)&0x1F
	switch (ins >> 10) & 0x3 {
	case 0x0:
		// C.SRLI(shamt 为 6 位,RV64 允许 ≥32)。
		return encodeOpImm(0x5, rd, rd, shamt), compressedOK
	case 0x1:
		// C.SRAI(funct6=010000)。
		return encodeOpImm(0x5, rd, rd, 0x10<<6|shamt), compressedOK
	case 0x2:
		return encodeOpImm(0x7, rd, rd, uint32(signExtend6(ins))), compressedOK
	default:
		// bit12=0: C.SUB/XOR/OR/AND;bit12=1: RV64 的 C.SUBW/C.ADDW,其余保留。
		if ins>>12&1 != 0 {
			switch (ins >> 5) & 0x3 {
			case 0x0:
				return encodeOpWord(0x20, 0x0, rd, rd, rs2), compressedOK
			case 0x1:
				return encodeOpWord(0x00, 0x0, rd, rd, rs2), compressedOK
			default:
				return 0, compressedReserved
			}
		}
		switch (ins >> 5) & 0x3 {
		case 0x0:
			return encodeOp(0x20, 0x0, rd, rd, rs2), compressedOK
		case 0x1:
			return encodeOp(0x00, 0x4, rd, rd, rs2), compressedOK
		case 0x2:
			return encodeOp(0x00, 0x6, rd, rd, rs2), compressedOK
		default:
			return encodeOp(0x00, 0x7, rd, rd, rs2), compressedOK
		}
	}
}

func expandQuadrant2(ins uint16) (uint32, compressedStatus) {
	rd := int((ins >> 7) & 0x1F)
	rs1 := int((ins >> 7) & 0x1F)
	rs2 := int((ins >> 2) & 0x1F)
	switch (ins >> 13) & 0x7 {
	case 0x0:
		// C.SLLI(rd=x0 为 HINT,展开无害)。
		shamt := uint32(ins>>12)&1<<5 | uint32(ins>>2)&0x1F
		return encodeOpImm(0x1, rd, rd, shamt), compressedOK
	case 0x1, 0x5:
		// C.FLDSP/C.FSDSP(D 扩展落地后可展开):与 C.LDSP/C.SDSP 同布局,
		// 展开为 32 位 FLD/FSD(sp 基址)。
		if (ins>>13)&0x7 == 0x1 {
			offset := uint32(ins>>12)&1<<5 |
				uint32(ins>>5)&0x3<<3 |
				uint32(ins>>2)&0x7<<6
			return encodeFPLoad(0x3, rd, regSP, offset), compressedOK
		}
		offset := uint32(ins>>10)&0x7<<3 |
			uint32(ins>>7)&0x7<<6
		return encodeFPStore(0x3, rs2, regSP, offset), compressedOK
	case 0x2:
		// C.LWSP;rd=0 为保留编码。
		if rd == regZero {
			return 0, compressedReserved
		}
		imm := uint32(ins>>12)&1<<5 |
			uint32(ins>>4)&0x7<<2 |
			uint32(ins>>2)&0x3<<6
		return encodeLoad(0x2, rd, regSP, imm), compressedOK
	case 0x3:
		// C.LDSP;rd=0 为保留编码。
		if rd == regZero {
			return 0, compressedReserved
		}
		imm := uint32(ins>>12)&1<<5 |
			uint32(ins>>5)&0x3<<3 |
			uint32(ins>>2)&0x7<<6
		return encodeLoad(0x3, rd, regSP, imm), compressedOK
	case 0x4:
		switch ins >> 12 & 1 {
		case 0:
			// funct4=1000: C.JR / C.MV。
			if rs2 == regZero {
				if rs1 == regZero {
					return 0, compressedReserved
				}
				return encodeJALR(regZero, rs1, 0), compressedOK
			}
			// rd=x0 时为 HINT,展开无害。
			return encodeOp(0x00, 0x0, rd, regZero, rs2), compressedOK
		default:
			// funct4=1001: C.JALR / C.EBREAK / C.ADD。
			switch {
			case rs2 == regZero && rd == regZero:
				return insEbreakEncoding, compressedOK
			case rs2 == regZero:
				return encodeJALR(regRA, rs1, 0), compressedOK
			default:
				// rd=x0 时为 HINT,展开无害。
				return encodeOp(0x00, 0x0, rd, rd, rs2), compressedOK
			}
		}
	case 0x6:
		// C.SWSP。
		imm := uint32(ins>>9)&0xF<<2 |
			uint32(ins>>7)&0x3<<6
		return encodeStore(0x2, rs2, regSP, imm), compressedOK
	case 0x7:
		// C.SDSP。
		imm := uint32(ins>>10)&0x7<<3 |
			uint32(ins>>7)&0x7<<6
		return encodeStore(0x3, rs2, regSP, imm), compressedOK
	default:
		return 0, compressedReserved
	}
}

// addi16spImmediate 解码 C.ADDI16SP 的 6 位无零立即数域:nzimm[9|4|6|8|7|5]
// 依次占用 inst[12]、inst[6]、inst[5]、inst[4]、inst[3]、inst[2],值 = 域×16。
func addi16spImmediate(ins uint16) int32 {
	raw := uint32(ins>>12)&1<<5 |
		uint32(ins>>6)&1<<0 |
		uint32(ins>>2)&1<<1 |
		uint32(ins>>5)&1<<2 |
		uint32(ins>>3)&1<<3 |
		uint32(ins>>4)&1<<4
	return int32(signExtend(uint64(raw), 6)) << 4
}

func jumpOffset(ins uint16) int32 {
	return int32(signExtend(uint64(
		uint32(ins>>12)&1<<11|
			uint32(ins>>11)&1<<4|
			uint32(ins>>9)&3<<8|
			uint32(ins>>8)&1<<10|
			uint32(ins>>7)&1<<6|
			uint32(ins>>6)&1<<7|
			uint32(ins>>3)&7<<1|
			uint32(ins>>2)&1<<5), 12))
}

func branchOffset(ins uint16) int32 {
	return int32(signExtend(uint64(
		uint32(ins>>12)&1<<8|
			uint32(ins>>10)&3<<3|
			uint32(ins>>5)&3<<6|
			uint32(ins>>3)&3<<1|
			uint32(ins>>2)&1<<5), 9))
}

func signExtend6(ins uint16) uint64 {
	imm := uint32(ins>>2)&0x1F | uint32(ins>>12)&1<<5
	return signExtend(uint64(imm), 6)
}

// 以下辅助生成规范 32 位编码,字段布局与 RISC-V 基本指令格式一致。
func encodeOpImm(funct3 uint32, rd, rs1 int, imm uint32) uint32 {
	return (imm&0xFFF)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x13
}

func encodeOpImmWord(funct3 uint32, rd, rs1 int, imm uint32) uint32 {
	return (imm&0xFFF)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x1B
}

func encodeLUI(rd int, imm uint32) uint32 {
	return imm&0xFFFFF000 | uint32(rd)<<7 | 0x37
}

func encodeOp(funct7, funct3 uint32, rd, rs1, rs2 int) uint32 {
	return funct7<<25 | uint32(rs2)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x33
}

func encodeOpWord(funct7, funct3 uint32, rd, rs1, rs2 int) uint32 {
	return funct7<<25 | uint32(rs2)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x3B
}

func encodeLoad(funct3 uint32, rd, rs1 int, offset uint32) uint32 {
	return (offset&0xFFF)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x03
}

// encodeFPLoad/encodeFPStore 生成 FLD/FSD 编码:rd 字段承载浮点寄存器
// 编号,布局与整数 LOAD/STORE 一致。
func encodeFPLoad(funct3 uint32, rd, rs1 int, offset uint32) uint32 {
	return (offset&0xFFF)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x07
}

func encodeFPStore(funct3 uint32, rs2, rs1 int, offset uint32) uint32 {
	return offset>>5&0x7F<<25 |
		uint32(rs2)<<20 |
		uint32(rs1)<<15 |
		funct3<<12 |
		offset&0x1F<<7 |
		0x27
}

func encodeStore(funct3 uint32, rs2, rs1 int, offset uint32) uint32 {
	return offset>>5&0x7F<<25 |
		uint32(rs2)<<20 |
		uint32(rs1)<<15 |
		funct3<<12 |
		offset&0x1F<<7 |
		0x23
}

func encodeJAL(rd int, offset int32) uint32 {
	imm := uint32(offset)
	return ((imm>>20)&0x1)<<31 |
		((imm>>1)&0x3FF)<<21 |
		((imm>>11)&0x1)<<20 |
		((imm>>12)&0xFF)<<12 |
		uint32(rd)<<7 | 0x6F
}

func encodeJALR(rd, rs1 int, offset int32) uint32 {
	return uint32(offset&0xFFF)<<20 | uint32(rs1)<<15 | uint32(rd)<<7 | 0x67
}

func encodeBranch(funct3 uint32, rs1, rs2 int, offset int32) uint32 {
	imm := uint32(offset)
	return ((imm>>12)&0x1)<<31 |
		((imm>>5)&0x3F)<<25 |
		uint32(rs2)<<20 |
		uint32(rs1)<<15 |
		funct3<<12 |
		((imm>>1)&0xF)<<8 |
		((imm>>11)&0x1)<<7 |
		0x63
}

const insEbreakEncoding = 0x00100073
