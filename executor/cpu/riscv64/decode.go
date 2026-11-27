package riscv64

// decodedBatchInstruction 是批量执行使用的预译码指令。op 与
// hot_cgo.cpp 中的 Op 枚举一一对应;imm 已符号扩展或为移位量。
type decodedBatchInstruction struct {
	op     uint32
	imm    int32
	rd     uint8
	rs1    uint8
	rs2    uint8
	length uint8
}

const (
	batchOpUnsupported uint32 = iota
	batchOpLUI
	batchOpAUIPC
	batchOpJAL
	batchOpJALR
	batchOpBEQ
	batchOpBNE
	batchOpBLT
	batchOpBGE
	batchOpBLTU
	batchOpBGEU
	batchOpLB
	batchOpLH
	batchOpLW
	batchOpLD
	batchOpLBU
	batchOpLHU
	batchOpLWU
	batchOpSB
	batchOpSH
	batchOpSW
	batchOpSD
	batchOpADDI
	batchOpSLTI
	batchOpSLTIU
	batchOpXORI
	batchOpORI
	batchOpANDI
	batchOpSLLI
	batchOpSRLI
	batchOpSRAI
	batchOpADDIW
	batchOpSLLIW
	batchOpSRLIW
	batchOpSRAIW
	batchOpADD
	batchOpSUB
	batchOpSLL
	batchOpSLT
	batchOpSLTU
	batchOpXOR
	batchOpSRL
	batchOpSRA
	batchOpOR
	batchOpAND
	batchOpADDW
	batchOpSUBW
	batchOpSLLW
	batchOpSRLW
	batchOpSRAW
	batchOpMUL
	batchOpMULH
	batchOpMULHSU
	batchOpMULHU
	batchOpDIV
	batchOpDIVU
	batchOpREM
	batchOpREMU
	batchOpMULW
	batchOpDIVW
	batchOpDIVUW
	batchOpREMW
	batchOpREMUW
	batchOpLRSC
	batchOpAMO
	batchOpFLW
	batchOpFLD
	batchOpFSW
	batchOpFSD
	batchOpFADD
	batchOpFSUB
	batchOpFMUL
	batchOpFDIV
	batchOpFSQRT
	batchOpFCVTFromInt
	batchOpFEQ
	batchOpFLT
	batchOpFLE
	batchOpFENCE
	batchOpRDTIME
	batchOpFPRaw
	batchOpFPCSR
	batchOpCount
)

func signExtend32(value uint32, bits uint) int32 {
	shift := 32 - bits
	return int32(value<<shift) >> shift
}

