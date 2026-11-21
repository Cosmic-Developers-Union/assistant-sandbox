package riscv64

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

type sparse struct {
	pages    map[uint64][]byte
	pageSize uint64

	faults int
}

func newSparse() *sparse { return &sparse{pages: map[uint64][]byte{}, pageSize: 4096} }

func (s *sparse) page(address uint64) []byte {
	base := address / s.pageSize * s.pageSize
	page, ok := s.pages[base]
	if !ok {
		page = make([]byte, s.pageSize)
		s.pages[base] = page
	}
	return page
}

func (s *sparse) Read(_ context.Context, address uint64, buffer []byte) error {
	if len(buffer) == 0 {
		return nil
	}

	if address/s.pageSize != (address+uint64(len(buffer))-1)/s.pageSize {
		s.faults++
		return errno.Range
	}
	page := s.page(address)
	offset := address % s.pageSize
	copy(buffer, page[offset:offset+uint64(len(buffer))])
	return nil
}

func (s *sparse) Write(_ context.Context, address uint64, buffer []byte) error {
	if len(buffer) == 0 {
		return nil
	}
	if address/s.pageSize != (address+uint64(len(buffer))-1)/s.pageSize {
		s.faults++
		return errno.Range
	}
	page := s.page(address)
	offset := address % s.pageSize
	copy(page[offset:offset+uint64(len(buffer))], buffer)
	return nil
}

func (s *sparse) load(t *testing.T, base uint64, instructions ...uint32) {
	t.Helper()
	for index, instruction := range instructions {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], instruction)
		if err := s.Write(context.Background(), base+uint64(index)*4, encoded[:]); err != nil {
			t.Fatal(err)
		}
	}
}

type counter struct {
	charged uint64
	limit   uint64
}

func (c *counter) Charge(instructions uint64) error {
	if c.limit > 0 && c.charged+instructions > c.limit {
		return errno.Wrap(errno.Limit, "指令预算耗尽")
	}
	c.charged += instructions
	return nil
}

type canceller struct{ cancelled bool }

func (c *canceller) Cancelled() bool { return c.cancelled }

const (
	insAddi5 = 0x00500093

	insAddi7 = 0x00700113

	insAdd = 0x002081B3

	insSub = 0x40110233

	insStoreWord = 0x00302023

	insLoadWord = 0x00002283

	insBranchTaken = 0x00000463

	insBranchNotTaken = 0x00001463

	insJump = 0x0080006F

	insEbreak = 0x00100073

	insEcall = 0x00000073

	insLui = 0x12345337

	insFence = 0x0FF0000F

	insFenceI = 0x0000100F

	insCSR = 0x00001073

	insIllegalLoad = 0x00007283
)

func newPort(t *testing.T, memory *sparse, base uint64) *Core {
	t.Helper()
	port, err := New(cpu.Config{Memory: memory, Entry: base})
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func run(t *testing.T, port *Core, count int) []cpu.Outcome {
	t.Helper()
	outcomes := make([]cpu.Outcome, 0, count)
	for index := 0; index < count; index++ {
		outcome, err := port.Step(context.Background())
		if err != nil {
			t.Fatalf("第 %d 步失败: %v", index, err)
		}
		outcomes = append(outcomes, outcome)
		if outcome != cpu.Continued {
			break
		}
	}
	return outcomes
}

func TestFixedInstructionVectorsArithmetic(t *testing.T) {
	memory := newSparse()
	memory.load(t, 0, insAddi5, insAddi7, insAdd, insSub)
	port := newPort(t, memory, 0)
	run(t, port, 4)

	for index, want := range map[int]uint64{1: 5, 2: 7, 3: 12, 4: 2} {
		got, err := port.ReadRegister(index)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("x%d=%d want %d", index, got, want)
		}
	}

	state, err := port.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Registers[0] != 0 {
		t.Fatalf("x0=%d 必须恒为 0", state.Registers[0])
	}

	if state.PC != 16 {
		t.Fatalf("PC=%d want 16", state.PC)
	}
}

func TestLoadStoreRoundTrip(t *testing.T) {
	memory := newSparse()
	memory.load(t, 0, insAddi5, insAddi7, insAdd, insStoreWord, insLoadWord)
	port := newPort(t, memory, 0)
	run(t, port, 5)
	got, err := port.ReadRegister(5)
	if err != nil {
		t.Fatal(err)
	}
	if got != 12 {
		t.Fatalf("x5=%d want 12", got)
	}

	var raw [4]byte
	if err := memory.Read(context.Background(), 0, raw[:]); err != nil {
		t.Fatal(err)
	}
	if value := binary.LittleEndian.Uint32(raw[:]); value != 12 {
		t.Fatalf("内存=%d want 12", value)
	}
}

func TestBranchesAndJumps(t *testing.T) {

	memory := newSparse()
	memory.load(t, 0, insAddi5, insBranchTaken, insAddi7, insAddi7)
	port := newPort(t, memory, 0)

	run(t, port, 2)
	state, err := port.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if state.PC != 12 {
		t.Fatalf("分支后 PC=%d want 12", state.PC)
	}
	if state.Registers[2] != 0 {
		t.Fatalf("被跳过的 addi 不应执行: x2=%d", state.Registers[2])
	}

	memory2 := newSparse()
	memory2.load(t, 0, insBranchNotTaken, insAddi5)
	port2 := newPort(t, memory2, 0)
	run(t, port2, 2)
	state2, err := port2.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state2.Registers[1] != 5 {
		t.Fatalf("未跳转时下一条应执行: x1=%d", state2.Registers[1])
	}

	memory3 := newSparse()
	memory3.load(t, 0, insJump, insAddi5, insAddi7)
	port3 := newPort(t, memory3, 0)

	run(t, port3, 1)
	state3, err := port3.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state3.PC != 8 || state3.Registers[1] != 0 {
		t.Fatalf("JAL 后 PC=%d x1=%d", state3.PC, state3.Registers[1])
	}
}

func TestTrapKindsAreDistinguished(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		label       string
		instruction uint32
	}{
		{"fence.i", insFenceI},
		{"csrrw", insCSR},
	} {
		memory := newSparse()
		memory.load(t, 0, tc.instruction)
		port := newPort(t, memory, 0)
		_, err := port.Step(ctx)
		var trap cpu.Trap
		if !errors.As(err, &trap) {
			t.Fatalf("%s: 应产生 trap: %v", tc.label, err)
		}
		if trap.Kind != cpu.TrapNotImplemented {
			t.Errorf("%s: 类别=%v want not-implemented", tc.label, trap.Kind)
		}
	}

	memory := newSparse()
	memory.load(t, 0, insIllegalLoad)
	port := newPort(t, memory, 0)
	_, err := port.Step(ctx)
	var trap cpu.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("非法加载应产生 trap: %v", err)
	}
	if trap.Kind != cpu.TrapIllegalInstruction {
		t.Fatalf("类别=%v want illegal-instruction", trap.Kind)
	}
	if trap.Instruction != insIllegalLoad || trap.Address != 0 {
		t.Fatalf("trap 应带原始指令与地址: %+v", trap)
	}

	memory2 := newSparse()
	memory2.load(t, 0, insFence, insAddi5)
	port2 := newPort(t, memory2, 0)
	if _, err := port2.Step(ctx); err != nil {
		t.Fatalf("fence 应为空操作: %v", err)
	}
}

func TestEcallIsReportedNotTranslated(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	memory.load(t, 0, insEcall)
	port := newPort(t, memory, 0)

	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Registers[17] = 93
	state.Registers[10] = 0
	if err := port.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	_, err = port.Step(ctx)
	var trap cpu.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("ECALL 应产生 trap: %v", err)
	}
	if trap.Kind != cpu.TrapEcall {
		t.Fatalf("类别=%v want ecall", trap.Kind)
	}

	if trap.Detail>>32 != 93 {
		t.Fatalf("Detail 应携带 a7=93: %+v", trap)
	}

	memory2 := newSparse()
	memory2.load(t, 0, insEbreak)
	port2 := newPort(t, memory2, 0)
	_, err = port2.Step(ctx)
	if !errors.As(err, &trap) || trap.Kind != cpu.TrapBreakpoint {
		t.Fatalf("EBREAK 类别=%v want breakpoint", err)
	}
}

