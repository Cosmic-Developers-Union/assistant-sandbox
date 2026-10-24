package riscv64

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// C 指令编码构造器:逐位组装,与 expandCompressed 相互独立,用于交叉验证。
func cADDI4SPN(rd uint16, imm int) uint16 {
	v := uint16(imm)
	return ((v>>4)&3)<<11 | ((v>>6)&0xF)<<7 | ((v>>2)&1)<<6 | ((v>>3)&1)<<5 | (rd-8)<<2
}

func cLW(rd, rs1, off uint16) uint16 {
	imm := off
	return uint16(0x2)<<13 | ((imm>>3)&7)<<10 | (rs1-8)<<7 | ((imm>>6)&1)<<5 | ((imm>>2)&1)<<6 | (rd-8)<<2
}

func cLD(rd, rs1, off uint16) uint16 {
	imm := off
	return uint16(0x3)<<13 | ((imm>>3)&7)<<10 | (rs1-8)<<7 | ((imm>>6)&3)<<5 | (rd-8)<<2
}

func cSW(rs2, rs1, off uint16) uint16 {
	imm := off
	return uint16(0x6)<<13 | ((imm>>3)&7)<<10 | (rs1-8)<<7 | ((imm>>6)&1)<<5 | ((imm>>2)&1)<<6 | (rs2-8)<<2
}

func cSD(rs2, rs1, off uint16) uint16 {
	imm := off
	return uint16(0x7)<<13 | ((imm>>3)&7)<<10 | (rs1-8)<<7 | ((imm>>6)&3)<<5 | (rs2-8)<<2
}

func cCIImm(ins uint16, imm int) uint16 {
	imm6 := uint16(imm) & 0x3F
	return ins | (imm6>>5)<<12 | (imm6&0x1F)<<2
}

func cADDI(rd, imm int) uint16 { return cCIImm(uint16(rd)<<7|0x0<<13|0x1, imm) }

func cLI(rd, imm int) uint16 { return cCIImm(uint16(rd)<<7|0x2<<13|0x1, imm) }

func cADDIW(rd, imm int) uint16 { return cCIImm(uint16(rd)<<7|0x1<<13|0x1, imm) }

func cLUI(rd, imm int) uint16 {
	imm6 := uint16(imm>>12) & 0x3F
	return uint16(rd)<<7 | 0x3<<13 | (imm6>>5)<<12 | (imm6&0x1F)<<2 | 0x1
}

func cADDI16SP(imm int) uint16 {
	nz := uint16(imm>>4) & 0x3F
	return regSP<<7 | 0x3<<13 |
		(nz>>5)<<12 |
		(nz>>0&1)<<6 |
		(nz>>1&1)<<2 |
		(nz>>2&1)<<5 |
		(nz>>3&1)<<4 | 0x1
}

func cShift(op uint16, rd, shamt int) uint16 {
	return uint16(0x4)<<13 | uint16(shamt>>5)<<12 | op<<10 | uint16(rd-8)<<7 | uint16(shamt&0x1F)<<2 | 0x1
}

func cSRLI(rd, shamt int) uint16 { return cShift(0x0, rd, shamt) }

func cSRAI(rd, shamt int) uint16 { return cShift(0x1, rd, shamt) }

func cANDI(rd, imm int) uint16 { return cCIImm(uint16(rd-8)<<7|0x2<<10|0x4<<13|0x1, imm) }

func cOp(kind, rd, rs2 int) uint16 {
	// kind:0 SUB,1 XOR,2 OR,3 AND;4 SUBW,5 ADDW。
	wide := uint16(kind >> 2)
	sel := uint16(kind & 3)
	return wide<<12 | uint16(0x3)<<10 | sel<<5 | uint16(rd-8)<<7 | uint16(rs2-8)<<2 | 0x4<<13 | 0x1
}

func cJ(offset int) uint16 {
	imm := uint16(offset)
	return uint16(0x5)<<13 |
		((imm>>11)&1)<<12 | ((imm>>4)&1)<<11 | ((imm>>8)&3)<<9 | ((imm>>10)&1)<<8 |
		((imm>>6)&1)<<7 | ((imm>>7)&1)<<6 | ((imm>>1)&7)<<3 | ((imm>>5)&1)<<2 | 0x1
}

func cBranch(op uint16, rs1, offset int) uint16 {
	imm := uint16(offset)
	return op<<13 |
		((imm>>8)&1)<<12 | ((imm>>3)&3)<<10 | uint16(rs1-8)<<7 |
		((imm>>6)&3)<<5 | ((imm>>1)&3)<<3 | ((imm>>5)&1)<<2 | 0x1
}

