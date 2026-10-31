package elf

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// F12:加载链与执行期故障的诊断必须报告客体路径、阶段与具体原因。
// 路径在 executor/elf 的装载层与故障包装处注入;本文件逐类锁定。

// TestDiagnosticsReportGuestPathAndStage 锁定装载各阶段的诊断形态:
// 缺少映像(读取阶段)、坏魔数(装载阶段)都携带客体路径与原因,
// errno 身份不被包装破坏。
func TestDiagnosticsReportGuestPathAndStage(t *testing.T) {
	harness := newHarness(t, nil, nil)
	program, _, err := Factory{}.New("/missing")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := program.Run(context.Background(), harness.process)
	if !errors.Is(runErr, errno.NotFound) {
		t.Fatalf("缺少映像: 错误=%v want ENOENT", runErr)
	}
	if !strings.Contains(runErr.Error(), "读取客体程序 /missing") {
		t.Fatalf("缺少映像的诊断应含阶段与路径: %v", runErr)
	}

	harness = newHarness(t, []byte("not an elf image at all"), nil) // 非 ELF:头部切片失败
	program, _, err = Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr = program.Run(context.Background(), harness.process)
	if !errors.Is(runErr, errno.NoExec) {
		t.Fatalf("坏魔数: 错误=%v want ENOEXEC 族", runErr)
	}
	// 非 ELF 文本在头部切片处即失败:原因是"超出映像"(截断)。
	if !strings.Contains(runErr.Error(), "装载客体程序 /program") || !strings.Contains(runErr.Error(), "超出映像") {
		t.Fatalf("坏映像的诊断应含阶段、路径与原因: %v", runErr)
	}
}

// TestDiagnosticsInterpreterStageNamesBothImages 锁定解释器阶段的
// 诊断同时携带解释器路径与主程序路径(F05 的 ENOENT 之上补全阶段)。
func TestDiagnosticsInterpreterStageNamesBothImages(t *testing.T) {
	image := buildInterpImage(t, interpreterBody[:2], "/lib/absent-ld.so")
	harness := newHarness(t, image, nil)
	program, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := program.Run(context.Background(), harness.process)
	if !errors.Is(runErr, errno.NotFound) {
		t.Fatalf("解释器缺失: 错误=%v want ENOENT", runErr)
	}
	if !strings.Contains(runErr.Error(), "读取动态解释器 /lib/absent-ld.so") ||
		!strings.Contains(runErr.Error(), "主程序 /program") {
		t.Fatalf("解释器阶段诊断应同时携带两个路径: %v", runErr)
	}
}

// TestDiagnosticsUnimplementedInstructionNamesProgram 锁定执行期
// 故障(内核未实现指令)的诊断携带客体程序路径与 PC/指令编码。
func TestDiagnosticsUnimplementedInstructionNamesProgram(t *testing.T) {
	// fence.i(opcode 0x0F、funct3=0x1)是内核未实现指令:
	// CPU 报 TrapNotImplemented,J06 保持 fail-closed。
	image := buildSigactionImage(t, []uint32{
		0x0000100F, // fence.i
		0x05D00893, // li a7, 93 (exit)
		0x00000073, // ecall
	}, [8]byte{}, [32]byte{})
	harness := newHarness(t, image, nil)
	program, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := program.Run(context.Background(), harness.process)
	if runErr == nil {
		t.Fatal("未实现指令应失败")
	}
	var trap cpu.Trap
	if !errors.As(runErr, &trap) || trap.Kind != cpu.TrapNotImplemented {
		t.Fatalf("错误=%v want TrapNotImplemented", runErr)
	}
	if !strings.Contains(runErr.Error(), "客体程序 /program") {
		t.Fatalf("执行期故障诊断应携带客体路径: %v", runErr)
	}
}
