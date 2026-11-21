package riscv64

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
)

const (
	fpFADD  = 0x00
	fpFSUB  = 0x04
	fpFMUL  = 0x08
	fpFDIV  = 0x0C
	fpFSQRT = 0x2C
	fpFCVTW = 0x60
	fpFCVTS = 0x68
)

func encodeFP3(funct7 uint32, rm uint32, rd, rs1, rs2 int) uint32 {
	return funct7<<25 | uint32(rs2)<<20 | uint32(rs1)<<15 | rm<<12 | uint32(rd)<<7 | 0x53
}

func encodeFMA(opcode uint32, rm uint32, rd, rs1, rs2, rs3 int) uint32 {
	return uint32(rs3)<<27 | uint32(rs2)<<20 | uint32(rs1)<<15 | rm<<12 | uint32(rd)<<7 | opcode
}

func encodeFPCvtInt(funct7 uint32, rm uint32, rd, rs1, rs2 int) uint32 {
	return funct7<<25 | uint32(rs2)<<20 | uint32(rs1)<<15 | rm<<12 | uint32(rd)<<7 | 0x53
}

func f32bits(f float32) uint64 { return uint64(math.Float32bits(f)) }

func runFP(t *testing.T, setup func(*Core), instructions ...uint32) *Core {
	t.Helper()
	memory := newSparse()
	raw := make([]byte, 0, len(instructions)*4)
	for _, instruction := range instructions {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], instruction)
		raw = append(raw, encoded[:]...)
	}
	if err := memory.Write(context.Background(), 0, raw); err != nil {
		t.Fatal(err)
	}
	port := newPort(t, memory, 0)
	if setup != nil {
		setup(port)
	}
	for index := 0; index < len(instructions); index++ {
		outcome, err := port.Step(context.Background())
		if err != nil || outcome != cpu.Continued {
			t.Fatalf("第 %d 条指令: outcome=%v err=%v", index, outcome, err)
		}
	}
	return port
}

func TestFloatArithmeticSingle(t *testing.T) {
	// 1.5 + 2.25 = 3.75;1.5 × 2.25 = 3.375。
	write := func(port *Core) {
		if err := port.WriteRegister(10, f32bits(1.5)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteRegister(11, f32bits(2.25)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(0, f32bits(1.5)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, f32bits(2.25)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write, encodeFP3(fpFADD, 0, 2, 0, 1))
	if got, err := port.ReadFP(2); err != nil || math.Float32frombits(uint32(got)) != 3.75 {
		t.Fatalf("fadd.s = %v err=%v want 3.75", math.Float32frombits(uint32(got)), err)
	}
	// NaN 装箱:结果的高 32 位必须全 1。
	if got, _ := port.ReadFP(2); got>>32 != 0xFFFFFFFF {
		t.Fatalf("S 结果必须装箱: %#x", got)
	}
}

func TestFloatMultiplyAndDivide(t *testing.T) {
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(1.5)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, f32bits(2.25)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write, encodeFP3(fpFMUL, 0, 2, 0, 1), encodeFP3(fpFDIV, 0, 3, 1, 0))
	if got, err := port.ReadFP(2); err != nil || math.Float32frombits(uint32(got)) != 3.375 {
		t.Fatalf("fmul.s = %v want 3.375", math.Float32frombits(uint32(got)))
	}
	if got, err := port.ReadFP(3); err != nil || math.Float32frombits(uint32(got)) != 1.5 {
		t.Fatalf("fdiv.s = %v want 1.5", math.Float32frombits(uint32(got)))
	}
}

func TestFloatDivisionByZeroSetsDZ(t *testing.T) {
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(1)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, f32bits(0)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write, encodeFP3(fpFDIV, 0, 2, 0, 1))
	got, err := port.ReadFP(2)
	if err != nil || math.Float32frombits(uint32(got)) != float32(math.Inf(1)) {
		t.Fatalf("1/0 = %v want +Inf", math.Float32frombits(uint32(got)))
	}
	fcsr, err := port.ReadFCSR()
	if err != nil || fcsr&flagDZ == 0 {
		t.Fatalf("除零应置 DZ 标志: fcsr=%#x err=%v", fcsr, err)
	}
}

func TestFloatSqrtNegativeSetsNV(t *testing.T) {
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(-4)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write, encodeFP3(fpFSQRT, 0, 2, 0, 0))
	got, _ := port.ReadFP(2)
	if !math.IsNaN(float64(math.Float32frombits(uint32(got)))) {
		t.Fatalf("sqrt(-4) 应为 NaN: %v", math.Float32frombits(uint32(got)))
	}
	fcsr, _ := port.ReadFCSR()
	if fcsr&flagNV == 0 {
		t.Fatalf("sqrt 负数应置 NV: fcsr=%#x", fcsr)
	}
}