func cSLLI(rd, shamt int) uint16 {
	return uint16(rd)<<7 | uint16(shamt>>5)<<12 | uint16(shamt&0x1F)<<2 | 0x0<<13 | 0x2
}

func cLWSP(rd, off int) uint16 {
	imm := uint16(off)
	return uint16(rd)<<7 | uint16(0x2)<<13 |
		((imm>>5)&1)<<12 | ((imm>>2)&7)<<4 | ((imm>>6)&3)<<2 | 0x2
}

func cLDSP(rd, off int) uint16 {
	imm := uint16(off)
	return uint16(rd)<<7 | uint16(0x3)<<13 |
		((imm>>5)&1)<<12 | ((imm>>3)&3)<<5 | ((imm>>6)&7)<<2 | 0x2
}

func cSWSP(rs2, off int) uint16 {
	imm := uint16(off)
	return uint16(rs2)<<2 | uint16(0x6)<<13 |
		((imm>>2)&0xF)<<9 | ((imm>>6)&3)<<7 | 0x2
}

func cSDSP(rs2, off int) uint16 {
	imm := uint16(off)
	return uint16(rs2)<<2 | uint16(0x7)<<13 |
		((imm>>3)&7)<<10 | ((imm>>6)&7)<<7 | 0x2
}

func cJR(rs1 int) uint16   { return uint16(rs1)<<7 | 0x4<<13 | 0x2 }
func cJALR(rs1 int) uint16 { return uint16(rs1)<<7 | 0x4<<13 | 0x1<<12 | 0x2 }
func cMV(rd, rs2 int) uint16 {
	return uint16(rd)<<7 | uint16(rs2)<<2 | 0x4<<13 | 0x2
}
func cCRADD(rd, rs2 int) uint16 {
	return uint16(rd)<<7 | uint16(rs2)<<2 | 0x4<<13 | 0x1<<12 | 0x2
}