func decodeBatchInstruction(instruction uint32, length uint8) decodedBatchInstruction {
	decoded := decodedBatchInstruction{
		op:     batchOpUnsupported,
		length: length,
		rd:     uint8(instruction >> 7 & 0x1F),
		rs1:    uint8(instruction >> 15 & 0x1F),
		rs2:    uint8(instruction >> 20 & 0x1F),
	}
	funct3 := instruction >> 12 & 0x7
	funct7 := instruction >> 25 & 0x7F
	opcode := instruction & 0x7F
	switch opcode {
	case 0x37: // LUI
		decoded.op = batchOpLUI
		decoded.imm = int32(instruction & 0xFFFFF000)
	case 0x17: // AUIPC
		decoded.op = batchOpAUIPC
		decoded.imm = int32(instruction & 0xFFFFF000)
	case 0x6F: // JAL
		immediate := (instruction>>31)<<20 |
			((instruction>>12)&0xFF)<<12 |
			((instruction>>20)&0x1)<<11 |
			((instruction>>21)&0x3FF)<<1
		decoded.op = batchOpJAL
		decoded.imm = signExtend32(immediate, 21)
	case 0x67: // JALR
		if funct3 != 0 {
			break
		}
		decoded.op = batchOpJALR
		decoded.imm = signExtend32(instruction>>20, 12)
	case 0x63: // branches
		immediate := (instruction>>31)<<12 |
			((instruction>>7)&0x1)<<11 |
			((instruction>>25)&0x3F)<<5 |
			((instruction>>8)&0xF)<<1
		decoded.imm = signExtend32(immediate, 13)
		switch funct3 {
		case 0x0:
			decoded.op = batchOpBEQ
		case 0x1:
			decoded.op = batchOpBNE
		case 0x4:
			decoded.op = batchOpBLT
		case 0x5:
			decoded.op = batchOpBGE
		case 0x6:
			decoded.op = batchOpBLTU
		case 0x7:
			decoded.op = batchOpBGEU
		}
	case 0x13: // OP-IMM
		decoded.imm = signExtend32(instruction>>20, 12)
		switch funct3 {
		case 0x0:
			decoded.op = batchOpADDI
		case 0x1:
			if instruction>>26 == 0 {
				decoded.op = batchOpSLLI
				decoded.imm = int32(instruction >> 20 & 0x3F)
			}
		case 0x2:
			decoded.op = batchOpSLTI
		case 0x3:
			decoded.op = batchOpSLTIU
		case 0x4:
			decoded.op = batchOpXORI
		case 0x5:
			switch instruction >> 26 {
			case 0:
				decoded.op = batchOpSRLI
				decoded.imm = int32(instruction >> 20 & 0x3F)
			case 0x10:
				decoded.op = batchOpSRAI
				decoded.imm = int32(instruction >> 20 & 0x3F)
			}
		case 0x6:
			decoded.op = batchOpORI
		case 0x7:
			decoded.op = batchOpANDI
		}
	case 0x03: // loads
		decoded.imm = signExtend32(instruction>>20, 12)
		switch funct3 {
		case 0x0:
			decoded.op = batchOpLB
		case 0x1:
			decoded.op = batchOpLH
		case 0x2:
			decoded.op = batchOpLW
		case 0x3:
			decoded.op = batchOpLD
		case 0x4:
			decoded.op = batchOpLBU
		case 0x5:
			decoded.op = batchOpLHU
		case 0x6:
			decoded.op = batchOpLWU
		}
	case 0x23: // stores
		decoded.imm = signExtend32((instruction>>25)<<5|(instruction>>7)&0x1F, 12)
		switch funct3 {
		case 0x0:
			decoded.op = batchOpSB
		case 0x1:
			decoded.op = batchOpSH
		case 0x2:
			decoded.op = batchOpSW
		case 0x3:
			decoded.op = batchOpSD
		}
	case 0x1B: // OP-IMM-32
		decoded.imm = signExtend32(instruction>>20, 12)
		switch funct3 {
		case 0x0:
			decoded.op = batchOpADDIW
		case 0x1:
			if instruction>>25 == 0 {
				decoded.op = batchOpSLLIW
				decoded.imm = int32(instruction >> 20 & 0x1F)
			}
		case 0x5:
			switch instruction >> 25 {
			case 0:
				decoded.op = batchOpSRLIW
				decoded.imm = int32(instruction >> 20 & 0x1F)
			case 0x20:
				decoded.op = batchOpSRAIW
				decoded.imm = int32(instruction >> 20 & 0x1F)
			}
		}
	case 0x33: // OP
		switch funct7 {
		case 0x00:
			switch funct3 {
			case 0x0:
				decoded.op = batchOpADD
			case 0x1:
				decoded.op = batchOpSLL
			case 0x2:
				decoded.op = batchOpSLT
			case 0x3:
				decoded.op = batchOpSLTU
			case 0x4:
				decoded.op = batchOpXOR
			case 0x5:
				decoded.op = batchOpSRL
			case 0x6:
				decoded.op = batchOpOR
			case 0x7:
				decoded.op = batchOpAND
			}
		case 0x20:
			switch funct3 {
			case 0x0:
				decoded.op = batchOpSUB
			case 0x5:
				decoded.op = batchOpSRA
			}
		case 0x01:
			switch funct3 {
			case 0x0:
				decoded.op = batchOpMUL
			case 0x1:
				decoded.op = batchOpMULH
			case 0x2:
				decoded.op = batchOpMULHSU
			case 0x3:
				decoded.op = batchOpMULHU
			case 0x4:
				decoded.op = batchOpDIV
			case 0x5:
				decoded.op = batchOpDIVU
			case 0x6:
				decoded.op = batchOpREM
			case 0x7:
				decoded.op = batchOpREMU
			}
		}
	case 0x3B: // OP-32
		switch funct7 {
		case 0x00:
			switch funct3 {
			case 0x0:
				decoded.op = batchOpADDW
			case 0x1:
				decoded.op = batchOpSLLW
			case 0x5:
				decoded.op = batchOpSRLW
			}
		case 0x20:
			switch funct3 {
			case 0x0:
				decoded.op = batchOpSUBW
			case 0x5:
				decoded.op = batchOpSRAW
			}
		case 0x01:
			switch funct3 {
			case 0x0:
				decoded.op = batchOpMULW
			case 0x4:
				decoded.op = batchOpDIVW
			case 0x5:
				decoded.op = batchOpDIVUW
			case 0x6:
				decoded.op = batchOpREMW
			case 0x7:
				decoded.op = batchOpREMUW
			}
		}
	case 0x2F: // AMO
		funct5 := instruction >> 27
		switch funct5 {
		case 0x02, 0x03:
			if funct3 == 0x2 || funct3 == 0x3 {
				decoded.op = batchOpLRSC
				width := int32(4)
				if funct3 == 0x3 {
					width = 8
				}
				decoded.imm = int32(funct5<<8) | width
			}
		case 0x00, 0x01, 0x04, 0x08, 0x0C, 0x10, 0x14, 0x18, 0x1C:
			if funct3 == 0x2 || funct3 == 0x3 {
				decoded.op = batchOpAMO
				width := int32(4)
				if funct3 == 0x3 {
					width = 8
				}
				decoded.imm = int32(funct5<<8) | width
			}
		}
	case 0x07: // FP loads
		decoded.imm = signExtend32(instruction>>20, 12)
		switch funct3 {
		case 0x2:
			decoded.op = batchOpFLW
		case 0x3:
			decoded.op = batchOpFLD
		}
	case 0x27: // FP stores
		decoded.imm = signExtend32((instruction>>25)<<5|(instruction>>7)&0x1F, 12)
		switch funct3 {
		case 0x2:
			decoded.op = batchOpFSW
		case 0x3:
			decoded.op = batchOpFSD
		}
	case 0x53: // OP-FP
		fpMode := int32((instruction >> 12) & 0x7)
		single := int32(1)
		if funct7&1 != 0 {
			single = 0
		}
		switch funct7 >> 1 {
		case 0x00:
			decoded.op = batchOpFADD
			decoded.imm = single | fpMode<<1
		case 0x02:
			decoded.op = batchOpFSUB
			decoded.imm = single | fpMode<<1
		case 0x04:
			decoded.op = batchOpFMUL
			decoded.imm = single | fpMode<<1
		case 0x06:
			decoded.op = batchOpFDIV
			decoded.imm = single | fpMode<<1
		case 0x16:
			if instruction>>20&0x1F == 0 {
				decoded.op = batchOpFSQRT
				decoded.imm = single | fpMode<<1
			}
		case 0x28:
			decoded.imm = int32(funct3) | single<<2
			switch funct3 {
			case 0x0:
				decoded.op = batchOpFLE
			case 0x1:
				decoded.op = batchOpFLT
			case 0x2:
				decoded.op = batchOpFEQ
			}
		case 0x34:
			decoded.imm = int32(instruction>>20&0x1F) | single<<3 | fpMode<<4
			decoded.op = batchOpFCVTFromInt
		}
		if decoded.op == batchOpUnsupported {
			decoded.op = batchOpFPRaw
			decoded.imm = int32(instruction)
		}
	case 0x43, 0x47, 0x4B, 0x4F: // 融合乘加
		decoded.op = batchOpFPRaw
		decoded.imm = int32(instruction)
	case 0x0F: // FENCE
		if funct3 == 0 {
			decoded.op = batchOpFENCE
		}
	case 0x73: // SYSTEM
		csr := instruction >> 20
		if csr == 0x001 || csr == 0x002 || csr == 0x003 {
			decoded.op = batchOpFPCSR
			decoded.imm = int32(instruction)
			break
		}
		if csr != 0xC00 && csr != 0xC01 && csr != 0xC02 {
			break
		}
		switch funct3 {
		case 0x2, 0x3:
			if (instruction>>15)&0x1F == 0 {
				decoded.op = batchOpRDTIME
			}
		case 0x6, 0x7:
			if (instruction>>15)&0x1F == 0 {
				decoded.op = batchOpRDTIME
			}
		}
	}
	return decoded
}
