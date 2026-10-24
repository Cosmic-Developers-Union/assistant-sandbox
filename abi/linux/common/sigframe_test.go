package common

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
)

func binaryLittleEndian(raw []byte) uint64 { return binary.LittleEndian.Uint64(raw) }

func TestSignalFrameRoundTrip(t *testing.T) {
	ctx := context.Background()
	space := newFakeMemory()
	machine := cpu.Machine{
		PC:        0x10108,
		Registers: make([]uint64, 32),
		Floats:    make([]uint64, 32),
		FCSR:      0x17,
	}
	for index := 1; index < 32; index++ {
		machine.Registers[index] = uint64(index) * 0x1000
		machine.Floats[index] = uint64(index)<<32 | 0xBEEF
	}
	machine.Registers[2] = 0x7FF00

	base := AlignFrameBase(0x7FF00)
	if base != 0x7FF00-rtSigframeSize&^0xF {
		t.Fatalf("帧基址=%#x", base)
	}
	info, ucontext, err := WriteSignalFrame(ctx, space, SignalFrameInput{
		Signal:    10,
		FrameBase: base,
		Machine:   machine,
		OldMask:   1 << 9,
		AltStack:  SignalAltStack{Sp: 0x60000, Size: 0x8000, Present: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info != base || ucontext != base+rtSigframeUCOff {
		t.Fatalf("帧地址 info=%#x uc=%#x", info, ucontext)
	}

	state, err := DecodeSignalFrame(ctx, space, base)
	if err != nil {
		t.Fatal(err)
	}
	if state.Machine.PC != machine.PC || state.Machine.FCSR != machine.FCSR {
		t.Fatalf("PC/FCSR 不一致: %#x/%#x", state.Machine.PC, state.Machine.FCSR)
	}
	for index := 1; index < 32; index++ {
		if state.Machine.Registers[index] != machine.Registers[index] {
			t.Fatalf("x%d=%#x want %#x", index, state.Machine.Registers[index], machine.Registers[index])
		}
		if state.Machine.Floats[index] != machine.Floats[index] {
			t.Fatalf("f%d=%#x want %#x", index, state.Machine.Floats[index], machine.Floats[index])
		}
	}
	if state.Machine.Registers[0] != 0 {
		t.Fatal("x0 恢复必须为零")
	}
	if state.Mask != 1<<9 {
		t.Fatalf("屏蔽集=%x", state.Mask)
	}
	// ucontext 的 uc_stack 记录备用栈;siginfo 带信号号与 SI_USER。
	var raw [24]byte
	if err := space.Read(ctx, base+rtSigframeUCOff+ucStackOff, raw[:]); err != nil {
		t.Fatal(err)
	}
	if sp := binaryLittleEndian(raw[0:8]); sp != 0x60000 {
		t.Fatalf("uc_stack.sp=%#x", sp)
	}
}