func TestFloatDoublePrecision(t *testing.T) {
	// D 指令:funct7 bit0=1。
	const faddD = 0x01
	write := func(port *Core) {
		if err := port.WriteFP(0, math.Float64bits(1.5)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, math.Float64bits(2.25)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write, encodeFP3(faddD, 0, 2, 0, 1))
	got, err := port.ReadFP(2)
	if err != nil || math.Float64frombits(got) != 3.75 {
		t.Fatalf("fadd.d = %v err=%v want 3.75", math.Float64frombits(got), err)
	}
}

func TestFusedMultiplyAddSingleRounding(t *testing.T) {
	// a=1.5, b=2.25, c=1:FMADD.S = 1.5*2.25+1 = 4.375,单次舍入。
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(1.5)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, f32bits(2.25)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(2, f32bits(1)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write, encodeFMA(0x43, 0, 3, 0, 1, 2))
	got, err := port.ReadFP(3)
	if err != nil || math.Float32frombits(uint32(got)) != 4.375 {
		t.Fatalf("fmadd.s = %v err=%v want 4.375", math.Float32frombits(uint32(got)), err)
	}
}

func TestFloatConvertToIntWithRounding(t *testing.T) {
	// FCVT.W.S RTZ:2.7 → 2,-2.7 → -2;RNE 舍入 2.5 → 2(向偶数)。
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(2.7)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, f32bits(-2.7)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(2, f32bits(2.5)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(3, f32bits(3.5)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write,
		encodeFPCvtInt(fpFCVTW, 0x1, 10, 0, 0), // rtz
		encodeFPCvtInt(fpFCVTW, 0x1, 11, 1, 0),
		encodeFPCvtInt(fpFCVTW, 0x0, 12, 2, 0), // rne
		encodeFPCvtInt(fpFCVTW, 0x0, 13, 3, 0),
	)
	for index, want := range map[int]uint64{10: 2, 11: ^uint64(0) - 1, 12: 2, 13: 4} {
		got, err := port.ReadRegister(index)
		if err != nil || got != want {
			t.Fatalf("x%d = %#x err=%v want %#x", index, got, err, want)
		}
	}
}

func TestFloatConvertToIntClampsOutOfRange(t *testing.T) {
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(3e9)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, f32bits(float32(math.NaN()))); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write,
		encodeFPCvtInt(fpFCVTW, 0x1, 10, 0, 0),
		encodeFPCvtInt(fpFCVTW, 0x1, 11, 1, 0),
	)
	got, err := port.ReadRegister(10)
	if err != nil || got != 0x7FFFFFFF {
		t.Fatalf("越界转换应钳制到 int32 最大值: %#x err=%v", got, err)
	}
	if got, _ := port.ReadRegister(11); got != 0x7FFFFFFF {
		t.Fatalf("NaN 转换应得到最大正数: %#x", got)
	}
	fcsr, _ := port.ReadFCSR()
	if fcsr&flagNV == 0 {
		t.Fatalf("越界/NaN 转换应置 NV: fcsr=%#x", fcsr)
	}
}

func TestFloatConvertFromInt(t *testing.T) {
	write := func(port *Core) {
		if err := port.WriteRegister(10, 0x40000000); err != nil { // 2^30
			t.Fatal(err)
		}
	}
	// FCVT.S.L:64 位源;funct7=0x68,rs2=2 选择 L。
	port := runFP(t, write, encodeFPCvtInt(0x68, 0x0, 0, 10, 2))
	got, err := port.ReadFP(0)
	if err != nil || math.Float32frombits(uint32(got)) != 1073741824 {
		t.Fatalf("fcvt.s.l = %v err=%v", math.Float32frombits(uint32(got)), err)
	}
}

func TestFloatLoadStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	if err := memory.Write(ctx, 0x400, []byte{0, 0, 0xC0, 0x3F}); err != nil { // 1.5
		t.Fatal(err)
	}
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(10, 0x400); err != nil {
		t.Fatal(err)
	}
	program := []uint32{
		encodeFPLoad(0x2, 0, 10, 0),         // flw f0, 0(a0)
		encodeFP3(fpFADD, 0, 1, 0, 0),       // f1 = 3.0
		0x00B00000 | 0x2<<20 | 0<<15 | 0x27, // 错误占位,下面用正确编码
	}
	// FSW f1, 8(a0):S 型 imm=8。
	fsw := uint32(8>>5)<<25 | uint32(1)<<20 | uint32(10)<<15 | 0x2<<12 | uint32(8&0x1F)<<7 | 0x27
	program[2] = fsw
	raw := make([]byte, 0, len(program)*4)
	for _, instruction := range program {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], instruction)
		raw = append(raw, encoded[:]...)
	}
	if err := memory.Write(ctx, 0, raw); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < len(program); index++ {
		if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
			t.Fatalf("第 %d 条: outcome=%v err=%v", index, outcome, err)
		}
	}
	var got [4]byte
	if err := memory.Read(ctx, 0x408, got[:]); err != nil {
		t.Fatal(err)
	}
	if math.Float32frombits(binary.LittleEndian.Uint32(got[:])) != 3 {
		t.Fatalf("fsw 存储的值 = %v want 3", math.Float32frombits(binary.LittleEndian.Uint32(got[:])))
	}
}

func TestFloatMinMaxAndSignInjection(t *testing.T) {
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(1)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, f32bits(-2)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write,
		encodeFP3(0x14, 0x0, 2, 0, 1), // fmin.s f2 = -2
		encodeFP3(0x14, 0x1, 3, 0, 1), // fmax.s f3 = 1
		encodeFP3(0x10, 0x1, 4, 0, 1), // fsgnjn.s f4 = -(f1 符号取反) = +1
	)
	if got, err := port.ReadFP(2); err != nil || math.Float32frombits(uint32(got)) != -2 {
		t.Fatalf("fmin = %v want -2", math.Float32frombits(uint32(got)))
	}
	if got, err := port.ReadFP(3); err != nil || math.Float32frombits(uint32(got)) != 1 {
		t.Fatalf("fmax = %v want 1", math.Float32frombits(uint32(got)))
	}
	if got, err := port.ReadFP(4); err != nil || math.Float32frombits(uint32(got)) != 1 {
		t.Fatalf("fsgnjn(f0=1, f1=-2) = %v want +1", math.Float32frombits(uint32(got)))
	}
}

func TestFloatClassifyAndCompare(t *testing.T) {
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(float32(math.Inf(-1)))); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(1, f32bits(1)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(2, f32bits(float32(math.NaN()))); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write,
		encodeFP3(0x70, 0x0, 10, 0, 1), // fclass.s f0(rs2=1 选择 FCLASS) = -Inf → bit0
		encodeFP3(0x50, 0x1, 11, 1, 0), // flt.s f1 < f0? false
		encodeFP3(0x50, 0x2, 12, 1, 1), // feq.s f1 == f1 → 1
	)
	if got, err := port.ReadRegister(10); err != nil || got != 1 {
		t.Fatalf("fclass(-Inf) = %#x want bit0", got)
	}
	if got, err := port.ReadRegister(11); err != nil || got != 0 {
		t.Fatalf("flt(1, -Inf) = %d want 0", got)
	}
	if got, err := port.ReadRegister(12); err != nil || got != 1 {
		t.Fatalf("feq(1, 1) = %d want 1", got)
	}
}