func TestExpandCompressedAgainstCanonicalEncoding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    uint16
		status compressedStatus
		want   uint32
	}{
		{"C.ADDI4SPN", cADDI4SPN(12, 16), compressedOK, encodeOpImm(0x0, 12, regSP, 16)},
		{"C.LW", cLW(12, 10, 8), compressedOK, encodeLoad(0x2, 12, 10, 8)},
		{"C.LD", cLD(13, 10, 24), compressedOK, encodeLoad(0x3, 13, 10, 24)},
		{"C.SW", cSW(13, 10, 8), compressedOK, encodeStore(0x2, 13, 10, 8)},
		{"C.SD", cSD(14, 10, 24), compressedOK, encodeStore(0x3, 14, 10, 24)},
		{"C.ADDI", cADDI(11, -1), compressedOK, encodeOpImm(0x0, 11, 11, 0xFFF)},
		{"C.NOP", cADDI(0, 0), compressedOK, encodeOpImm(0x0, 0, 0, 0)},
		{"C.ADDIW", cADDIW(11, 2), compressedOK, encodeOpImmWord(0x0, 11, 2)},
		{"C.LI 负数", cLI(15, -2), compressedOK, encodeOpImm(0x0, 15, regZero, 0xFFE)},
		{"C.LUI", cLUI(10, 0x1000), compressedOK, encodeLUI(10, 0x1000)},
		{"C.ADDI16SP", cADDI16SP(16), compressedOK, encodeOpImm(0x0, regSP, regSP, 16)},
		{"C.ADDI16SP 负数", cADDI16SP(-32), compressedOK, encodeOpImm(0x0, regSP, regSP, 0xFE0)},
		{"C.SRLI", cSRLI(10, 1), compressedOK, encodeOpImm(0x5, 10, 10, 1)},
		{"C.SRLI 高位", cSRLI(10, 33), compressedOK, encodeOpImm(0x5, 10, 10, 33)},
		{"C.SRAI", cSRAI(10, 1), compressedOK, encodeOpImm(0x5, 10, 10, 0x10<<6|1)},
		{"C.ANDI", cANDI(11, -3), compressedOK, encodeOpImm(0x7, 11, 11, 0xFFD)},
		{"C.SUB", cOp(0, 12, 12), compressedOK, encodeOp(0x20, 0x0, 12, 12, 12)},
		{"C.XOR", cOp(1, 12, 12), compressedOK, encodeOp(0x00, 0x4, 12, 12, 12)},
		{"C.OR", cOp(2, 12, 12), compressedOK, encodeOp(0x00, 0x6, 12, 12, 12)},
		{"C.AND", cOp(3, 12, 12), compressedOK, encodeOp(0x00, 0x7, 12, 12, 12)},
		{"C.SUBW", cOp(4, 12, 12), compressedOK, encodeOpWord(0x20, 0x0, 12, 12, 12)},
		{"C.ADDW", cOp(5, 12, 12), compressedOK, encodeOpWord(0x00, 0x0, 12, 12, 12)},
		{"C.J 正跳", cJ(4), compressedOK, encodeJAL(regZero, 4)},
		{"C.J 反跳", cJ(-10), compressedOK, encodeJAL(regZero, -10)},
		{"C.BEQZ", cBranch(6, 10, 12), compressedOK, encodeBranch(0x0, 10, regZero, 12)},
		{"C.BNEZ", cBranch(7, 10, 3), compressedOK, encodeBranch(0x1, 10, regZero, 3)},
		{"C.SLLI", cSLLI(12, 1), compressedOK, encodeOpImm(0x1, 12, 12, 1)},
		{"C.LWSP", cLWSP(10, 4), compressedOK, encodeLoad(0x2, 10, regSP, 4)},
		{"C.LDSP", cLDSP(10, 8), compressedOK, encodeLoad(0x3, 10, regSP, 8)},
		{"C.SWSP", cSWSP(13, 12), compressedOK, encodeStore(0x2, 13, regSP, 12)},
		{"C.SDSP", cSDSP(10, 8), compressedOK, encodeStore(0x3, 10, regSP, 8)},
		{"C.JR", cJR(10), compressedOK, encodeJALR(regZero, 10, 0)},
		{"C.JALR", cJALR(10), compressedOK, encodeJALR(regRA, 10, 0)},
		{"C.MV", cMV(10, 11), compressedOK, encodeOp(0x00, 0x0, 10, regZero, 11)},
		{"C.ADD", cCRADD(10, 11), compressedOK, encodeOp(0x00, 0x0, 10, 10, 11)},
		{"C.EBREAK", 0x9002, compressedOK, insEbreakEncoding},
		{"C.FLD 依赖 F", 0x20c4, compressedFloat, 0},
		{"C.FSDSP 依赖 F", 0xa823, compressedFloat, 0},
		{"全零编码", 0x0000, compressedReserved, 0},
		{"C.ADDI4SPN 零偏移", 0x0008, compressedReserved, 0},
		{"C.LUI rd=x0", 0x6001, compressedReserved, 0},
		{"C.JR rs1=x0", 0x8002, compressedReserved, 0},
		{"保留 W 变体", 0x9cd1, compressedReserved, 0},
	} {
		expanded, status := expandCompressed(tc.raw)
		if status != tc.status {
			t.Errorf("%s(%#04x): status=%v want %v", tc.name, tc.raw, status, tc.status)
			continue
		}
		if status == compressedOK && expanded != tc.want {
			t.Errorf("%s(%#04x): 展开 %#x want %#x", tc.name, tc.raw, expanded, tc.want)
		}
	}
}

func writeCompressed(t *testing.T, memory *sparse, program []uint16) {
	t.Helper()
	raw := make([]byte, 0, len(program)*2)
	for _, instruction := range program {
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], instruction)
		raw = append(raw, encoded[:]...)
	}
	if err := memory.Write(context.Background(), 0, raw); err != nil {
		t.Fatal(err)
	}
}

func TestMixedLengthFetchAndBranch(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	writeCompressed(t, memory, []uint16{
		cLI(10, 5),        // 0x00
		cLI(11, 0),        // 0x02
		cBranch(7, 10, 6), // 0x04: c.bnez a0,+6 → 0x0A
		cLI(11, 99),       // 0x06: 被跳过
		cLI(11, 88),       // 0x08: 被跳过
		cLI(11, 1),        // 0x0A
		cMV(12, 10),       // 0x0C: a2 = a0
		cCRADD(12, 11),    // 0x0E: a2 += a1
	})
	port := newPort(t, memory, 0)
	for step := 0; step < 6; step++ {
		outcome, err := port.Step(ctx)
		if err != nil || outcome != cpu.Continued {
			t.Fatalf("第 %d 步: outcome=%v err=%v", step, outcome, err)
		}
	}
	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.PC != 0x10 {
		t.Fatalf("PC=%#x want 0x10", state.PC)
	}
	if got := state.Registers[10]; got != 5 {
		t.Fatalf("a0=%d want 5", got)
	}
	if got := state.Registers[11]; got != 1 {
		t.Fatalf("a1=%d want 1(分支必须跳过 0x06/0x08)", got)
	}
	if got := state.Registers[12]; got != 6 {
		t.Fatalf("a2=%d want 6", got)
	}
}

