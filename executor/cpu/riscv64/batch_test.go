package riscv64

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

const (
	batchCodeBase = 0x1000
	batchDataBase = 0x2000
)

func encodeAMO(funct5, funct3 uint32, rd, rs1, rs2 int) uint32 {
	return funct5<<27 | uint32(rs2)<<20 | uint32(rs1)<<15 | funct3<<12 |
		uint32(rd)<<7 | 0x2F
}

// TestStepBatchMatchesSingleStep 对 C++ 整数批量路径与既有 Go 单步
// 语义做差分，覆盖整数 ALU、乘除、访存、分支和压缩指令。
func TestStepBatchMatchesSingleStep(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	cases := []struct {
		name  string
		code  uint32
		width int
		regs  map[int]uint64
	}{
		{"lui", encodeLUI(5, 0x12345), 4, nil},
		{"auipc", 0x00000297, 4, nil},
		{"addi", encodeOpImm(0x0, 5, 6, 7), 4, map[int]uint64{6: 9}},
		{"slti", encodeOpImm(0x2, 5, 6, 7), 4, map[int]uint64{6: 9}},
		{"sltiu", encodeOpImm(0x3, 5, 6, 7), 4, map[int]uint64{6: 9}},
		{"xori", encodeOpImm(0x4, 5, 6, 7), 4, map[int]uint64{6: 9}},
		{"ori", encodeOpImm(0x6, 5, 6, 7), 4, map[int]uint64{6: 9}},
		{"andi", encodeOpImm(0x7, 5, 6, 7), 4, map[int]uint64{6: 9}},
		{"slli", encodeOpImm(0x1, 5, 6, 5), 4, map[int]uint64{6: 3}},
		{"srli", encodeOpImm(0x5, 5, 6, 2), 4, map[int]uint64{6: 0x40}},
		{"srai", encodeOpImm(0x5, 5, 6, 0x10<<6|2), 4, map[int]uint64{6: ^uint64(0)}},
		{"addiw", encodeOpImmWord(0x0, 5, 6, 1), 4, map[int]uint64{6: 0x7FFFFFFF}},
		{"slliw", encodeOpImmWord(0x1, 5, 6, 2), 4, map[int]uint64{6: 3}},
		{"srliw", encodeOpImmWord(0x5, 5, 6, 1), 4, map[int]uint64{6: 0x80000000}},
		{"sraiw", encodeOpImmWord(0x5, 5, 6, 0x20<<5|1), 4, map[int]uint64{6: 0x80000000}},
		{"add", encodeOp(0x00, 0x0, 5, 6, 7), 4, map[int]uint64{6: 9, 7: 3}},
		{"sub", encodeOp(0x20, 0x0, 5, 6, 7), 4, map[int]uint64{6: 9, 7: 3}},
		{"sll", encodeOp(0x00, 0x1, 5, 6, 7), 4, map[int]uint64{6: 3, 7: 4}},
		{"slt", encodeOp(0x00, 0x2, 5, 6, 7), 4, map[int]uint64{6: 3, 7: 4}},
		{"sltu", encodeOp(0x00, 0x3, 5, 6, 7), 4, map[int]uint64{6: 3, 7: 4}},
		{"xor", encodeOp(0x00, 0x4, 5, 6, 7), 4, map[int]uint64{6: 0xF0, 7: 0x3C}},
		{"srl", encodeOp(0x00, 0x5, 5, 6, 7), 4, map[int]uint64{6: 0x40, 7: 2}},
		{"sra", encodeOp(0x20, 0x5, 5, 6, 7), 4, map[int]uint64{6: ^uint64(0), 7: 2}},
		{"or", encodeOp(0x00, 0x6, 5, 6, 7), 4, map[int]uint64{6: 0xF0, 7: 0x3C}},
		{"and", encodeOp(0x00, 0x7, 5, 6, 7), 4, map[int]uint64{6: 0xF0, 7: 0x3C}},
		{"mul", encodeOp(0x01, 0x0, 5, 6, 7), 4, map[int]uint64{6: 7, 7: 9}},
		{"mulh", encodeOp(0x01, 0x1, 5, 6, 7), 4, map[int]uint64{6: ^uint64(0), 7: 2}},
		{"mulhsu", encodeOp(0x01, 0x2, 5, 6, 7), 4, map[int]uint64{6: ^uint64(0), 7: 2}},
		{"mulhu", encodeOp(0x01, 0x3, 5, 6, 7), 4, map[int]uint64{6: ^uint64(0), 7: 2}},
		{"div", encodeOp(0x01, 0x4, 5, 6, 7), 4, map[int]uint64{6: ^uint64(0), 7: 2}},
		{"divu", encodeOp(0x01, 0x5, 5, 6, 7), 4, map[int]uint64{6: 7, 7: 2}},
		{"rem", encodeOp(0x01, 0x6, 5, 6, 7), 4, map[int]uint64{6: 7, 7: 2}},
		{"remu", encodeOp(0x01, 0x7, 5, 6, 7), 4, map[int]uint64{6: 7, 7: 2}},
		{"addw", encodeOpWord(0x00, 0x0, 5, 6, 7), 4, map[int]uint64{6: 0x7FFFFFFF, 7: 1}},
		{"subw", encodeOpWord(0x20, 0x0, 5, 6, 7), 4, map[int]uint64{6: 1, 7: 2}},
		{"sllw", encodeOpWord(0x00, 0x1, 5, 6, 7), 4, map[int]uint64{6: 0x40000000, 7: 1}},
		{"srlw", encodeOpWord(0x00, 0x5, 5, 6, 7), 4, map[int]uint64{6: 0x80000000, 7: 1}},
		{"sraw", encodeOpWord(0x20, 0x5, 5, 6, 7), 4, map[int]uint64{6: 0x80000000, 7: 1}},
		{"mulw", encodeOpWord(0x01, 0x0, 5, 6, 7), 4, map[int]uint64{6: 0x40000000, 7: 2}},
		{"divw", encodeOpWord(0x01, 0x4, 5, 6, 7), 4, map[int]uint64{6: 7, 7: 2}},
		{"divuw", encodeOpWord(0x01, 0x5, 5, 6, 7), 4, map[int]uint64{6: 7, 7: 2}},
		{"remw", encodeOpWord(0x01, 0x6, 5, 6, 7), 4, map[int]uint64{6: 7, 7: 2}},
		{"remuw", encodeOpWord(0x01, 0x7, 5, 6, 7), 4, map[int]uint64{6: 7, 7: 2}},
		{"lb", encodeLoad(0x0, 5, 2, 1), 4, map[int]uint64{2: batchDataBase}},
		{"lh", encodeLoad(0x1, 5, 2, 2), 4, map[int]uint64{2: batchDataBase}},
		{"lw", encodeLoad(0x2, 5, 2, 4), 4, map[int]uint64{2: batchDataBase}},
		{"ld", encodeLoad(0x3, 5, 2, 8), 4, map[int]uint64{2: batchDataBase}},
		{"lbu", encodeLoad(0x4, 5, 2, 1), 4, map[int]uint64{2: batchDataBase}},
		{"lhu", encodeLoad(0x5, 5, 2, 2), 4, map[int]uint64{2: batchDataBase}},
		{"lwu", encodeLoad(0x6, 5, 2, 4), 4, map[int]uint64{2: batchDataBase}},
		{"sb", encodeStore(0x0, 5, 2, 1), 4, map[int]uint64{2: batchDataBase, 5: 0xAB}},
		{"sh", encodeStore(0x1, 5, 2, 2), 4, map[int]uint64{2: batchDataBase, 5: 0xABCD}},
		{"sw", encodeStore(0x2, 5, 2, 4), 4, map[int]uint64{2: batchDataBase, 5: 0xABCDEF01}},
		{"sd", encodeStore(0x3, 5, 2, 8), 4, map[int]uint64{2: batchDataBase, 5: 0xABCDEF0123456789}},
		{"beq", encodeBranch(0x0, 6, 7, 8), 4, map[int]uint64{6: 3, 7: 3}},
		{"bne", encodeBranch(0x1, 6, 7, 8), 4, map[int]uint64{6: 3, 7: 4}},
		{"blt", encodeBranch(0x4, 6, 7, 8), 4, map[int]uint64{6: 3, 7: 4}},
		{"bge", encodeBranch(0x5, 6, 7, 8), 4, map[int]uint64{6: 4, 7: 3}},
		{"bltu", encodeBranch(0x6, 6, 7, 8), 4, map[int]uint64{6: 3, 7: 4}},
		{"bgeu", encodeBranch(0x7, 6, 7, 8), 4, map[int]uint64{6: 4, 7: 3}},
		{"jal", encodeJAL(1, 8), 4, nil},
		{"jalr", encodeJALR(1, 6, 4), 4, map[int]uint64{6: batchCodeBase + 0x20}},
		{"fence", 0x0000000F, 4, nil},
		{"c.li", uint32(cLI(5, -1)), 2, nil},
		{"c.addi", uint32(cADDI(5, 2)), 2, map[int]uint64{5: 3}},
		{"c.add", uint32(cCRADD(5, 6)), 2, map[int]uint64{5: 3, 6: 4}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			compareBatchAndStep(t, testCase.code, testCase.width, testCase.regs)
		})
	}
}