func TestFloatConvertWidthAndFlagsAccumulate(t *testing.T) {
	// FCVT.D.S 精确扩展;FCVT.S.D 舍回。
	write := func(port *Core) {
		if err := port.WriteFP(0, f32bits(1.5)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, write,
		encodeFP3(0x21, 0, 1, 0, 0), // fcvt.d.s f1 = 1.5(double)
		encodeFP3(0x20, 0, 2, 1, 0), // fcvt.s.d f2 = 1.5
	)
	got, err := port.ReadFP(1)
	if err != nil || math.Float64frombits(got) != 1.5 {
		t.Fatalf("fcvt.d.s = %v err=%v", math.Float64frombits(got), err)
	}
	if got, _ := port.ReadFP(2); got>>32 != 0xFFFFFFFF {
		t.Fatalf("fcvt.s.d 结果应装箱: %#x", got)
	}
}

func TestFCSRReadWriteViaCSR(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	// csrrw a0, fcsr, x0(读旧值);csrrwi a1, frm, 2。
	program := []uint32{
		encodeCSR(0x1, 10, 0, int(csrFCSR)),
		encodeCSR(0x5, 11, 2, int(csrFrm)),
	}
	raw := make([]byte, 0, len(program)*4)
	for _, instruction := range program {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], instruction)
		raw = append(raw, encoded[:]...)
	}
	if err := memory.Write(ctx, 0, raw); err != nil {
		t.Fatal(err)
	}
	port := newPort(t, memory, 0)
	for index := 0; index < len(program); index++ {
		if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
			t.Fatalf("第 %d 条: outcome=%v err=%v", index, outcome, err)
		}
	}
	// csrrwi 的 rd 得到旧值(0),新值写入 frm。
	if got, err := port.ReadRegister(11); err != nil || got != 0 {
		t.Fatalf("csrrwi 应返回旧 frm: %d err=%v", got, err)
	}
	fcsr, _ := port.ReadFCSR()
	if fcsr>>5&0x7 != 2 {
		t.Fatalf("fcsr.frm = %d want 2", fcsr>>5&0x7)
	}
}

func TestFloatStateSurvivesReset(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	port := newPort(t, memory, 0)
	if err := port.WriteFP(3, math.Float64bits(2.5)); err != nil {
		t.Fatal(err)
	}
	if err := port.WriteFCSR(0x20); err != nil {
		t.Fatal(err)
	}
	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := port.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	got, err := port.ReadFP(3)
	if err != nil || math.Float64frombits(got) != 2.5 {
		t.Fatalf("Reset 后浮点寄存器应保留: %v err=%v", math.Float64frombits(got), err)
	}
	fcsr, _ := port.ReadFCSR()
	if fcsr != 0x20 {
		t.Fatalf("Reset 后 FCSR 应保留: %#x", fcsr)
	}
}

func TestFloatTrapsCarryInstructionEncoding(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	var encoded [4]byte
	binary.LittleEndian.PutUint32(encoded[:], encodeFP3(0x7F, 0, 0, 0, 0))
	if err := memory.Write(ctx, 0, encoded[:]); err != nil {
		t.Fatal(err)
	}
	port := newPort(t, memory, 0)
	_, err := port.Step(ctx)
	var trap cpu.Trap
	if !errors.As(err, &trap) || trap.Kind != cpu.TrapIllegalInstruction {
		t.Fatalf("保留 funct7 应非法: %v", err)
	}
	if trap.Instruction != encodeFP3(0x7F, 0, 0, 0, 0) {
		t.Fatalf("trap 应携带指令编码: %#x", trap.Instruction)
	}
}

