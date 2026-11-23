package riscv64

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

const (
	batchCodeBase = 0x1000
	batchDataBase = 0x2000
)

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
