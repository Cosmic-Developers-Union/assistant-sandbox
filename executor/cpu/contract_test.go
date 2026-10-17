package cpu

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

var _ error = Trap{}

func TestOutcomeAndTrapNames(t *testing.T) {
	for outcome, want := range map[Outcome]string{
		Continued: "continued", Halted: "halted", Trapped: "trapped",
	} {
		if got := outcome.String(); got != want {
			t.Errorf("Outcome(%d)=%q want %q", outcome, got, want)
		}
	}
	for kind, want := range map[TrapKind]string{
		TrapIllegalInstruction: "illegal-instruction",
		TrapNotImplemented:     "not-implemented",
		TrapEcall:              "ecall",
		TrapBreakpoint:         "breakpoint",
		TrapMemoryFault:        "memory-fault",
	} {
		if got := kind.String(); got != want {
			t.Errorf("TrapKind(%d)=%q want %q", kind, got, want)
		}
	}

	if got := Outcome(0).String(); got != "outcome(0)" {
		t.Fatalf("Outcome(0)=%q", got)
	}
	if got := Outcome(99).String(); got != "outcome(99)" {
		t.Fatalf("未知 Outcome=%q", got)
	}
	if got := TrapKind(0).String(); got != "trap(0)" {
		t.Fatalf("TrapKind(0)=%q", got)
	}
	if got := TrapKind(99).String(); got != "trap(99)" {
		t.Fatalf("未知 TrapKind=%q", got)
	}
}

func TestTrapDiagnostics(t *testing.T) {
	trap := Trap{Kind: TrapEcall, Address: 0x1000, Instruction: 0x73, Cause: errno.Range}
	if got := trap.Error(); got != "ecall@0x1000: instruction 0x73" {
		t.Fatalf("trap 文本=%q", got)
	}
	if !errors.Is(trap, errno.Range) {
		t.Fatal("trap 应保留底层错误身份")
	}

	zero := Trap{Kind: TrapIllegalInstruction}
	if got := zero.Error(); got != "illegal-instruction@0x0: instruction 0x0" {
		t.Fatalf("零地址 trap 文本=%q", got)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (Config{}).Validate(); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺内存应报 EINVAL: %v", err)
	}
	if err := (Config{Memory: stubMemory{}}).Validate(); err != nil {
		t.Fatalf("有内存应通过: %v", err)
	}
}

type stubMemory struct{}

func (stubMemory) Read(context.Context, uint64, []byte) error  { return nil }
func (stubMemory) Write(context.Context, uint64, []byte) error { return nil }