// TestStepBatchHandlesCrossPageInstruction 锁定页尾 32 位指令不能按
// 两字节压缩编码误译，必须回退到可跨页的单步取指。
func TestStepBatchHandlesCrossPageInstruction(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + 2*memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	code := make([]byte, 2*memory.PageSize)
	binary.LittleEndian.PutUint32(code[memory.PageSize-2:], encodeOpImm(0x0, 5, 5, 1))
	if err := table.Load(ctx, batchCodeBase, code); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	address := batchCodeBase + memory.PageSize - 2
	state := cpu.Machine{
		PC:        address,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[5] = 4
	batchCore, _ := New(cpu.Config{Memory: table, Entry: address})
	stepCore, _ := New(cpu.Config{Memory: clone, Entry: address})
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := batchCore.StepBatch(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := stepCore.Step(ctx); err != nil {
		t.Fatal(err)
	}
	batchState, _ := batchCore.State(ctx)
	stepState, _ := stepCore.State(ctx)
	if batchState.PC != stepState.PC || batchState.Registers[5] != stepState.Registers[5] {
		t.Fatalf("batch=%+v step=%+v", batchState, stepState)
	}
}

// TestStepBatchFollowsCachedCodePages 验证一次原生批量调用可以跨多个
// 已译码代码页继续执行，减少 cgo 边界次数。
func TestStepBatchFollowsCachedCodePages(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + 2*memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	code := make([]byte, 2*memory.PageSize)
	binary.LittleEndian.PutUint32(code, encodeJAL(0, int32(memory.PageSize)))
	const instructions = 8
	for index := range instructions {
		offset := int(memory.PageSize) + index*4
		binary.LittleEndian.PutUint32(code[offset:], encodeOpImm(0x0, 5, 5, 1))
	}
	if err := table.Load(ctx, batchCodeBase, code); err != nil {
		t.Fatal(err)
	}
	core, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase + memory.PageSize,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	if err := core.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	// 先预热目标页的译码缓存。
	if _, _, err := core.StepBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	state.PC = batchCodeBase
	state.Registers[5] = 0
	if err := core.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	steps, _, err := core.StepBatch(ctx, instructions+1)
	if err != nil {
		t.Fatal(err)
	}
	if steps != instructions+1 {
		t.Fatalf("steps=%d want %d", steps, instructions+1)
	}
	machine, _ := core.State(ctx)
	if machine.Registers[5] != instructions {
		t.Fatalf("x5=%d want %d", machine.Registers[5], instructions)
	}
}

// TestStepBatchJITLinksCrossPageLoop 覆盖热点 JIT 的跨页直接链接与
// JALR 目标缓存。循环每轮都跨越两个代码页，必须与 Go 单步语义一致。
func TestStepBatchJITLinksCrossPageLoop(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	const iterations = 1000
	target := batchCodeBase + memory.PageSize + 4
	tests := []struct {
		name string
		jump uint32
	}{
		{name: "jal", jump: encodeJAL(0, int32(memory.PageSize-4))},
		{name: "jalr", jump: encodeJALR(0, 7, 0)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			table := memory.New(0)
			if err := table.Map(ctx, memory.Region{
				Start: batchCodeBase, End: batchCodeBase + 2*memory.PageSize,
				Prot: memory.ProtRead | memory.ProtExec, Name: "code",
			}); err != nil {
				t.Fatal(err)
			}
			code := make([]byte, 2*memory.PageSize)
			binary.LittleEndian.PutUint32(code[4:], encodeOpImm(0x0, 5, 5, 1))
			binary.LittleEndian.PutUint32(code[8:], testCase.jump)
			binary.LittleEndian.PutUint32(code[memory.PageSize+4:], encodeBranch(0x4, 5, 6, -4096))
			if err := table.Load(ctx, batchCodeBase, code); err != nil {
				t.Fatal(err)
			}
			clone, err := table.Clone()
			if err != nil {
				t.Fatal(err)
			}
			state := cpu.Machine{
				PC:        batchCodeBase + 4,
				Registers: make([]uint64, numRegs),
				Floats:    make([]uint64, numFPRegs),
			}
			state.Registers[6] = iterations
			state.Registers[7] = target
			batchCore, err := New(cpu.Config{Memory: table, Entry: state.PC})
			if err != nil {
				t.Fatal(err)
			}
			stepCore, err := New(cpu.Config{Memory: clone, Entry: state.PC})
			if err != nil {
				t.Fatal(err)
			}
			if err := batchCore.Reset(ctx, state); err != nil {
				t.Fatal(err)
			}
			if err := stepCore.Reset(ctx, state); err != nil {
				t.Fatal(err)
			}
			const retired = iterations * 3
			var steps uint64
			for steps < retired {
				count, _, err := batchCore.StepBatch(ctx, retired-steps)
				if err != nil {
					t.Fatal(err)
				}
				if count == 0 {
					t.Fatal("批量执行没有推进")
				}
				steps += count
			}
			if steps != retired {
				t.Fatalf("steps=%d want %d", steps, retired)
			}
			for range retired {
				if _, err := stepCore.Step(ctx); err != nil {
					t.Fatal(err)
				}
			}
			batchState, err := batchCore.State(ctx)
			if err != nil {
				t.Fatal(err)
			}
			stepState, err := stepCore.State(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if batchState.PC != stepState.PC {
				t.Fatalf("PC batch=%#x step=%#x", batchState.PC, stepState.PC)
			}
			for index := range stepState.Registers {
				if batchState.Registers[index] != stepState.Registers[index] {
					t.Fatalf("x%d batch=%#x step=%#x", index,
						batchState.Registers[index], stepState.Registers[index])
				}
			}
		})
	}
}

func compareBatchAndStepFP(t *testing.T, instructions []uint32,
	registers map[int]uint64, floats map[int]uint64, fcsr uint32) {
	t.Helper()
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	for index, instruction := range instructions {
		binary.LittleEndian.PutUint32(program[index*4:], instruction)
	}
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
		FCSR:      fcsr,
	}
	for index, value := range registers {
		state.Registers[index] = value
	}
	for index, value := range floats {
		state.Floats[index] = value
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	steps, _, err := batchCore.StepBatch(ctx, uint64(len(instructions)))
	if err != nil {
		t.Fatal(err)
	}
	if steps != uint64(len(instructions)) {
		t.Fatalf("steps=%d want %d", steps, len(instructions))
	}
	for range instructions {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	batchState, err := batchCore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stepState, err := stepCore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if batchState.PC != stepState.PC || batchState.FCSR != stepState.FCSR {
		t.Fatalf("PC/FCSR batch=%#x/%#x step=%#x/%#x",
			batchState.PC, batchState.FCSR, stepState.PC, stepState.FCSR)
	}
	for index := range stepState.Registers {
		if batchState.Registers[index] != stepState.Registers[index] {
			t.Fatalf("x%d batch=%#x step=%#x", index,
				batchState.Registers[index], stepState.Registers[index])
		}
	}
	for index := range stepState.Floats {
		if batchState.Floats[index] != stepState.Floats[index] {
			t.Fatalf("f%d batch=%#x step=%#x", index,
				batchState.Floats[index], stepState.Floats[index])
		}
	}
}

// TestStepBatchFPRawMatchesSingleStep 覆盖 V8/Node 热路径使用的
// FSGNJ、FCVT、FMV、FMA 与 fcsr CSR 的批量语义。
func TestStepBatchFPRawMatchesSingleStep(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	oneAndHalf := math.Float64bits(1.5)
	negativeTwo := math.Float64bits(-2)
	tests := []struct {
		name   string
		code   uint32
		regs   map[int]uint64
		floats map[int]uint64
		fcsr   uint32
	}{
		{
			name:   "fsgnj.d",
			code:   encodeFP3(0x11, 0, 1, 2, 3),
			floats: map[int]uint64{2: oneAndHalf, 3: negativeTwo},
		},
		{
			name:   "fcvt.d.s",
			code:   encodeFPCvtInt(0x21, 0, 1, 2, 0),
			floats: map[int]uint64{2: f32bits(1.5)},
		},
		{
			name:   "fcvt.s.d",
			code:   encodeFPCvtInt(0x20, 0, 1, 2, 0),
			floats: map[int]uint64{2: oneAndHalf},
		},
		{
			name:   "fcvt.w.d",
			code:   encodeFPCvtInt(0x61, 1, 1, 2, 0),
			floats: map[int]uint64{2: math.Float64bits(3.75)},
		},
		{
			name:   "fcvt.d.w",
			code:   encodeFPCvtInt(0x71, 0, 1, 2, 0),
			regs:   map[int]uint64{2: 7},
			floats: nil,
		},
		{
			name:   "fmv.x.d",
			code:   encodeFP3(0x71, 0, 1, 2, 0),
			floats: map[int]uint64{2: oneAndHalf},
		},
		{
			name:   "fmv.d.x",
			code:   encodeFP3(0x79, 0, 1, 2, 0),
			regs:   map[int]uint64{2: math.Float64bits(2.5)},
			floats: nil,
		},
		{
			name:   "fscsr",
			code:   uint32(0x003<<20 | 2<<15 | 0x1<<12 | 1<<7 | 0x73),
			regs:   map[int]uint64{2: 0x2A},
			floats: nil,
			fcsr:   0x11,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			compareBatchAndStepFP(t, []uint32{testCase.code},
				testCase.regs, testCase.floats, testCase.fcsr)
		})
	}
}

// TestStepBatchJITFPRawLoop 确认热点 JIT 直接发射的 raw FP 路径与
// Go 单步语义一致，而不是只在 C++ 解释器中通过。
func TestStepBatchJITFPRawLoop(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[0:], encodeFP3(0x11, 0, 1, 2, 3))
	binary.LittleEndian.PutUint32(program[4:], encodeOpImm(0x0, 5, 5, 1))
	binary.LittleEndian.PutUint32(program[8:], encodeBranch(0x1, 5, 6, -8))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[6] = 1000
	state.Floats[2] = math.Float64bits(1.5)
	state.Floats[3] = math.Float64bits(-2)
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	const retired = 3000
	var steps uint64
	for steps < retired {
		count, _, err := batchCore.StepBatch(ctx, retired-steps)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatal("批量执行没有推进")
		}
		steps += count
	}
	for range retired {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	batchState, err := batchCore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stepState, err := stepCore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if batchState.PC != stepState.PC || batchState.FCSR != stepState.FCSR {
		t.Fatalf("PC/FCSR batch=%#x/%#x step=%#x/%#x",
			batchState.PC, batchState.FCSR, stepState.PC, stepState.FCSR)
	}
	for index := range stepState.Registers {
		if batchState.Registers[index] != stepState.Registers[index] {
			t.Fatalf("x%d batch=%#x step=%#x", index,
				batchState.Registers[index], stepState.Registers[index])
		}
	}
	for index := range stepState.Floats {
		if batchState.Floats[index] != stepState.Floats[index] {
			t.Fatalf("f%d batch=%#x step=%#x", index,
				batchState.Floats[index], stepState.Floats[index])
		}
	}
}

// TestStepBatchJITMatchesSingleStep 让原生翻译路径跨越多个基本块和
// 真实数据访存,再与逐指令语义基准比较完整机器状态。
func TestStepBatchJITMatchesSingleStep(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, memory.Region{
		Start: batchDataBase, End: batchDataBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtWrite, Name: "data",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[0:], encodeOpImm(0x0, 5, 5, 1))
	binary.LittleEndian.PutUint32(program[4:], encodeLoad(0x3, 6, 8, 0x148))
	binary.LittleEndian.PutUint32(program[8:], encodeStore(0x3, 5, 8, 0x148))
	binary.LittleEndian.PutUint32(program[12:], encodeBranch(0x1, 5, 7, -12))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[7] = 2500
	state.Registers[8] = batchDataBase
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	const instructions = 10000
	var retired uint64
	for retired < instructions {
		remaining := instructions - retired
		stepCount, _, err := batchCore.StepBatch(ctx, remaining)
		if err != nil {
			t.Fatal(err)
		}
		if stepCount == 0 {
			t.Fatal("批量执行没有推进")
		}
		retired += stepCount
	}
	if retired > instructions {
		t.Fatalf("批量执行越界: retired=%d want %d", retired, instructions)
	}
	for range instructions {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	batchState, err := batchCore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stepState, err := stepCore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if batchState.PC != stepState.PC {
		t.Fatalf("PC batch=%#x step=%#x", batchState.PC, stepState.PC)
	}
	for index := range stepState.Registers {
		if batchState.Registers[index] != stepState.Registers[index] {
			t.Fatalf("x%d batch=%#x step=%#x", index,
				batchState.Registers[index], stepState.Registers[index])
		}
	}
	batchData, err := readPage(ctx, table, batchDataBase)
	if err != nil {
		t.Fatal(err)
	}
	stepData, err := readPage(ctx, clone, batchDataBase)
	if err != nil {
		t.Fatal(err)
	}
	if string(batchData) != string(stepData) {
		t.Fatal("批量与单步写入的内存不同")
	}
}

// TestStepBatchJITReadsCounters 锁定 JIT/批量路径中的 cycle/time/instret
// 读取语义:值必须等于已退休指令数,与单步 CSR 实现一致。
func TestStepBatchJITReadsCounters(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[0:], encodeOpImm(0x0, 5, 0, 0))
	binary.LittleEndian.PutUint32(program[4:], encodeCSR(0x2, 6, 0, 0xC01))
	binary.LittleEndian.PutUint32(program[8:], encodeOp(0x00, 0x0, 5, 5, 6))
	binary.LittleEndian.PutUint32(program[12:], encodeOpImm(0x0, 7, 7, 1))
	binary.LittleEndian.PutUint32(program[16:], encodeBranch(0x4, 7, 8, -12))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[8] = 3000
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	const instructions = 1 + 3000*4
	var retired uint64
	for retired < instructions {
		remaining := instructions - retired
		stepCount, _, err := batchCore.StepBatch(ctx, remaining)
		if err != nil {
			t.Fatal(err)
		}
		if stepCount == 0 {
			t.Fatal("批量执行没有推进")
		}
		retired += stepCount
	}
	for range instructions {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	batchState, err := batchCore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stepState, err := stepCore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if batchState.PC != stepState.PC {
		t.Fatalf("PC batch=%#x step=%#x", batchState.PC, stepState.PC)
	}
	for index := range stepState.Registers {
		if batchState.Registers[index] != stepState.Registers[index] {
			t.Fatalf("x%d batch=%#x step=%#x", index,
				batchState.Registers[index], stepState.Registers[index])
		}
	}
}

// TestStepBatchJITIndirectBranches 锁定页内 JALR 的动态目标分派,
// 包括返回地址与循环回边。
func TestStepBatchJITIndirectBranches(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[0:], encodeOpImm(0x0, 5, 5, 1))
	binary.LittleEndian.PutUint32(program[4:], encodeJALR(1, 6, 0))
	binary.LittleEndian.PutUint32(program[8:], encodeBranch(0x1, 5, 8, -8))
	binary.LittleEndian.PutUint32(program[12:], encodeJALR(0, 9, -8))
	binary.LittleEndian.PutUint32(program[16:], encodeOpImm(0x0, 7, 7, 1))
	binary.LittleEndian.PutUint32(program[20:], encodeJALR(0, 1, 0))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[6] = batchCodeBase + 16
	state.Registers[8] = 1 << 20
	state.Registers[9] = batchCodeBase + 8
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	const instructions = 10000
	var retired uint64
	for retired < instructions {
		stepCount, _, err := batchCore.StepBatch(ctx, instructions-retired)
		if err != nil {
			t.Fatal(err)
		}
		if stepCount == 0 {
			t.Fatal("批量执行没有推进")
		}
		retired += stepCount
	}
	for range instructions {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	batchState, _ := batchCore.State(ctx)
	stepState, _ := stepCore.State(ctx)
	if batchState.PC != stepState.PC {
		t.Fatalf("PC batch=%#x step=%#x", batchState.PC, stepState.PC)
	}
	for index := range stepState.Registers {
		if batchState.Registers[index] != stepState.Registers[index] {
			t.Fatalf("x%d batch=%#x step=%#x", index,
				batchState.Registers[index], stepState.Registers[index])
		}
	}
}

// TestStepBatchJITFloatingPoint 覆盖 CPython 热路径使用的单精度
// 装入、整数转换、加减除、比较与存储，并保留完整浮点状态对拍。
func TestStepBatchJITFloatingPoint(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, memory.Region{
		Start: batchDataBase, End: batchDataBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtWrite, Name: "data",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[0:], encodeFPLoad(0x2, 0, 20, 0))
	binary.LittleEndian.PutUint32(program[4:], encodeFPLoad(0x2, 1, 20, 4))
	binary.LittleEndian.PutUint32(program[8:], encodeFP3(fpFADD, 0, 2, 0, 1))
	binary.LittleEndian.PutUint32(program[12:], encodeFP3(fpFDIV, 0, 3, 2, 0))
	binary.LittleEndian.PutUint32(program[16:], encodeFPCvtInt(fpFCVTS, 0, 4, 5, 3))
	binary.LittleEndian.PutUint32(program[20:], encodeFP3(0x50, 1, 6, 0, 1))
	binary.LittleEndian.PutUint32(program[24:], encodeFPStore(0x2, 3, 20, 8))
	binary.LittleEndian.PutUint32(program[28:], encodeFPLoad(0x3, 5, 20, 16))
	binary.LittleEndian.PutUint32(program[32:], encodeOpImm(0x0, 5, 5, 1))
	binary.LittleEndian.PutUint32(program[36:], encodeBranch(0x1, 5, 7, -36))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(data[0:], math.Float32bits(1.5))
	binary.LittleEndian.PutUint32(data[4:], math.Float32bits(2.25))
	binary.LittleEndian.PutUint64(data[16:], math.Float64bits(3.5))
	if err := table.Load(ctx, batchDataBase, data); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[7] = 300
	state.Registers[20] = batchDataBase
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	const instructions = 3000
	jitSteps, _, err := batchCore.StepBatch(ctx, 2048)
	if err != nil || jitSteps == 0 {
		t.Fatalf("JIT 浮点执行失败: steps=%d err=%v", jitSteps, err)
	}
	for range jitSteps {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	compareMachineState(t, batchCore, stepCore)
	retired := jitSteps
	for retired < instructions {
		stepCount, _, err := batchCore.StepBatch(ctx, instructions-retired)
		if err != nil {
			t.Fatal(err)
		}
		if stepCount == 0 {
			t.Fatal("批量执行没有推进")
		}
		retired += stepCount
	}
	for range instructions - jitSteps {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	compareMachineState(t, batchCore, stepCore)
}

// TestStepBatchJITDoubleFloatingPoint 覆盖 C++ 批量路径的双精度装入、
// 算术、比较与存储。Python/Node 热路径大量使用双精度运算。
func TestStepBatchJITDoubleFloatingPoint(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, memory.Region{
		Start: batchDataBase, End: batchDataBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtWrite, Name: "data",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[0:], encodeFPLoad(0x3, 0, 20, 0))
	binary.LittleEndian.PutUint32(program[4:], encodeFPLoad(0x3, 1, 20, 8))
	binary.LittleEndian.PutUint32(program[8:], encodeFP3(0x01, 0, 2, 0, 1))
	binary.LittleEndian.PutUint32(program[12:], encodeFP3(0x09, 0, 3, 2, 0))
	binary.LittleEndian.PutUint32(program[16:], encodeFP3(0x51, 0, 6, 2, 1))
	binary.LittleEndian.PutUint32(program[20:], encodeFPStore(0x3, 3, 20, 16))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint64(data[0:], math.Float64bits(1.5))
	binary.LittleEndian.PutUint64(data[8:], math.Float64bits(2.25))
	if err := table.Load(ctx, batchDataBase, data); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[20] = batchDataBase
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	steps, _, err := batchCore.StepBatch(ctx, 6)
	if err != nil || steps != 6 {
		t.Fatalf("双精度批量执行: steps=%d err=%v", steps, err)
	}
	for range steps {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	compareMachineState(t, batchCore, stepCore)
	batchData, err := readPage(ctx, table, batchDataBase)
	if err != nil {
		t.Fatal(err)
	}
	stepData, err := readPage(ctx, clone, batchDataBase)
	if err != nil {
		t.Fatal(err)
	}
	if string(batchData) != string(stepData) {
		t.Fatal("双精度批量与单步写入的内存不同")
	}
}

func compareMachineState(t *testing.T, batchCore, stepCore *Core) {
	t.Helper()
	ctx := context.Background()
	batchState, _ := batchCore.State(ctx)
	stepState, _ := stepCore.State(ctx)
	if batchState.PC != stepState.PC || batchState.FCSR != stepState.FCSR {
		t.Fatalf("PC/FCSR batch=%#x/%#x step=%#x/%#x", batchState.PC, batchState.FCSR,
			stepState.PC, stepState.FCSR)
	}
	for index := range stepState.Registers {
		if batchState.Registers[index] != stepState.Registers[index] {
			t.Fatalf("x%d batch=%#x step=%#x", index,
				batchState.Registers[index], stepState.Registers[index])
		}
	}
	for index := range stepState.Floats {
		if batchState.Floats[index] != stepState.Floats[index] {
			t.Fatalf("f%d batch=%#x step=%#x", index,
				batchState.Floats[index], stepState.Floats[index])
		}
	}
}

// TestStepBatchJITAtomics 覆盖 AMOADD.W 与 LR/SC,确保原子访存不会
// 在翻译路径中丢失保留集或返回旧值。
func TestStepBatchJITAtomics(t *testing.T) {
	if !hotBatchSupported {
		t.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, memory.Region{
		Start: batchDataBase, End: batchDataBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtWrite, Name: "data",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[0:], encodeOpImm(0x0, 5, 5, 1))
	binary.LittleEndian.PutUint32(program[4:], encodeAMO(0x00, 0x2, 6, 20, 5))
	binary.LittleEndian.PutUint32(program[8:], encodeAMO(0x02, 0x2, 7, 20, 0))
	binary.LittleEndian.PutUint32(program[12:], encodeAMO(0x03, 0x2, 8, 20, 5))
	binary.LittleEndian.PutUint32(program[16:], encodeOpImm(0x0, 9, 9, 1))
	binary.LittleEndian.PutUint32(program[20:], encodeBranch(0x1, 9, 10, -20))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[10] = 800
	state.Registers[20] = batchDataBase
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	const instructions = 4000
	var retired uint64
	for retired < instructions {
		stepCount, _, err := batchCore.StepBatch(ctx, instructions-retired)
		if err != nil {
			t.Fatal(err)
		}
		if stepCount == 0 {
			t.Fatal("批量执行没有推进")
		}
		retired += stepCount
	}
	for range instructions {
		if _, err := stepCore.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	compareMachineState(t, batchCore, stepCore)
	batchData, err := readPage(ctx, table, batchDataBase)
	if err != nil {
		t.Fatal(err)
	}
	stepData, err := readPage(ctx, clone, batchDataBase)
	if err != nil {
		t.Fatal(err)
	}
	if string(batchData) != string(stepData) {
		t.Fatal("原子批量与单步写入的内存不同")
	}
}

func compareBatchAndStep(t *testing.T, code uint32, width int, registers map[int]uint64) {
	t.Helper()
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, memory.Region{
		Start: batchDataBase, End: batchDataBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtWrite, Name: "data",
	}); err != nil {
		t.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	if width == 2 {
		binary.LittleEndian.PutUint16(program, uint16(code))
	} else {
		binary.LittleEndian.PutUint32(program, code)
	}
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		t.Fatal(err)
	}
	data := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	if err := table.Load(ctx, batchDataBase, data); err != nil {
		t.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		t.Fatal(err)
	}
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		t.Fatal(err)
	}
	state := cpu.Machine{PC: batchCodeBase, Registers: make([]uint64, numRegs), Floats: make([]uint64, numFPRegs)}
	for index, value := range registers {
		state.Registers[index] = value
	}
	if err := batchCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	_, batchOutcome, batchErr := batchCore.StepBatch(ctx, 1)
	stepOutcome, stepErr := stepCore.Step(ctx)
	if batchOutcome != stepOutcome || !sameError(batchErr, stepErr) {
		t.Fatalf("outcome batch=%v/%v step=%v/%v", batchOutcome, batchErr, stepOutcome, stepErr)
	}
	batchState, _ := batchCore.State(ctx)
	stepState, _ := stepCore.State(ctx)
	if batchState.PC != stepState.PC {
		t.Fatalf("PC batch=%#x step=%#x", batchState.PC, stepState.PC)
	}
	for index := range stepState.Registers {
		if batchState.Registers[index] != stepState.Registers[index] {
			t.Fatalf("x%d batch=%#x step=%#x", index, batchState.Registers[index], stepState.Registers[index])
		}
	}
	if width == 4 && (code&0x7F) == 0x23 {
		batchData, err := readPage(ctx, table, batchDataBase)
		if err != nil {
			t.Fatal(err)
		}
		stepData, err := readPage(ctx, clone, batchDataBase)
		if err != nil {
			t.Fatal(err)
		}
		if string(batchData) != string(stepData) {
			t.Fatal("批量与单步写入的内存不同")
		}
	}
}

func sameError(left, right error) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	var leftTrap, rightTrap cpu.Trap
	if errors.As(left, &leftTrap) && errors.As(right, &rightTrap) {
		return leftTrap.Kind == rightTrap.Kind && leftTrap.Address == rightTrap.Address
	}
	return left.Error() == right.Error()
}

func readPage(ctx context.Context, table *memory.Table, address uint64) ([]byte, error) {
	buffer := make([]byte, memory.PageSize)
	if err := table.Read(ctx, address, buffer); err != nil {
		return nil, err
	}
	return buffer, nil
}