// TestFloatFusedMultiplyAddDouble 覆盖 FMADD/FMSUB/FNMSUB/FNMADD.D 的
// 操作种类(不能把 fmt 位当作运算选择)与单次舍入结果。
func TestFloatFusedMultiplyAddDouble(t *testing.T) {
	const (
		a = 18034063.0
		b = 67108864.0
		c = 38198625.0
	)
	setup := func(port *Core) {
		for register, value := range map[int]float64{1: a, 2: b, 4: c, 5: 1.0 / 9007199254740992.0} {
			if err := port.WriteFP(register, math.Float64bits(value)); err != nil {
				t.Fatal(err)
			}
		}
	}
	const fmtDouble = 1 << 25
	for _, tc := range []struct {
		name   string
		opcode uint32
		want   uint64
	}{
		{"fmadd", 0x43 | fmtDouble, math.Float64bits(a*b + c)},
		{"fmsub", 0x47 | fmtDouble, math.Float64bits(a*b - c)},
		{"fnmsub", 0x4B | fmtDouble, math.Float64bits(-a*b + c)},
		{"fnmadd", 0x4F | fmtDouble, math.Float64bits(-a*b - c)},
	} {
		port := runFP(t, setup, encodeFMA(tc.opcode, 0, 3, 1, 2, 4))
		got, err := port.ReadFP(3)
		if err != nil || got != tc.want {
			t.Fatalf("%s.d = %#x err=%v want %#x", tc.name, got, err, tc.want)
		}
	}

	// 乘法后接乘法:复现 Python random() 的 53 位组合路径。
	port := runFP(t, setup,
		encodeFMA(0x43|fmtDouble, 0, 1, 1, 2, 4),
		encodeFP3(fpFMUL|1, 0, 1, 1, 5))
	got, err := port.ReadFP(1)
	if want := math.Float64bits((a*b + c) * (1.0 / 9007199254740992.0)); err != nil || got != want {
		t.Fatalf("fmadd+fmul.d = %#x err=%v want %#x", got, err, want)
	}
}

// TestFloatArithmeticSigns 验证负操作数的加减乘除符号传播。
func TestFloatArithmeticSigns(t *testing.T) {
	setup := func(port *Core) {
		if err := port.WriteFP(1, math.Float64bits(-1.5)); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(2, math.Float64bits(2.25)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		code uint32
		want float64
	}{
		{"fadd", fpFADD | 1, 0.75},
		{"fmul", fpFMUL | 1, -3.375},
		{"fdiv", fpFDIV | 1, -1.5 / 2.25},
	} {
		port := runFP(t, setup, encodeFP3(tc.code, 0, 3, 1, 2))
		got, err := port.ReadFP(3)
		if err != nil || math.Float64frombits(got) != tc.want {
			t.Fatalf("%s.d = %v err=%v want %v", tc.name, math.Float64frombits(got), err, tc.want)
		}
	}
}

// TestFloatMultiplyKeepsExponentFloor 是 log2Floor 的回归用例:
// 大数乘 2^-53 曾把指数从 -3 误判为 -2,使 Python random() 结果偏移。
func TestFloatMultiplyKeepsExponentFloor(t *testing.T) {
	setup := func(port *Core) {
		if err := port.WriteFP(1, 0x431132d8f91b7584); err != nil {
			t.Fatal(err)
		}
		if err := port.WriteFP(2, math.Float64bits(1.0/9007199254740992.0)); err != nil {
			t.Fatal(err)
		}
	}
	port := runFP(t, setup, encodeFP3(fpFMUL|1, 0, 3, 1, 2))
	got, err := port.ReadFP(3)
	if want := uint64(0x3fc132d8f91b7584); err != nil || got != want {
		t.Fatalf("fmul.d = %#x err=%v want %#x", got, err, want)
	}
}

// TestFloatConvertZeroToInt 是 fpToInt 的回归用例:±0 的 fpValue
// 不保留有理数,转换必须直接返回 0 而不是解引用 nil。
func TestFloatConvertZeroToInt(t *testing.T) {
	setup := func(port *Core) {
		if err := port.WriteFP(1, math.Float64bits(0)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		funct7 uint32
		rs2    int
	}{
		{"fcvt.w.d", 0x61, 0},
		{"fcvt.wu.d", 0x61, 1},
		{"fcvt.l.d", 0x61, 2},
		{"fcvt.lu.d", 0x61, 3},
	} {
		port := runFP(t, setup, encodeFPCvtInt(tc.funct7, 0, 3, 1, tc.rs2))
		got, err := port.ReadRegister(3)
		if err != nil || got != 0 {
			t.Fatalf("%s 0.0 = %#x err=%v want 0", tc.name, got, err)
		}
	}
}