func TestMemoryFaultIsTrapped(t *testing.T) {
	ctx := context.Background()

	memory := newSparse()
	memory.load(t, 0, 0x0000A283)
	port := newPort(t, memory, 0)
	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}

	state.Registers[1] = 4094
	if err := port.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	_, err = port.Step(ctx)
	var trap cpu.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("越界访存应产生 trap: %v", err)
	}
	if trap.Kind != cpu.TrapMemoryFault {
		t.Fatalf("类别=%v want memory-fault", trap.Kind)
	}
	if trap.Detail != 4094 {
		t.Fatalf("trap 应带访存地址: %+v", trap)
	}

	if !errors.Is(err, errno.Range) {
		t.Fatalf("trap 应保留底层错误身份: %v", err)
	}

	// 页尾放入 4 字节指令的低 16 位,两段取指跨页时必须报访存故障。
	if err := memory.Write(ctx, 4094, []byte{0xFF, 0xFF}); err != nil {
		t.Fatal(err)
	}
	port2 := newPort(t, memory, 4094)
	if _, err := port2.Step(ctx); !errors.As(err, &trap) || trap.Kind != cpu.TrapMemoryFault {
		t.Fatalf("取指跨页应产生访存 trap: %v", err)
	}
}

func TestCancellationAtInstructionBoundary(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	memory.load(t, 0, insAddi5, insAddi7)
	stop := &canceller{}
	port, err := New(cpu.Config{Memory: memory, Entry: 0, Canceller: stop})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := port.Step(ctx); err != nil {
		t.Fatal(err)
	}
	stop.cancelled = true
	_, err = port.Step(ctx)
	if !errors.Is(err, errno.Cancelled) {
		t.Fatalf("取消后应报 ECANCELED: %v", err)
	}

	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Registers[2] != 0 || state.PC != 4 {
		t.Fatalf("取消后状态不应前进: PC=%d x2=%d", state.PC, state.Registers[2])
	}
}

func TestMeterChargesPerInstruction(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	memory.load(t, 0, insAddi5, insAddi7, insAdd, insSub)
	meter := &counter{limit: 3}
	port, err := New(cpu.Config{Memory: memory, Entry: 0, Meter: meter})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if _, err := port.Step(ctx); err != nil {
			t.Fatalf("第 %d 步应成功: %v", index, err)
		}
	}
	if meter.charged != 3 {
		t.Fatalf("计量=%d want 3", meter.charged)
	}
	_, err = port.Step(ctx)
	if !errors.Is(err, errno.Limit) {
		t.Fatalf("预算耗尽应报 EAGAIN: %v", err)
	}

	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Registers[4] != 0 {
		t.Fatalf("被拒绝的指令不应执行: x4=%d", state.Registers[4])
	}
}

func TestArchitectureIsDeterministic(t *testing.T) {
	program := []uint32{insAddi5, insAddi7, insAdd, insStoreWord, insLoadWord, insLui, insFence}
	runOnce := func() ([numRegs]uint64, uint64) {
		memory := newSparse()
		memory.load(t, 0, program...)
		port := newPort(t, memory, 0)
		run(t, port, len(program))
		state, err := port.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var registers [numRegs]uint64
		copy(registers[:], state.Registers)
		return registers, state.PC
	}
	firstRegisters, firstPC := runOnce()
	secondRegisters, secondPC := runOnce()
	if firstRegisters != secondRegisters || firstPC != secondPC {
		t.Fatal("同一程序两次执行结果不同")
	}

	if firstRegisters[6] != 0x12345000 {
		t.Fatalf("x6=%#x want 0x12345000", firstRegisters[6])
	}
}

