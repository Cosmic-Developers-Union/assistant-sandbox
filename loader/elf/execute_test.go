package elf

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu/riscv64"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

var program = []uint32{
	0x000122B7,
	0x00700313,
	0x0062B423,
	0x0082B383,
	0x02A00513,
	0x05D00893,
	0x00000073,
}

func applyPlan(t *testing.T, plan *Plan) *memory.Table {
	t.Helper()
	ctx := context.Background()
	space := memory.New(0)
	for _, segment := range plan.Segments {
		populate := segment.Region
		populate.Prot = memory.ProtRead | memory.ProtWrite
		if err := space.Map(ctx, populate); err != nil {
			t.Fatalf("映射段 %+v: %v", segment.Region, err)
		}
		if err := space.Write(ctx, segment.Address, segment.Data); err != nil {
			t.Fatalf("写入段内容: %v", err)
		}
		if segment.Region.Prot != populate.Prot {
			if err := space.Protect(ctx, segment.Region.Start, segment.Region.End, segment.Region.Prot); err != nil {
				t.Fatalf("收紧段权限: %v", err)
			}
		}
	}
	if err := space.Map(ctx, plan.Stack.Region); err != nil {
		t.Fatalf("映射初始栈: %v", err)
	}
	if err := space.Write(ctx, plan.Stack.ContentBase, plan.Stack.Content); err != nil {
		t.Fatalf("写入初始栈: %v", err)
	}
	return space
}

func TestPlanRunsOnAddressSpace(t *testing.T) {
	ctx := context.Background()
	raw := minimalImage()
	for index, instruction := range program {
		binary.LittleEndian.PutUint32(raw[0x200+index*4:], instruction)
	}
	binary.LittleEndian.PutUint64(raw[offEntry:], imageCodeBase+0x200)
	plan, err := Load(raw, Options{
		Argv:     []string{"prog", "arg"},
		Env:      []string{"A=1"},
		ExecPath: "prog",
		Random:   [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	})
	if err != nil {
		t.Fatal(err)
	}
	space := applyPlan(t, plan)
	core, err := riscv64.New(cpu.Config{Memory: space, Entry: plan.Entry})
	if err != nil {
		t.Fatal(err)
	}

	for index := 0; index < len(program)-1; index++ {
		outcome, err := core.Step(ctx)
		if err != nil {
			t.Fatalf("第 %d 条指令: %v", index+1, err)
		}
		if outcome != cpu.Continued {
			t.Fatalf("第 %d 条指令结果=%v want continued", index+1, outcome)
		}
	}
	state, err := core.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Registers[7] != 7 {
		t.Fatalf("x7=%d want 7：存取没有真的经过地址空间", state.Registers[7])
	}

	var stored [8]byte
	if err := space.Read(ctx, imageDataBase+8, stored[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint64(stored[:]) != 7 {
		t.Fatalf("数据段=%x want 7", stored)
	}

	if err := space.Write(ctx, plan.Entry, []byte{0}); !errors.Is(err, errno.Permission) {
		t.Fatalf("写代码段=%v want EACCES", err)
	}

	outcome, err := core.Step(ctx)
	if outcome != cpu.Trapped {
		t.Fatalf("ECALL 结果=%v want trapped", outcome)
	}
	var trap cpu.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("ECALL 应产生 trap: %v", err)
	}
	if trap.Kind != cpu.TrapEcall {
		t.Fatalf("ECALL 类别=%v", trap.Kind)
	}
	if want := plan.Entry + (uint64(len(program))-1)*4; trap.Address != want {
		t.Fatalf("ECALL 地址=%#x want %#x", trap.Address, want)
	}

	if trap.Detail>>32 != 93 || trap.Detail&0xFFFFFFFF != 42 {
		t.Fatalf("ECALL 参数=%#x want a7=93 a0=42", trap.Detail)
	}

	var argc [8]byte
	if err := space.Read(ctx, plan.Stack.SP, argc[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint64(argc[:]) != 2 {
		t.Fatalf("argc=%d want 2", binary.LittleEndian.Uint64(argc[:]))
	}
	var pointer [8]byte
	if err := space.Read(ctx, plan.Stack.SP+8, pointer[:]); err != nil {
		t.Fatal(err)
	}
	name := make([]byte, 4)
	if err := space.Read(ctx, binary.LittleEndian.Uint64(pointer[:]), name); err != nil {
		t.Fatal(err)
	}
	if string(name) != "prog" {
		t.Fatalf("argv[0]=%q want prog", name)
	}

	detached, err := riscv64.New(cpu.Config{Memory: space, Entry: 0x90000})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = detached.Step(ctx)
	if outcome != cpu.Trapped {
		t.Fatalf("未映射取指结果=%v want trapped", outcome)
	}
	if !errors.As(err, &trap) || trap.Kind != cpu.TrapMemoryFault {
		t.Fatalf("未映射取指 trap=%v want memory-fault", err)
	}
}