func TestCompressedLoadStoreAndStackRoundTrip(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(regSP, 0x2000); err != nil {
		t.Fatal(err)
	}
	writeCompressed(t, memory, []uint16{
		cLI(10, 7),
		cSWSP(10, 4),
		cLI(11, 0),
		cLWSP(11, 4),
		cSDSP(11, 8),
		cLDSP(12, 8),
	})
	for step := 0; step < 6; step++ {
		outcome, err := port.Step(ctx)
		if err != nil || outcome != cpu.Continued {
			t.Fatalf("第 %d 步: outcome=%v err=%v", step, outcome, err)
		}
	}
	if got, err := port.ReadRegister(12); err != nil || got != 7 {
		t.Fatalf("a2=%#x err=%v want 7", got, err)
	}
	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.PC != 12 {
		t.Fatalf("PC=%#x want 12(六条 2 字节指令)", state.PC)
	}
}

func TestCompressedJumpRegistersExecute(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(10, 8); err != nil {
		t.Fatal(err)
	}
	writeCompressed(t, memory, []uint16{
		cJR(10),     // 0x00: 跳到 0x08
		cLI(11, 99), // 0x02: 被跳过
		cLI(11, 0),  // 0x04: 被跳过
		0x9002,      // 0x08: c.ebreak
	})
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("c.jr: outcome=%v err=%v", outcome, err)
	}
	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.PC != 8 {
		t.Fatalf("C.JR 应跳到 0x8, PC=%#x", state.PC)
	}
	if _, err := port.Step(ctx); !errors.As(err, new(cpu.Trap)) {
		t.Fatalf("应到达 c.ebreak: %v", err)
	}
}

// execSparse 在 sparse 之上叠加执行权限位,用于取指权限测试。
type execSparse struct {
	*sparse
	exec map[uint64]uint64
}

func (s *execSparse) Executable(_ context.Context, address, length uint64) error {
	end := address + length
	for base, limit := range s.exec {
		if address >= base && end <= base+limit {
			return nil
		}
	}
	return errno.Permission
}

func TestFetchRequiresExecutablePage(t *testing.T) {
	ctx := context.Background()
	memory := &execSparse{sparse: newSparse(), exec: map[uint64]uint64{0: 10}}
	// 0x00: c.li a0,1(2 字节,在可执行区间内);0x02: 4 字节 nop(0x13)跨出 8 字节边界外的部分;
	// 0x08: c.jalr 跳到 a1=0x100(不可执行页)。
	if err := memory.Write(ctx, 0, []byte{0x01, 0x05}); err != nil { // c.li a0, 1
		t.Fatal(err)
	}
	var nop [4]byte
	binary.LittleEndian.PutUint32(nop[:], encodeOpImm(0x0, 0, 0, 0))
	if err := memory.Write(ctx, 2, nop[:]); err != nil {
		t.Fatal(err)
	}
	if err := memory.Write(ctx, 6, []byte{0x01, 0x05}); err != nil { // c.addi a0, 1
		t.Fatal(err)
	}
	if err := memory.Write(ctx, 8, []byte{0x82, 0x85}); err != nil { // c.jr a1 @0x08
		t.Fatal(err)
	}
	port, err := New(cpu.Config{Memory: memory, Entry: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := port.WriteRegister(11, 0x100); err != nil {
		t.Fatal(err)
	}

	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("可执行页内取指应成功: outcome=%v err=%v", outcome, err)
	}
	// 4 字节 nop 起始 0x02,长度 4 → 覆盖 [2,6) ⊂ [0,8) 可执行。
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("页内 4 字节取指应成功: outcome=%v err=%v", outcome, err)
	}
	// 0x06: c.addi a0, 1,推进到 0x08 的 c.jr。
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("可执行页内压缩取指应成功: outcome=%v err=%v", outcome, err)
	}
	// c.jr 本身可执行,跳转后的取指才落在不可执行页。
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("c.jr 应成功跳转: outcome=%v err=%v", outcome, err)
	}
	var trap cpu.Trap
	_, stepErr := port.Step(ctx)
	if !errors.As(stepErr, &trap) || trap.Kind != cpu.TrapMemoryFault {
		t.Fatalf("不可执行取指应产生访存故障: %v", err)
	}
	if !errors.Is(stepErr, errno.Permission) {
		t.Fatalf("故障应携带 EACCES 身份: %v", stepErr)
	}
	if trap.Detail != 0x100 {
		t.Fatalf("故障应携带取指地址 0x100: %+v", trap)
	}
}
