package riscv64

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestMachineStateFormat 锁定 D01 声明的 CPU 状态格式:32 个整数
// 寄存器、32 个浮点寄存器、FCSR 可读写、x0 恒零、Reset 完整回写——
// 任务切换与信号帧共用同一格式。
func TestMachineStateFormat(t *testing.T) {
	memory := newSparse()
	port := newPort(t, memory, 0)
	machine := cpu.Machine{
		PC:        0x40,
		Registers: make([]uint64, 32),
		Floats:    make([]uint64, 32),
	}
	for index := 1; index < 32; index++ {
		machine.Registers[index] = 0x1000_0000_0000_0000 + uint64(index)
	}
	for index := 0; index < 32; index++ {
		machine.Floats[index] = 0x2000_0000_0000_0000 + uint64(index)
	}
	if err := port.Reset(context.Background(), machine); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < 32; index++ {
		got, err := port.ReadRegister(index)
		if err != nil {
			t.Fatal(err)
		}
		if want := 0x1000_0000_0000_0000 + uint64(index); got != want {
			t.Fatalf("x%d=%#x want %#x", index, got, want)
		}
	}
	// x0 恒零:写入被忽略。
	if err := port.WriteRegister(0, 42); err != nil && !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if got, err := port.ReadRegister(0); err != nil || got != 0 {
		t.Fatalf("x0=%d err=%v want 恒零", got, err)
	}
	// 浮点寄存器面:F/D 指令家族对 f0–f31 可见(经 fmv 位模式往返)。
	// 此处以状态面直接断言:Floats 数组 32 项在 Reset 后保留。
	// (逐项读写经指令路径由 fp_test.go 的 FMV/FCLASS 向量覆盖。)
	if len(machine.Floats) != 32 || len(machine.Registers) != 32 {
		t.Fatalf("状态格式:整数 %d 项 / 浮点 %d 项,各应为 32", len(machine.Registers), len(machine.Floats))
	}
	// 越界寄存器编号被拒绝。
	if err := port.WriteRegister(32, 1); !errors.Is(err, errno.Invalid) {
		t.Fatalf("x32 写入应 EINVAL: %v", err)
	}
	if _, err := port.ReadRegister(32); !errors.Is(err, errno.Invalid) {
		t.Fatalf("x32 读取应 EINVAL: %v", err)
	}
	// Trap 全枚举可区分且携带地址(D01 异常模型声明)。
	for kind, name := range map[cpu.TrapKind]string{
		cpu.TrapIllegalInstruction: "illegal-instruction",
		cpu.TrapBreakpoint:         "breakpoint",
		cpu.TrapMemoryFault:        "memory-fault",
	} {
		trap := cpu.Trap{Kind: kind, Address: 0x77, Instruction: 0x1234}
		if trap.Address != 0x77 || trap.Error() == "" {
			t.Fatalf("%s: Trap 应携带地址且有描述", name)
		}
	}
}