func TestConstructionAndStateValidation(t *testing.T) {
	if _, err := New(cpu.Config{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺内存应报 EINVAL: %v", err)
	}
	memory := newSparse()
	port := newPort(t, memory, 0)

	if _, err := port.ReadRegister(32); !errors.Is(err, errno.Invalid) {
		t.Fatalf("越界寄存器应报 EINVAL: %v", err)
	}
	if _, err := port.ReadRegister(-1); !errors.Is(err, errno.Invalid) {
		t.Fatalf("负寄存器应报 EINVAL: %v", err)
	}

	word := port.WordSize()
	if word.Bits != 64 || word.Bytes != 8 || !word.LittleEndian {
		t.Fatalf("字长=%+v", word)
	}

	state, err := port.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.Registers[1] = 999
	again, err := port.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again.Registers[1] == 999 {
		t.Fatal("State 必须返回副本")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := port.Reset(cancelled, cpu.Machine{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := port.State(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestResetKeepsFullWidth(t *testing.T) {
	memory := newSparse()
	port := newPort(t, memory, 0)
	err := port.Reset(context.Background(), cpu.Machine{
		PC:        0x1000,
		Registers: []uint64{0xDEADBEEF, 0xFFFFFFFF_FFFFFFFF, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := port.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.PC != 0x1000 {
		t.Fatalf("PC=%#x", state.PC)
	}
	if state.Registers[0] != 0 {
		t.Fatalf("x0=%d 必须为 0", state.Registers[0])
	}
	if state.Registers[1] != 0xFFFFFFFF_FFFFFFFF {
		t.Fatalf("x1=%#x 必须原样保留 64 位", state.Registers[1])
	}
}

func TestSignExtendBoundaries(t *testing.T) {
	for _, tc := range []struct {
		value uint64
		width uint
		want  uint64
	}{
		{0x000, 12, 0},
		{0x7FF, 12, 2047},
		{0x800, 12, 0xFFFFFFFFFFFFF800},
		{0xFFF, 12, 0xFFFFFFFFFFFFFFFF},
		{0x1FFE, 13, 0xFFFFFFFFFFFFFFFE},
		{0x0FFE, 13, 0x0FFE},
		{0x1FFFFF, 21, 0xFFFFFFFFFFFFFFFF},
		{0x100000, 21, 0xFFFFFFFFFFF00000},
	} {
		if got := signExtend(tc.value, tc.width); got != tc.want {
			t.Errorf("signExtend(%#x,%d)=%d want %d", tc.value, tc.width, got, tc.want)
		}
	}
}

const (
	insXori  = 0x00F0C193
	insOri   = 0x0300E193
	insAndi  = 0x00F0F193
	insSlti  = 0x0050A193
	insSltiu = 0x0050B193
	insSlli  = 0x00409193
	insSrli  = 0x0020D193
	insSrai  = 0x4020D193
	insSll   = 0x002091B3
	insSlt   = 0x0020A1B3
	insSltu  = 0x0020B1B3
	insXor   = 0x0020C1B3
	insSrl   = 0x0020D1B3
	insSra   = 0x4020D1B3
	insOr    = 0x0020E1B3
	insAnd   = 0x0020F1B3
	insLb    = 0x00008183
	insLh    = 0x00009183
	insLbu   = 0x0000C183
	insLhu   = 0x0000D183
	insSb    = 0x00208023
	insSh    = 0x00209023
	insJalr  = 0x000081E7
	insAuipc = 0x01000217
	insBlt   = 0x0020C463
	insBge   = 0x0020D463
	insBltu  = 0x0020E463
	insBgeu  = 0x0020F463

	insIllegalShift = 0x40109193

	insIllegalJalr = 0x000091E7

	insIllegalSystem = 0x00200073
)

func executeOne(t *testing.T, instruction uint32, setup func(*cpu.Machine)) (cpu.Machine, error) {
	t.Helper()
	memory := newSparse()
	memory.load(t, 0, instruction)
	port := newPort(t, memory, 0)
	ctx := context.Background()
	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(&state)
	}
	if err := port.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	_, stepErr := port.Step(ctx)
	after, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return after, stepErr
}

func TestOpImmVectors(t *testing.T) {
	setup := func(state *cpu.Machine) { state.Registers[1] = 0xFF }
	for _, tc := range []struct {
		label       string
		instruction uint32
		want        uint64
	}{
		{"xori", insXori, 0xF0},
		{"ori", insOri, 0xFF},
		{"andi", insAndi, 0x0F},
		{"slti 255<5 假", insSlti, 0},
		{"sltiu 255<5 假", insSltiu, 0},
		{"slli 255<<4", insSlli, 0xFF0},
		{"srli 255>>2", insSrli, 0x3F},
		{"srai 255>>2", insSrai, 0x3F},
	} {
		state, err := executeOne(t, tc.instruction, setup)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if state.Registers[3] != tc.want {
			t.Errorf("%s: x3=%#x want %#x", tc.label, state.Registers[3], tc.want)
		}
	}

	negative := func(state *cpu.Machine) { state.Registers[1] = 0xFFFFFFFFFFFFFFF0 }
	arithmetic, err := executeOne(t, insSrai, negative)
	if err != nil {
		t.Fatal(err)
	}
	if arithmetic.Registers[3] != 0xFFFFFFFFFFFFFFFC {
		t.Errorf("srai 负数: x3=%#x want 0xFFFFFFFFFFFFFFFC", arithmetic.Registers[3])
	}
	logical, err := executeOne(t, insSrli, negative)
	if err != nil {
		t.Fatal(err)
	}
	if logical.Registers[3] != 0x3FFFFFFFFFFFFFFC {
		t.Errorf("srli 负数: x3=%#x want 0x3FFFFFFFFFFFFFFC", logical.Registers[3])
	}

	minusOne := func(state *cpu.Machine) { state.Registers[1] = 0xFFFFFFFFFFFFFFFF }
	signed, err := executeOne(t, insSlti, minusOne)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Registers[3] != 1 {
		t.Errorf("slti -1<5 应为真: x3=%d", signed.Registers[3])
	}
	unsigned, err := executeOne(t, insSltiu, minusOne)
	if err != nil {
		t.Fatal(err)
	}
	if unsigned.Registers[3] != 0 {
		t.Errorf("sltiu 0xFFFFFFFFFFFFFFFF<5 应为假: x3=%d", unsigned.Registers[3])
	}
}

func TestRegisterRegisterVectors(t *testing.T) {

	setup := func(state *cpu.Machine) {
		state.Registers[1] = 0xF0
		state.Registers[2] = 3
	}
	for _, tc := range []struct {
		label       string
		instruction uint32
		want        uint64
	}{
		{"sll", insSll, 0x780},
		{"slt 240<3 假", insSlt, 0},
		{"sltu 240<3 假", insSltu, 0},
		{"xor", insXor, 0xF3},
		{"srl", insSrl, 0x1E},
		{"sra", insSra, 0x1E},
		{"or", insOr, 0xF3},
		{"and", insAnd, 0},
	} {
		state, err := executeOne(t, tc.instruction, setup)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if state.Registers[3] != tc.want {
			t.Errorf("%s: x3=%#x want %#x", tc.label, state.Registers[3], tc.want)
		}
	}

	less := func(state *cpu.Machine) {
		state.Registers[1] = 3
		state.Registers[2] = 240
	}
	for _, tc := range []struct {
		label       string
		instruction uint32
	}{
		{"slt 3<240", insSlt},
		{"sltu 3<240", insSltu},
	} {
		state, err := executeOne(t, tc.instruction, less)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if state.Registers[3] != 1 {
			t.Errorf("%s: x3=%d want 1", tc.label, state.Registers[3])
		}
	}
}

func TestLoadStoreWidthsAndSignExtension(t *testing.T) {
	ctx := context.Background()

	memory := newSparse()
	memory.load(t, 0,
		0x08000113,
		insSb,
		insLb,
	)
	port := newPort(t, memory, 0)
	state, err := port.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Registers[1] = 0x1000
	if err := port.Reset(ctx, state); err != nil {
		t.Fatal(err)
	}
	run(t, port, 3)

	signed, err := port.ReadRegister(3)
	if err != nil {
		t.Fatal(err)
	}
	if signed != 0xFFFFFFFFFFFFFF80 {
		t.Fatalf("lb: x3=%#x want 0xFFFFFFFFFFFFFF80", signed)
	}

	memory2 := newSparse()
	memory2.load(t, 0,
		0x08000113,
		insSb,
		insLbu,
	)
	port2 := newPort(t, memory2, 0)
	state2, err := port2.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state2.Registers[1] = 0x1000
	if err := port2.Reset(ctx, state2); err != nil {
		t.Fatal(err)
	}
	run(t, port2, 3)
	unsigned, err := port2.ReadRegister(3)
	if err != nil {
		t.Fatal(err)
	}
	if unsigned != 0x80 {
		t.Fatalf("lbu: x3=%#x want 0x80", unsigned)
	}

	memory3 := newSparse()
	memory3.load(t, 0,
		0x80000137,
		insSh,
		insLhu,
	)
	port3 := newPort(t, memory3, 0)
	state3, err := port3.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state3.Registers[1] = 0x1000
	if err := port3.Reset(ctx, state3); err != nil {
		t.Fatal(err)
	}
	run(t, port3, 3)
	half, err := port3.ReadRegister(3)
	if err != nil {
		t.Fatal(err)
	}
	if half != 0 {
		t.Fatalf("lhu 低 16 位应为 0: x3=%#x", half)
	}

	memory4 := newSparse()
	memory4.load(t, 0,
		0x00008137,
		insSh,
		insLh,
	)
	port4 := newPort(t, memory4, 0)
	state4, err := port4.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state4.Registers[1] = 0x1000
	if err := port4.Reset(ctx, state4); err != nil {
		t.Fatal(err)
	}
	run(t, port4, 3)
	signedHalf, err := port4.ReadRegister(3)
	if err != nil {
		t.Fatal(err)
	}
	if signedHalf != 0xFFFFFFFFFFFF8000 {
		t.Fatalf("lh: x3=%#x want 0xFFFFFFFFFFFF8000", signedHalf)
	}
}

func TestJalrAndAuipc(t *testing.T) {

	state, err := executeOne(t, insJalr, func(state *cpu.Machine) { state.Registers[1] = 0x2000 })
	if err != nil {
		t.Fatal(err)
	}
	if state.PC != 0x2000 || state.Registers[3] != 4 {
		t.Fatalf("jalr: PC=%#x x3=%d", state.PC, state.Registers[3])
	}

	auipc, err := executeOne(t, insAuipc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auipc.Registers[4] != 0x01000000 {
		t.Fatalf("auipc: x4=%#x want 0x01000000", auipc.Registers[4])
	}

	if _, err := executeOne(t, insIllegalJalr, func(state *cpu.Machine) { state.Registers[1] = 0x2000 }); err == nil {
		t.Fatal("非法 jalr 应 trap")
	}
}

func TestBranchSignednessVectors(t *testing.T) {

	setup := func(state *cpu.Machine) {
		state.Registers[1] = 0xFFFFFFFFFFFFFFFF
		state.Registers[2] = 1
	}
	for _, tc := range []struct {
		label       string
		instruction uint32
		taken       bool
	}{
		{"blt -1<1 真", insBlt, true},
		{"bge -1>=1 假", insBge, false},
		{"bltu 0xFFFFFFFFFFFFFFFF<1 假", insBltu, false},
		{"bgeu 0xFFFFFFFFFFFFFFFF>=1 真", insBgeu, true},
	} {
		state, err := executeOne(t, tc.instruction, setup)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}

		wantPC := uint64(4)
		if tc.taken {
			wantPC = 8
		}
		if state.PC != wantPC {
			t.Errorf("%s: PC=%d want %d", tc.label, state.PC, wantPC)
		}
	}
}

func TestIllegalEncodingsAreRejected(t *testing.T) {
	for _, tc := range []struct {
		label       string
		instruction uint32
	}{
		{"slli 高位非零", insIllegalShift},
		{"jalr funct3 非零", insIllegalJalr},
		{"system 立即数非法", insIllegalSystem},
	} {
		_, err := executeOne(t, tc.instruction, func(state *cpu.Machine) { state.Registers[1] = 0x1000 })
		var trap cpu.Trap
		if !errors.As(err, &trap) {
			t.Errorf("%s: 应产生 trap: %v", tc.label, err)
			continue
		}
		if trap.Kind != cpu.TrapIllegalInstruction {
			t.Errorf("%s: 类别=%v want illegal-instruction", tc.label, trap.Kind)
		}
	}
}

func TestReservedCompressedEncodingsTrap(t *testing.T) {
	for _, tc := range []struct {
		label       string
		instruction uint16
		want        cpu.TrapKind
	}{
		{"全零编码", 0x0000, cpu.TrapIllegalInstruction},
		{"C.ADDI4SPN 零偏移", 0x0008, cpu.TrapIllegalInstruction},
		{"C.LUI rd=x0", 0x6001, cpu.TrapIllegalInstruction},
		{"C.JR rs1=x0", 0x8002, cpu.TrapIllegalInstruction},
	} {
		memory := newSparse()
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], tc.instruction)
		if err := memory.Write(context.Background(), 0, encoded[:]); err != nil {
			t.Fatal(err)
		}
		port := newPort(t, memory, 0)
		_, err := port.Step(context.Background())
		var trap cpu.Trap
		if !errors.As(err, &trap) {
			t.Errorf("%s: 应产生 trap: %v", tc.label, err)
			continue
		}
		if trap.Kind != tc.want {
			t.Errorf("%s: 类别=%v want %v", tc.label, trap.Kind, tc.want)
		}
		if trap.Address != 0 {
			t.Errorf("%s: trap PC=%#x want 0", tc.label, trap.Address)
		}
	}

	// C.FLD 在 D 扩展落地后展开为 FLD:装入门内存的位型。
	memory := newSparse()
	if err := memory.Write(context.Background(), 0, []byte{0xc4, 0x20}); err != nil {
		t.Fatal(err)
	}
	var pattern [8]byte
	binary.LittleEndian.PutUint64(pattern[:], 0x400C000000000000)
	// 0x20c4 = c.fld f9, 128(s1):位型放在 s1+128 处。
	if err := memory.Write(context.Background(), 0x10+128, pattern[:]); err != nil {
		t.Fatal(err)
	}
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(9, 0x10); err != nil {
		t.Fatal(err)
	}
	if _, err := port.Step(context.Background()); err != nil {
		t.Fatalf("C.FLD 应展开执行: %v", err)
	}
	state, err := port.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Floats[9]; got != 0x400C000000000000 {
		t.Fatalf("f9=%#x want 3.5 的位型", got)
	}
}

const (
	insLd  = 0x0000B183
	insLw  = 0x0000A183
	insLwu = 0x0000E183
	insLw4 = 0x0040A183
	insSd  = 0x0020B023
	insSw  = 0x0020A023

	insAddiw = 0x0010819B
	insSlliw = 0x0010919B
	insSrliw = 0x0010D19B
	insSraiw = 0x4010D19B

	insAddw  = 0x002081BB
	insSubw  = 0x402081BB
	insSllw  = 0x002091BB
	insSrlw  = 0x0020D1BB
	insSraw  = 0x4020D1BB
	insMulw  = 0x022081BB
	insDivw  = 0x0220C1BB
	insDivuw = 0x0220D1BB
	insRemw  = 0x0220E1BB
	insRemuw = 0x0220F1BB

	insMul    = 0x022081B3
	insMulh   = 0x022091B3
	insMulhsu = 0x0220A1B3
	insMulhu  = 0x0220B1B3
	insDiv    = 0x0220C1B3
	insDivu   = 0x0220D1B3
	insRem    = 0x0220E1B3
	insRemu   = 0x0220F1B3

	insLuiSigned   = 0x800001B7
	insAuipcSigned = 0xFFFFF197
)

const (
	minusOne64 = 0xFFFFFFFFFFFFFFFF
	minusTwo64 = 0xFFFFFFFFFFFFFFFE
	minusSeven = 0xFFFFFFFFFFFFFFF9
	minInt64   = 0x8000000000000000
)

func TestRV64WordOperations(t *testing.T) {
	for _, tc := range []struct {
		label       string
		instruction uint32
		left        uint64
		right       uint64
		want        uint64
	}{

		{"addw 0x7FFFFFFF+1 回绕", insAddw, 0x7FFFFFFF, 1, 0xFFFFFFFF80000000},
		{"addw -1+1", insAddw, minusOne64, 1, 0},
		{"subw 0-1", insSubw, 0, 1, minusOne64},
		{"sllw 1<<31", insSllw, 1, 31, 0xFFFFFFFF80000000},
		{"srlw 0xFFFFFFFF>>4", insSrlw, 0xFFFFFFFF, 4, 0x0FFFFFFF},
		{"sraw -0x80000000>>4", insSraw, 0xFFFFFFFF80000000, 4, 0xFFFFFFFFF8000000},
	} {
		state, err := executeOne(t, tc.instruction, func(state *cpu.Machine) {
			state.Registers[1] = tc.left
			state.Registers[2] = tc.right
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if state.Registers[3] != tc.want {
			t.Errorf("%s: x3=%#x want %#x", tc.label, state.Registers[3], tc.want)
		}
	}
	for _, tc := range []struct {
		label       string
		instruction uint32
		source      uint64
		want        uint64
	}{
		{"addiw 0x7FFFFFFF+1 回绕", insAddiw, 0x7FFFFFFF, 0xFFFFFFFF80000000},
		{"slliw 1<<1", insSlliw, 1, 2},
		{"srliw 0xFFFFFFFF>>1", insSrliw, 0xFFFFFFFF, 0x7FFFFFFF},
		{"sraiw -0x80000000>>1", insSraiw, 0xFFFFFFFF80000000, 0xFFFFFFFFC0000000},
	} {
		state, err := executeOne(t, tc.instruction, func(state *cpu.Machine) { state.Registers[1] = tc.source })
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if state.Registers[3] != tc.want {
			t.Errorf("%s: x3=%#x want %#x", tc.label, state.Registers[3], tc.want)
		}
	}
}

func TestRV64MultiplicationAndDivision(t *testing.T) {
	for _, tc := range []struct {
		label       string
		instruction uint32
		left        uint64
		right       uint64
		want        uint64
	}{
		{"mul 3*5", insMul, 3, 5, 15},
		{"mul -1*2", insMul, minusOne64, 2, minusTwo64},
		{"mulh -1*-1", insMulh, minusOne64, minusOne64, 0},
		{"mulh 最小负数*2", insMulh, minInt64, 2, minusOne64},
		{"mulhu 最大*最大", insMulhu, minusOne64, minusOne64, minusTwo64},
		{"mulhsu -1*最大", insMulhsu, minusOne64, minusOne64, minusOne64},
		{"div -7/2 向零截断", insDiv, minusSeven, 2, 0xFFFFFFFFFFFFFFFD},
		{"div 除零得全 1", insDiv, 7, 0, minusOne64},
		{"div 最小负数/-1 溢出", insDiv, minInt64, minusOne64, minInt64},
		{"divu 除零得全 1", insDivu, 7, 0, minusOne64},
		{"divu 最大/2", insDivu, minusOne64, 2, 0x7FFFFFFFFFFFFFFF},
		{"rem -7%2", insRem, minusSeven, 2, minusOne64},
		{"rem 除零取被除数", insRem, 7, 0, 7},
		{"rem 最小负数%-1 溢出", insRem, minInt64, minusOne64, 0},
		{"remu 除零取被除数", insRemu, 7, 0, 7},
		{"remu 最大%2", insRemu, minusOne64, 2, 1},
		{"mulw 0x80000000*2 回绕", insMulw, 0x80000000, 2, 0},
		{"mulw -1*-1", insMulw, 0xFFFFFFFF, 0xFFFFFFFF, 1},

		{"divw 只看低 32 位", insDivw, 0xFFFFFFFF00000001, 1, 1},
		{"divw 除零", insDivw, 1, 0, minusOne64},
		{"divw 最小负数/-1", insDivw, 0xFFFFFFFF80000000, minusOne64, 0xFFFFFFFF80000000},
		{"divuw 0xFFFFFFFF/2", insDivuw, 0xFFFFFFFF, 2, 0x7FFFFFFF},
		{"remw -7%3", insRemw, minusSeven, 3, minusOne64},
		{"remw 除零取被除数", insRemw, 0xFFFFFFFF00000007, 0, 7},
		{"remuw 除零取被除数", insRemuw, 0xFFFFFFFF00000007, 0, 7},
		{"remuw 0xFFFFFFFF%3", insRemuw, 0xFFFFFFFF, 3, 0},
	} {
		state, err := executeOne(t, tc.instruction, func(state *cpu.Machine) {
			state.Registers[1] = tc.left
			state.Registers[2] = tc.right
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if state.Registers[3] != tc.want {
			t.Errorf("%s: x3=%#x want %#x", tc.label, state.Registers[3], tc.want)
		}
	}
}

func TestRV64WideLoadStore(t *testing.T) {
	ctx := context.Background()

	execute := func(t *testing.T, value uint64, instructions ...uint32) *Core {
		t.Helper()
		memory := newSparse()
		memory.load(t, 0, instructions...)
		port := newPort(t, memory, 0)
		state, err := port.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		state.Registers[1] = 0x2000
		state.Registers[2] = value
		if err := port.Reset(ctx, state); err != nil {
			t.Fatal(err)
		}
		run(t, port, len(instructions))
		return port
	}

	port := execute(t, 0x0123456789ABCDEF, insSd, insLd)
	got, err := port.ReadRegister(3)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x0123456789ABCDEF {
		t.Fatalf("ld: x3=%#x want 0x0123456789ABCDEF", got)
	}

	if got, err = execute(t, minusOne64, insSd, insLw).ReadRegister(3); err != nil {
		t.Fatal(err)
	}
	if got != minusOne64 {
		t.Fatalf("lw: x3=%#x want 0xFFFFFFFFFFFFFFFF", got)
	}
	if got, err = execute(t, minusOne64, insSd, insLwu).ReadRegister(3); err != nil {
		t.Fatal(err)
	}
	if got != 0xFFFFFFFF {
		t.Fatalf("lwu: x3=%#x want 0xFFFFFFFF", got)
	}

	if got, err = execute(t, 0x1122334455667788, insSw, insLd).ReadRegister(3); err != nil {
		t.Fatal(err)
	}
	if got != 0x0000000055667788 {
		t.Fatalf("sw 应只写 4 字节: x3=%#x want 0x55667788", got)
	}

	if got, err = execute(t, 0x1122334455667788, insSd, insLw4).ReadRegister(3); err != nil {
		t.Fatal(err)
	}
	if got != 0x11223344 {
		t.Fatalf("lw 4(x1): x3=%#x want 0x11223344", got)
	}
}

func TestRV64ImmediateSignExtension(t *testing.T) {
	state, err := executeOne(t, insLuiSigned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Registers[3] != 0xFFFFFFFF80000000 {
		t.Fatalf("lui: x3=%#x want 0xFFFFFFFF80000000", state.Registers[3])
	}
	auipc, err := executeOne(t, insAuipcSigned, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auipc.Registers[3] != 0xFFFFFFFFFFFFF000 {
		t.Fatalf("auipc: x3=%#x want 0xFFFFFFFFFFFFF000", auipc.Registers[3])
	}
}

func TestRV64ProgramCounterIsWide(t *testing.T) {

	state, err := executeOne(t, insJalr, func(state *cpu.Machine) { state.Registers[1] = 0x1_0000_0002 })
	if err != nil {
		t.Fatal(err)
	}
	if state.PC != 0x1_0000_0002 {
		t.Fatalf("jalr: PC=%#x want 0x100000002", state.PC)
	}
	if state.Registers[3] != 4 {
		t.Fatalf("jalr: x3=%d want 4", state.Registers[3])
	}
}

func TestUnimplementedExtensions(t *testing.T) {
	for _, tc := range []struct {
		label       string
		instruction uint32
	}{
		{"csrrw（Zicsr）", insCSR},
		{"fence.i（Zifencei）", insFenceI},
	} {
		_, err := executeOne(t, tc.instruction, func(state *cpu.Machine) { state.Registers[1] = 0x2000 })
		var trap cpu.Trap
		if !errors.As(err, &trap) {
			t.Errorf("%s: 应产生 trap: %v", tc.label, err)
			continue
		}
		if trap.Kind != cpu.TrapNotImplemented {
			t.Errorf("%s: 类别=%v want not-implemented", tc.label, trap.Kind)
		}
	}
}

func TestRegisterFilePort(t *testing.T) {
	memory := newSparse()
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(10, 0x1122334455667788); err != nil {
		t.Fatal(err)
	}
	got, err := port.ReadRegister(10)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x1122334455667788 {
		t.Fatalf("a0=%#x", got)
	}

	if err := port.WriteRegister(0, 42); err != nil {
		t.Fatal(err)
	}
	if zero, err := port.ReadRegister(0); err != nil || zero != 0 {
		t.Fatalf("x0=%d err=%v", zero, err)
	}
	for _, index := range []int{-1, numRegs} {
		if err := port.WriteRegister(index, 1); !errors.Is(err, errno.Invalid) {
			t.Errorf("写寄存器 %d: %v want EINVAL", index, err)
		}
		if _, err := port.ReadRegister(index); !errors.Is(err, errno.Invalid) {
			t.Errorf("读寄存器 %d: %v want EINVAL", index, err)
		}
	}
}

func encodeAmo(funct5, funct3 uint32, rs1, rs2, rd int) uint32 {
	return funct5<<27 | uint32(rs2)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x2F
}

func TestAmoReadModifyWrite(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		width      int
		funct5     uint32
		initial    uint64
		operand    uint64
		wantMemory uint64
		wantRD     uint64
	}{
		{"swap word", 4, 0x01, 0x11223344, 0xAABBCCDD, 0xAABBCCDD, 0x11223344},
		{"add word wraps", 4, 0x00, 0x7FFFFFFF, 1, 0x80000000, 0x7FFFFFFF},
		{"add word truncates", 4, 0x00, 0xFFFFFFFF, 2, 0x00000001, 0xFFFFFFFFFFFFFFFF},
		{"xor word", 4, 0x04, 0xFF00FF00, 0x0F0F0F0F, 0xF00FF00F, 0xFFFFFFFFFF00FF00},
		{"and word", 4, 0x0C, 0xFF00FF00, 0x0F0F0F0F, 0x0F000F00, 0xFFFFFFFFFF00FF00},
		{"or word", 4, 0x08, 0xFF00FF00, 0x0000000F, 0xFF00FF0F, 0xFFFFFFFFFF00FF00},
		{"min word keeps negative", 4, 0x10, 0xFFFFFFFF, 1, 0xFFFFFFFF, 0xFFFFFFFFFFFFFFFF},
		{"min word takes smaller", 4, 0x10, 5, 0xFFFFFFFF, 0xFFFFFFFF, 5},
		{"max word takes larger", 4, 0x14, 1, 0xFFFFFFFF, 1, 1},
		{"minu word", 4, 0x18, 0x80000000, 1, 1, 0xFFFFFFFF80000000},
		{"maxu word", 4, 0x1C, 0x80000000, 0x7FFFFFFF, 0x80000000, 0xFFFFFFFF80000000},
		{"swap double", 8, 0x01, 0x1122334455667788, 0xAABBCCDDEEFF0011, 0xAABBCCDDEEFF0011, 0x1122334455667788},
		{"add double wraps", 8, 0x00, 0xFFFFFFFFFFFFFFFF, 3, 2, 0xFFFFFFFFFFFFFFFF},
		{"min double negative", 8, 0x10, 0x8000000000000000, 1, 0x8000000000000000, 0x8000000000000000},
		{"max double", 8, 0x14, 0x8000000000000000, 0x7FFFFFFFFFFFFFFF, 0x7FFFFFFFFFFFFFFF, 0x8000000000000000},
		{"minu double", 8, 0x18, 0x8000000000000000, 7, 7, 0x8000000000000000},
		{"maxu double", 8, 0x1C, 0x8000000000000000, 0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF, 0x8000000000000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memory := newSparse()
			address := uint64(0x800)
			raw := make([]byte, tc.width)
			if tc.width == 4 {
				binary.LittleEndian.PutUint32(raw, uint32(tc.initial))
			} else {
				binary.LittleEndian.PutUint64(raw, tc.initial)
			}
			if err := memory.Write(ctx, address, raw); err != nil {
				t.Fatal(err)
			}
			port := newPort(t, memory, 0)
			if err := port.WriteRegister(10, address); err != nil {
				t.Fatal(err)
			}
			if err := port.WriteRegister(11, tc.operand); err != nil {
				t.Fatal(err)
			}
			funct3 := uint32(0x2)
			if tc.width == 8 {
				funct3 = 0x3
			}
			memory.load(t, 0, encodeAmo(tc.funct5, funct3, 10, 11, 5))

			outcome, err := port.Step(ctx)
			if err != nil || outcome != cpu.Continued {
				t.Fatalf("Step: outcome=%v err=%v", outcome, err)
			}
			gotRD, err := port.ReadRegister(5)
			if err != nil {
				t.Fatal(err)
			}
			if gotRD != tc.wantRD {
				t.Fatalf("rd=%#x want %#x", gotRD, tc.wantRD)
			}
			if err := memory.Read(ctx, address, raw); err != nil {
				t.Fatal(err)
			}
			var gotMemory uint64
			if tc.width == 4 {
				gotMemory = uint64(binary.LittleEndian.Uint32(raw))
			} else {
				gotMemory = binary.LittleEndian.Uint64(raw)
			}
			if gotMemory != tc.wantMemory {
				t.Fatalf("memory=%#x want %#x", gotMemory, tc.wantMemory)
			}
		})
	}
}

func TestAmoSignExtendsWordResult(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	address := uint64(0x1000)
	if err := memory.Write(ctx, address, []byte{0x01, 0x00, 0x00, 0x80}); err != nil {
		t.Fatal(err)
	}
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(10, address); err != nil {
		t.Fatal(err)
	}
	memory.load(t, 0, encodeAmo(0x01, 0x2, 10, 0, 5))
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("Step: outcome=%v err=%v", outcome, err)
	}
	got, err := port.ReadRegister(5)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0xFFFFFFFF80000001 {
		t.Fatalf("rd=%#x want 符号扩展后的 0xFFFFFFFF80000001", got)
	}
}

func TestAmoRejectsMisalignedAndReservedEncodings(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label       string
		instruction uint32
		address     uint64
		wantKind    cpu.TrapKind
	}{
		{"word 未对齐", encodeAmo(0x00, 0x2, 10, 11, 5), 0x802, cpu.TrapMemoryFault},
		{"double 未对齐", encodeAmo(0x00, 0x3, 10, 11, 5), 0x804, cpu.TrapMemoryFault},
		{"保留 funct5", encodeAmo(0x05, 0x2, 10, 11, 5), 0x800, cpu.TrapIllegalInstruction},
		{"保留 funct3", encodeAmo(0x00, 0x0, 10, 11, 5), 0x800, cpu.TrapIllegalInstruction},
	} {
		t.Run(tc.label, func(t *testing.T) {
			memory := newSparse()
			memory.load(t, 0, tc.instruction)
			port := newPort(t, memory, 0)
			if err := port.WriteRegister(10, tc.address); err != nil {
				t.Fatal(err)
			}
			if err := port.WriteRegister(11, 1); err != nil {
				t.Fatal(err)
			}
			outcome, err := port.Step(ctx)
			if outcome != cpu.Trapped {
				t.Fatalf("outcome=%v err=%v want Trapped", outcome, err)
			}
			var trap cpu.Trap
			if !errors.As(err, &trap) {
				t.Fatalf("应产生 trap: %v", err)
			}
			if trap.Kind != tc.wantKind {
				t.Fatalf("trap.Kind=%v want %v", trap.Kind, tc.wantKind)
			}
		})
	}
}

func TestAmoWritesMemoryWithZeroDestination(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	address := uint64(0x900)
	if err := memory.Write(ctx, address, []byte{7, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(10, address); err != nil {
		t.Fatal(err)
	}
	if err := port.WriteRegister(11, 9); err != nil {
		t.Fatal(err)
	}
	memory.load(t, 0, encodeAmo(0x00, 0x2, 10, 11, 0))
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("Step: outcome=%v err=%v", outcome, err)
	}
	var raw [4]byte
	if err := memory.Read(ctx, address, raw[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(raw[:]); got != 16 {
		t.Fatalf("memory=%d want 16(rd=x0 仍需完成 7+9 访存)", got)
	}
}

func encodeLr(word bool, rs1, rd int) uint32 {
	// RISC-V A 扩展:LR.W/LR.D 的 funct5 同为 00010,字宽由 funct3 选择
	// (.W=010,.D=011)。LLVM 编码锁定:n04 身份探针首次使用真实 LR.D。
	funct3 := uint32(0x3)
	if word {
		funct3 = 0x2
	}
	return 0x02<<27 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x2F
}

func encodeSc(word bool, rs2, rs1, rd int) uint32 {
	// SC.W/SC.D 的 funct5 同为 00011,字宽由 funct3 选择。
	funct3 := uint32(0x3)
	if word {
		funct3 = 0x2
	}
	return 0x03<<27 | uint32(rs2)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x2F
}

func encodeStoreWord(rs2, rs1 int) uint32 {
	return uint32(rs2)<<20 | uint32(rs1)<<15 | 0x2<<12 | 0x23
}

func setupReservation(t *testing.T, address uint64, initial uint64, word bool) (*sparse, *Core) {
	t.Helper()
	memory := newSparse()
	width := 8
	if word {
		width = 4
	}
	raw := make([]byte, width)
	if word {
		binary.LittleEndian.PutUint32(raw, uint32(initial))
	} else {
		binary.LittleEndian.PutUint64(raw, initial)
	}
	if err := memory.Write(context.Background(), address, raw); err != nil {
		t.Fatal(err)
	}
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(10, address); err != nil {
		t.Fatal(err)
	}
	return memory, port
}

func registerValue(t *testing.T, port *Core, index int) uint64 {
	t.Helper()
	got, err := port.ReadRegister(index)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLoadReservedThenStoreConditional(t *testing.T) {
	ctx := context.Background()
	memory, port := setupReservation(t, 0x800, 0x12345678, true)
	memory.load(t, 0, encodeLr(true, 10, 5), encodeSc(true, 11, 10, 6))
	if err := port.WriteRegister(11, 0xCAFEBABE); err != nil {
		t.Fatal(err)
	}

	outcomes := run(t, port, 2)
	if len(outcomes) != 2 || outcomes[1] != cpu.Continued {
		t.Fatalf("outcomes=%v", outcomes)
	}
	if got := registerValue(t, port, 5); got != 0x12345678 {
		t.Fatalf("lr rd=%#x want 0x12345678", got)
	}
	if got := registerValue(t, port, 6); got != 0 {
		t.Fatalf("sc 成功时 rd=%d want 0", got)
	}
	var raw [4]byte
	if err := memory.Read(ctx, 0x800, raw[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(raw[:]); got != 0xCAFEBABE {
		t.Fatalf("memory=%#x want 0xCAFEBABE", got)
	}
}

func TestStoreConditionalFailsWithoutMatchingReservation(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label        string
		instructions []uint32
		setupAddr    uint64
		scAddr       uint64
	}{
		{"无 LR", []uint32{encodeSc(true, 11, 10, 6)}, 0x800, 0x800},
		{"地址不同", []uint32{encodeLr(true, 10, 5), encodeSc(true, 11, 12, 6)}, 0x800, 0x900},
		{"字长不同", []uint32{encodeLr(true, 10, 5), encodeSc(false, 11, 10, 6)}, 0x800, 0x800},
	} {
		t.Run(tc.label, func(t *testing.T) {
			memory := newSparse()
			for _, address := range []uint64{0x800, 0x900} {
				if err := memory.Write(ctx, address, []byte{1, 2, 3, 4, 5, 6, 7, 8}); err != nil {
					t.Fatal(err)
				}
			}
			port := newPort(t, memory, 0)
			if err := port.WriteRegister(10, tc.setupAddr); err != nil {
				t.Fatal(err)
			}
			if err := port.WriteRegister(11, 99); err != nil {
				t.Fatal(err)
			}
			if err := port.WriteRegister(12, tc.scAddr); err != nil {
				t.Fatal(err)
			}
			memory.load(t, 0, tc.instructions...)
			outcomes := run(t, port, len(tc.instructions))
			if len(outcomes) != len(tc.instructions) {
				t.Fatalf("提前陷入: outcomes=%v", outcomes)
			}
			if got := registerValue(t, port, 6); got != 1 {
				t.Fatalf("%s: sc rd=%d want 1", tc.label, got)
			}
			var raw [8]byte
			if err := memory.Read(ctx, tc.scAddr, raw[:]); err != nil {
				t.Fatal(err)
			}
			if raw[0] != 1 {
				t.Fatalf("%s: SC 失败不得写内存 memory[0]=%d", tc.label, raw[0])
			}
		})
	}
}

func TestOverlappingStoreInvalidatesReservation(t *testing.T) {
	ctx := context.Background()
	memory, port := setupReservation(t, 0x800, 0x11111111, true)
	memory.load(t, 0, encodeLr(true, 10, 5), encodeStoreWord(11, 13), encodeSc(true, 12, 10, 6))
	if err := port.WriteRegister(11, 0x22222222); err != nil {
		t.Fatal(err)
	}
	if err := port.WriteRegister(12, 0x33333333); err != nil {
		t.Fatal(err)
	}
	if err := port.WriteRegister(13, 0x800); err != nil {
		t.Fatal(err)
	}
	outcomes := run(t, port, 3)
	if len(outcomes) != 3 || outcomes[2] != cpu.Continued {
		t.Fatalf("outcomes=%v", outcomes)
	}
	if got := registerValue(t, port, 6); got != 1 {
		t.Fatalf("重叠写入后 sc rd=%d want 1", got)
	}
	var raw [4]byte
	if err := memory.Read(ctx, 0x800, raw[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(raw[:]); got != 0x22222222 {
		t.Fatalf("memory=%#x want 0x22222222", got)
	}
}

func TestNonOverlappingStoreKeepsReservation(t *testing.T) {
	memory, port := setupReservation(t, 0x800, 0x11111111, true)
	memory.load(t, 0, encodeLr(true, 10, 5), encodeStoreWord(11, 13), encodeSc(true, 12, 10, 6))
	if err := port.WriteRegister(11, 0x22222222); err != nil {
		t.Fatal(err)
	}
	if err := port.WriteRegister(12, 0x44444444); err != nil {
		t.Fatal(err)
	}
	if err := port.WriteRegister(13, 0x900); err != nil {
		t.Fatal(err)
	}
	outcomes := run(t, port, 3)
	if len(outcomes) != 3 || outcomes[2] != cpu.Continued {
		t.Fatalf("outcomes=%v", outcomes)
	}
	if got := registerValue(t, port, 6); got != 0 {
		t.Fatalf("不重叠写入后 sc rd=%d want 0", got)
	}
	var raw [4]byte
	if err := memory.Read(context.Background(), 0x800, raw[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(raw[:]); got != 0x44444444 {
		t.Fatalf("memory=%#x want 0x44444444", got)
	}
}

func TestAmoAndFreshLRInvalidateReservation(t *testing.T) {
	ctx := context.Background()
	memory, port := setupReservation(t, 0x800, 7, true)
	if err := port.WriteRegister(11, 1); err != nil {
		t.Fatal(err)
	}
	memory.load(t, 0, encodeLr(true, 10, 5), encodeAmo(0x00, 0x2, 10, 11, 0), encodeSc(true, 11, 10, 6))
	outcomes := run(t, port, 3)
	if len(outcomes) != 3 || outcomes[2] != cpu.Continued {
		t.Fatalf("outcomes=%v", outcomes)
	}
	if got := registerValue(t, port, 6); got != 1 {
		t.Fatalf("AMO 写入后 sc rd=%d want 1", got)
	}
	var raw [4]byte
	if err := memory.Read(ctx, 0x800, raw[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(raw[:]); got != 8 {
		t.Fatalf("memory=%d want 8", got)
	}

	memory2, port2 := setupReservation(t, 0x800, 7, true)
	if err := port2.WriteRegister(12, 0x900); err != nil {
		t.Fatal(err)
	}
	memory2.load(t, 0, encodeLr(true, 10, 5), encodeLr(true, 12, 5), encodeSc(true, 11, 10, 6), encodeSc(true, 11, 12, 7))
	outcomes2 := run(t, port2, 4)
	if len(outcomes2) != 4 || outcomes2[3] != cpu.Continued {
		t.Fatalf("outcomes=%v", outcomes2)
	}
	if got := registerValue(t, port2, 6); got != 1 {
		t.Fatalf("被新 LR 取代后旧 sc rd=%d want 1", got)
	}
	if got := registerValue(t, port2, 7); got != 0 {
		t.Fatalf("最新 LR 的 sc rd=%d want 0", got)
	}
}

func TestStoreConditionalConsumesReservation(t *testing.T) {
	memory, port := setupReservation(t, 0x800, 7, true)
	memory.load(t, 0, encodeLr(true, 10, 5), encodeSc(true, 11, 10, 6), encodeSc(true, 11, 10, 7))
	if err := port.WriteRegister(11, 9); err != nil {
		t.Fatal(err)
	}
	outcomes := run(t, port, 3)
	if len(outcomes) != 3 || outcomes[2] != cpu.Continued {
		t.Fatalf("outcomes=%v", outcomes)
	}
	if got := registerValue(t, port, 6); got != 0 {
		t.Fatalf("首次 sc rd=%d want 0", got)
	}
	if got := registerValue(t, port, 7); got != 1 {
		t.Fatalf("重复 sc rd=%d want 1", got)
	}
}

func TestResetClearsReservation(t *testing.T) {
	memory, port := setupReservation(t, 0x800, 7, true)
	memory.load(t, 0, encodeLr(true, 10, 5), encodeSc(true, 11, 10, 6))
	if err := port.WriteRegister(11, 9); err != nil {
		t.Fatal(err)
	}
	run(t, port, 1)
	state, err := port.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := port.Reset(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if outcome, err := port.Step(context.Background()); err != nil || outcome != cpu.Continued {
		t.Fatalf("Step: outcome=%v err=%v", outcome, err)
	}
	if got := registerValue(t, port, 6); got != 1 {
		t.Fatalf("任务切换保留失效后 sc rd=%d want 1", got)
	}
}

func TestReservedAccessRejectsMisalignedAndMismatchedEncodings(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	memory.load(t, 0, encodeLr(true, 10, 5))
	port := newPort(t, memory, 0)
	if err := port.WriteRegister(10, 0x802); err != nil {
		t.Fatal(err)
	}
	outcome, err := port.Step(ctx)
	if outcome != cpu.Trapped {
		t.Fatalf("outcome=%v err=%v want Trapped", outcome, err)
	}
	var trap cpu.Trap
	if !errors.As(err, &trap) || trap.Kind != cpu.TrapMemoryFault {
		t.Fatalf("未对齐 LR 应产生访存故障: %v", err)
	}

	memory2 := newSparse()
	memory2.load(t, 0, 0x02<<27|uint32(10)<<15|0x1<<12|uint32(5)<<7|0x2F)
	port2 := newPort(t, memory2, 0)
	if err := port2.WriteRegister(10, 0x800); err != nil {
		t.Fatal(err)
	}
	outcome2, err2 := port2.Step(ctx)
	if outcome2 != cpu.Trapped {
		t.Fatalf("outcome=%v err=%v want Trapped", outcome2, err2)
	}
	var trap2 cpu.Trap
	if !errors.As(err2, &trap2) || trap2.Kind != cpu.TrapIllegalInstruction {
		t.Fatalf("保留字宽编码应产生非法指令: %v", err2)
	}
}

func encodeCSR(funct3 uint32, rd, rs1, csr int) uint32 {
	return uint32(csr)<<20 | uint32(rs1)<<15 | funct3<<12 | uint32(rd)<<7 | 0x73
}

func TestCSRCountersAreReadable(t *testing.T) {
	ctx := context.Background()
	memory := newSparse()
	// 前置 2 条指令,使计数器非零;随后 csrrs a0, cycle, x0 与 csrrs a1, instret, x0。
	memory.load(t, 0,
		insAddi5, insAddi7,
		encodeCSR(0x2, 10, 0, int(csrCycle)),
		encodeCSR(0x2, 11, 0, int(csrInstret)),
	)
	port := newPort(t, memory, 0)
	run(t, port, 4)
	if got, err := port.ReadRegister(10); err != nil || got != 2 {
		t.Fatalf("cycle=%d err=%v want 2", got, err)
	}
	// cycle 的 CSR 指令本身已退休,instret 读到 3。
	if got, err := port.ReadRegister(11); err != nil || got != 3 {
		t.Fatalf("instret=%d err=%v want 3", got, err)
	}

	// 立即数形式 csrrsi t2, cycle, 0 同样可读。
	memory.load(t, 16, encodeCSR(0x6, 12, 0, int(csrCycle)))
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("csrrsi: outcome=%v err=%v", outcome, err)
	}
	if got, err := port.ReadRegister(12); err != nil || got != 4 {
		t.Fatalf("第二次 cycle=%d err=%v want 4", got, err)
	}
}

func TestCSRWritesAndUnknownAddressesAreRejected(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label       string
		instruction uint32
		want        cpu.TrapKind
		shouldPass  bool
	}{
		{"csrrw 写 cycle", encodeCSR(0x1, 10, 11, int(csrCycle)), cpu.TrapIllegalInstruction, false},
		{"csrrs 置位写 cycle", encodeCSR(0x2, 10, 11, int(csrCycle)), cpu.TrapIllegalInstruction, false},
		{"csrrc 清位写 cycle", encodeCSR(0x3, 10, 11, int(csrCycle)), cpu.TrapIllegalInstruction, false},
		{"csrrwi 立即写 instret", encodeCSR(0x5, 10, 3, int(csrInstret)), cpu.TrapIllegalInstruction, false},
		{"csrrs 零源读合法", encodeCSR(0x2, 10, 0, int(csrCycle)), 0, true},
		{"time 未实现", encodeCSR(0x2, 10, 0, int(csrTime)), cpu.TrapNotImplemented, false},
		{"mstatus 未实现", encodeCSR(0x2, 10, 0, 0x300), cpu.TrapNotImplemented, false},
	} {
		memory := newSparse()
		memory.load(t, 0, tc.instruction)
		port := newPort(t, memory, 0)
		if err := port.WriteRegister(11, 0x1); err != nil {
			t.Fatal(err)
		}
		outcome, err := port.Step(ctx)
		if tc.shouldPass {
			if err != nil || outcome != cpu.Continued {
				t.Errorf("%s: 应成功: outcome=%v err=%v", tc.label, outcome, err)
			}
			continue
		}
		var trap cpu.Trap
		if !errors.As(err, &trap) || trap.Kind != tc.want {
			t.Errorf("%s: 类别=%v err=%v want %v", tc.label, trap.Kind, err, tc.want)
		}
	}
}

// TestAmoAndOrOpcodes 锁定 RISC-V A 扩展的 funct5:0x08 是 AMOOR,
// 0x0C 是 AMOAND。二者曾互换,导致 V8 的原子任务位图只增不减(Q20)。
func TestAmoAndOrOpcodes(t *testing.T) {
	and, ok := amoOperation(0x0C)
	if !ok || and(0xFF00, 0x0F0F, true) != 0x0F00 {
		t.Fatalf("funct5 0x0C 应为 AMOAND")
	}
	or, ok := amoOperation(0x08)
	if !ok || or(0xF000, 0x000F, true) != 0xF00F {
		t.Fatalf("funct5 0x08 应为 AMOOR")
	}
}

// TestAmoAndOrUseAtomicTable 经真实 memory.Table 执行 AMOAND/AMOOR,
// 覆盖 AtomicMemory 读改写路径与操作码映射。
func TestAmoAndOrUseAtomicTable(t *testing.T) {
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{Start: 0, End: memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite}); err != nil {
		t.Fatal(err)
	}
	if err := table.Map(ctx, memory.Region{Start: memory.PageSize, End: 2 * memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite}); err != nil {
		t.Fatal(err)
	}
	instructions := []uint32{
		encodeAmo(0x0C, 0x2, 10, 11, 0), // amoand.w x0, a1, (a0)
		encodeAmo(0x08, 0x2, 10, 11, 0), // amoor.w x0, a1, (a0)
	}
	var code [8]byte
	for index, instruction := range instructions {
		binary.LittleEndian.PutUint32(code[index*4:], instruction)
	}
	if err := table.Write(ctx, 0, code[:]); err != nil {
		t.Fatal(err)
	}
	if err := table.Protect(ctx, 0, memory.PageSize, memory.ProtRead|memory.ProtExec); err != nil {
		t.Fatal(err)
	}
	var data [4]byte
	binary.LittleEndian.PutUint32(data[:], 0xFF00)
	if err := table.Write(ctx, memory.PageSize, data[:]); err != nil {
		t.Fatal(err)
	}
	port, err := New(cpu.Config{Memory: table, Entry: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := port.WriteRegister(10, memory.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := port.WriteRegister(11, 0x0F0F); err != nil {
		t.Fatal(err)
	}
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("amoand: outcome=%v err=%v", outcome, err)
	}
	if err := port.WriteRegister(11, 0x00F0); err != nil {
		t.Fatal(err)
	}
	if outcome, err := port.Step(ctx); err != nil || outcome != cpu.Continued {
		t.Fatalf("amoor: outcome=%v err=%v", outcome, err)
	}
	if err := table.Read(ctx, memory.PageSize, data[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(data[:]); got != 0x0FF0 {
		t.Fatalf("amoand/amoor 结果=%#x want 0x0ff0", got)
	}
}
