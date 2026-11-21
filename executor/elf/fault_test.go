package elf

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/common"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

// TestFaultSignalMapsTrapCategories 逐类验证同步故障 → 信号/siginfo
// 映射:故障地址取数据地址(取指故障为取指地址,空指针保持 0),
// 保护错误与未映射区分为 ACCERR/MAPERR,内核未实现指令不转换。
func TestFaultSignalMapsTrapCategories(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	if err := space.Map(ctx, memory.Region{Start: 0x10000, End: 0x11000, Prot: memory.ProtRead, Name: "ro"}); err != nil {
		t.Fatal(err)
	}
	unmappedErr := space.Read(ctx, 0x90000, make([]byte, 4))
	protectionErr := space.Write(ctx, 0x10000, []byte{1})
	misalignedErr := space.Unmap(ctx, 0x10004, 0x11000)

	table := []struct {
		name        string
		trap        cpu.Trap
		wantNumber  signal.Number
		wantInfo    common.FaultInfo
		convertible bool
	}{
		{"非法指令", cpu.Trap{Kind: cpu.TrapIllegalInstruction, Address: 0x1004},
			signal.ILL, common.FaultInfo{Code: common.SigInfoIllIllegalOp, Addr: 0x1004}, true},
		{"断点", cpu.Trap{Kind: cpu.TrapBreakpoint, Address: 0x1008},
			signal.TRAP, common.FaultInfo{Code: common.SigInfoTrapBreak, Addr: 0x1008}, true},
		{"取指未映射", cpu.Trap{Kind: cpu.TrapMemoryFault, Address: 0x9000, Cause: unmappedErr},
			signal.SEGV, common.FaultInfo{Code: common.SigInfoSegvMapErr, Addr: 0x9000}, true},
		{"数据未映射", cpu.Trap{Kind: cpu.TrapMemoryFault, Address: 0x1000, Instruction: 0x1234, Detail: 0x30000, Cause: unmappedErr},
			signal.SEGV, common.FaultInfo{Code: common.SigInfoSegvMapErr, Addr: 0x30000}, true},
		{"空指针解引用", cpu.Trap{Kind: cpu.TrapMemoryFault, Address: 0x1000, Instruction: 0x1234, Cause: unmappedErr},
			signal.SEGV, common.FaultInfo{Code: common.SigInfoSegvMapErr, Addr: 0}, true},
		{"保护错误", cpu.Trap{Kind: cpu.TrapMemoryFault, Address: 0x1000, Instruction: 0x1234, Detail: 0x10000, Cause: protectionErr},
			signal.SEGV, common.FaultInfo{Code: common.SigInfoSegvAccErr, Addr: 0x10000}, true},
		{"不对齐", cpu.Trap{Kind: cpu.TrapMemoryFault, Address: 0x1000, Instruction: 0x1234, Detail: 0x1002, Cause: misalignedErr},
			signal.BUS, common.FaultInfo{Code: common.SigInfoBusAdraln, Addr: 0x1002}, true},
	}
	for _, entry := range table {
		number, info, convertible := faultSignal(entry.trap)
		if !convertible || number != entry.wantNumber || info != entry.wantInfo {
			t.Fatalf("%s: (%v,%+v,%v) want (%v,%+v,true)",
				entry.name, number, info, convertible, entry.wantNumber, entry.wantInfo)
		}
	}
	if _, _, convertible := faultSignal(cpu.Trap{Kind: cpu.TrapNotImplemented, Address: 0x1000}); convertible {
		t.Fatal("未实现指令不应转换为客体信号(fail-closed)")
	}
}

// TestRealELFFaultDiesBySignal 用真实 RISC-V ELF 验证默认处置的
// 同步故障以信号终止:非法指令→SIGILL、断点→SIGTRAP。
func TestRealELFFaultDiesBySignal(t *testing.T) {
	table := []struct {
		name       string
		program    []uint32
		wantSignal signal.Number
	}{
		{"非法指令", []uint32{0xFFFFFFFF}, signal.ILL},
		{"断点", []uint32{0x00100073}, signal.TRAP},
	}
	for _, entry := range table {
		t.Run(entry.name, func(t *testing.T) {
			harness := newHarness(t, buildImageWith(t, entry.program, ""), nil)
			stub := &sigControlStub{actions: map[signal.Number]signal.ActionConfig{}}
			harness.process.Control = stub
			guest, _, err := Factory{}.New("/program")
			if err != nil {
				t.Fatal(err)
			}
			_, runErr := guest.Run(context.Background(), harness.process)
			var death executable.Death
			if !errors.As(runErr, &death) {
				t.Fatalf("err=%v want executable.Death", runErr)
			}
			if death.Signal != entry.wantSignal {
				t.Fatalf("signal=%v want %v", death.Signal, entry.wantSignal)
			}
		})
	}
}

