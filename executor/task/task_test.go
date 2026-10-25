package task

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu/riscv64"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

// registerTLS 是 RISC-V 的线程指针寄存器(tp = x4)。
const registerTLS = 4

// newCore 建立一个执行核心与一块可写可执行的内存。
func newCore(t *testing.T, entry uint64) (cpu.Port, *memory.Table) {
	t.Helper()
	space := memory.New(1 << 20)
	core, err := riscv64.New(cpu.Config{Memory: space, Entry: entry})
	if err != nil {
		t.Fatal(err)
	}
	return core, space
}

// machineState 构造一份带标识值的机器状态:整数寄存器、浮点寄存器、
// FCSR 与 TLS 指针各不相同,便于验证切换后互不污染。
func machineState(pc uint64, marker uint64) cpu.Machine {
	fcsr := uint32(marker & 0xFFFFFFFF)
	state := cpu.Machine{PC: pc, Registers: make([]uint64, 32), Floats: make([]uint64, 32), FCSR: fcsr}
	state.Registers[10] = marker              // a0
	state.Registers[registerTLS] = marker + 1 // tp
	state.Floats[0] = marker<<32 | marker
	return state
}

// TestSwitchPreservesCompleteMachineState 验证 D10:交替切换任务时,
// 完整 CPU 状态(程序计数器、整数、浮点、TLS 指针与 FCSR)互不污染。
func TestSwitchPreservesCompleteMachineState(t *testing.T) {
	ctx := context.Background()
	core, first := newCore(t, 0x1000)
	_, second := newCore(t, 0x2000)
	tasks := []*Task{
		New(first, machineState(0x1000, 0x11)),
		New(second, machineState(0x2000, 0x22)),
	}
	set, err := NewSet(core, tasks, 1)
	if err != nil {
		t.Fatal(err)
	}
	// 首个任务先落到核心上。
	attacher, ok := core.(cpu.Attacher)
	if !ok {
		t.Fatal("执行核心不支持换绑地址空间")
	}
	attacher.Attach(tasks[0].memory)
	if err := core.Reset(ctx, tasks[0].State()); err != nil {
		t.Fatal(err)
	}
	// 两个任务各改写自己的寄存器后交替切换:值不得互相污染。
	for range 4 {
		registers, ok := core.(cpu.RegisterFile)
		if !ok {
			t.Fatal("核心不支持寄存器读写")
		}
		if err := registers.WriteRegister(10, 0x99); err != nil {
			t.Fatal(err)
		}
		if err := registers.WriteRegister(registerTLS, 0x98); err != nil {
			t.Fatal(err)
		}
		floats, ok := core.(cpu.FloatRegisterFile)
		if !ok {
			t.Fatal("核心不支持浮点寄存器")
		}
		if err := floats.WriteFP(0, 0xDEAD); err != nil {
			t.Fatal(err)
		}
		if err := floats.WriteFCSR(0x1F); err != nil {
			t.Fatal(err)
		}
		switched, err := set.Switch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !switched {
			t.Fatal("应发生任务切换")
		}
		// 切换后的任务仍是它自己的值。
		state, err := core.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		marker := uint64(0x22)
		if state.PC != 0x2000 {
			t.Fatalf("程序计数器=%#x want 0x2000", state.PC)
		}
		if state.Registers[10] != marker {
			t.Fatalf("整数寄存器=%#x want %#x", state.Registers[10], marker)
		}
		if state.Registers[registerTLS] != marker+1 {
			t.Fatalf("TLS 指针=%#x want %#x", state.Registers[registerTLS], marker+1)
		}
		if state.Floats[0] != marker<<32|marker {
			t.Fatalf("浮点寄存器=%#x want %#x", state.Floats[0], marker<<32|marker)
		}
		if state.FCSR != uint32(marker) {
			t.Fatalf("FCSR=%#x want %#x", state.FCSR, marker)
		}
		// 再切回第一个任务:它自己的写入保持不变,未被第二个任务污染。
		if _, err := set.Switch(ctx); err != nil {
			t.Fatal(err)
		}
		state, err = core.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state.PC != 0x1000 || state.Registers[10] != 0x99 || state.Registers[registerTLS] != 0x98 {
			t.Fatalf("切回后状态被污染: %+v", state)
		}
		if state.Floats[0] != 0xDEAD || state.FCSR != 0x1F {
			t.Fatalf("切回后浮点状态被污染: %+v", state)
		}
	}
}

// TestTimeSliceBoundsTaskExecution 验证 D11:时间片有界——到期后切换
// 到下一个任务,先到的任务不会独占核心。
func TestTimeSliceBoundsTaskExecution(t *testing.T) {
	ctx := context.Background()
	core, first := newCore(t, 0x1000)
	_, second := newCore(t, 0x2000)
	tasks := []*Task{
		New(first, machineState(0x1000, 0x11)),
		New(second, machineState(0x2000, 0x22)),
	}
	set, err := NewSet(core, tasks, 4)
	if err != nil {
		t.Fatal(err)
	}
	attacher := core.(cpu.Attacher)
	attacher.Attach(tasks[0].memory)
	if err := core.Reset(ctx, tasks[0].State()); err != nil {
		t.Fatal(err)
	}
	// 时间片内不切换。
	for range 3 {
		if set.Tick() {
			t.Fatal("时间片未用尽却要求切换")
		}
	}
	// 第四条指令用尽时间片:切换到下一个任务。
	if !set.Tick() {
		t.Fatal("时间片用尽应要求切换")
	}
	switched, err := set.Switch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !switched {
		t.Fatal("应发生切换")
	}
	if set.Current() != tasks[1] {
		t.Fatal("未切换到下一个任务")
	}
	state, err := core.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.PC != 0x2000 {
		t.Fatalf("程序计数器=%#x want 0x2000", state.PC)
	}
	// 切换前的写入被保存:再次切回该任务后仍可见。
	if err := core.(cpu.RegisterFile).WriteRegister(10, 0x77); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Switch(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = core.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Registers[10] != 0x11 {
		t.Fatalf("另一任务的写入污染了本任务:%#x", state.Registers[10])
	}
	if _, err := set.Switch(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = core.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Registers[10] != 0x77 {
		t.Fatalf("切换前的写入未保存:%#x", state.Registers[10])
	}
	// 零时间片表示不切换。
	plain, err := NewSet(core, tasks, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Tick() {
		t.Fatal("零时间片不应要求切换")
	}
}