// TestRealELFSEGVHandlerSeesFaultAddress 用真实 RISC-V ELF 验证
// 无效访问→SIGSEGV 的处理器投递:siginfo 携带 si_code=SEGV_MAPERR
// 与 si_addr=故障地址,处理器经 write 把 siginfo 前 24 字节写到
// 标准输出后以 0x42 退出。
func TestRealELFSEGVHandlerSeesFaultAddress(t *testing.T) {
	output := runSegvHandler(t, 0x42, []uint32{
		0x000DB2B7, // lui t0, 0xDB → 0xDB000(未映射)
		0x0002A503, // lw a0, 0(t0)
	})
	assertSiginfo(t, output, 0xDB000, common.SigInfoSegvMapErr)
}

// TestRealELFProtectionHandlerSeesAccessError 用真实 RISC-V ELF 验证
// 保护错误→SIGSEGV:向只读可执行代码页写入,siginfo 记
// si_code=SEGV_ACCERR 与 si_addr=代码页地址。
func TestRealELFProtectionHandlerSeesAccessError(t *testing.T) {
	output := runSegvHandler(t, 0x43, []uint32{
		0x000102B7, // lui t0, 0x10 → 0x10000(代码页 R|X)
		0x06300313, // li t1, 99
		0x0062A023, // sw t1, 0(t0)
	})
	assertSiginfo(t, output, 0x10000, common.SigInfoSegvAccErr)
}

// runSegvHandler 组装登记 SIGSEGV 处理器(SA_SIGINFO)的镜像并运行:
// 处理器把 siginfo 前 24 字节写到标准输出后以 exitCode 退出。
func runSegvHandler(t *testing.T, exitCode uint8, main []uint32) []byte {
	t.Helper()
	// 处理器:write(1, siginfo, 24) 后按 exitCode 退出。
	handler := []uint32{
		0x04000893, // li a7, 64 (write)
		0x00100513, // li a0, 1
		// a1 = siginfo 指针(SA_SIGINFO 的第二参数)
		0x01800613,                        // li a2, 24
		0x00000073,                        // ecall
		uint32(exitCode)<<20 | 0x00000513, // li a0, exitCode
		0x05D00893,                        // li a7, 93 (exit)
		0x00000073,                        // ecall
	}
	// sigaction:handler=入口+完整主程序长度、SA_SIGINFO。
	var action [32]byte
	main = append([]uint32{
		0x00B00513, // li a0, 11 (SIGSEGV)
		0x000125B7, // lui a1, 0x12
		0x01058593, // addi a1, a1, 16 → act
		0x00000613, // li a2, 0
		0x00800693, // li a3, 8
		0x08600893, // li a7, 134 (rt_sigaction)
		0x00000073, // ecall
	}, main...)
	binary.LittleEndian.PutUint64(action[0:], imageEntry+uint64(len(main))*4)
	binary.LittleEndian.PutUint64(action[8:], uint64(common.SigactionSigInfo))
	image := buildSigactionImage(t, append(append([]uint32{}, main...), handler...), [8]byte{}, action)
	harness := newHarness(t, image, nil)
	stub := &sigControlStub{actions: map[signal.Number]signal.ActionConfig{}}
	harness.process.Control = stub
	guest, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := guest.Run(context.Background(), harness.process)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if code != exitCode {
		t.Fatalf("退出码=%#x want %#x", code, exitCode)
	}
	return harness.stdout.output.Bytes()
}

// assertSiginfo 校验处理器写出的 siginfo:信号号、si_code 与 si_addr。
func assertSiginfo(t *testing.T, raw []byte, wantAddr uint64, wantCode int32) {
	t.Helper()
	if len(raw) < 24 {
		t.Fatalf("siginfo 输出不足 24 字节: %d", len(raw))
	}
	if signo := binary.LittleEndian.Uint32(raw[0:]); signo != uint32(signal.SEGV) {
		t.Fatalf("si_signo=%d want %d", signo, signal.SEGV)
	}
	if code := int32(binary.LittleEndian.Uint32(raw[8:])); code != wantCode {
		t.Fatalf("si_code=%d want %d", code, wantCode)
	}
	if address := binary.LittleEndian.Uint64(raw[16:]); address != wantAddr {
		t.Fatalf("si_addr=%#x want %#x", address, wantAddr)
	}
}
