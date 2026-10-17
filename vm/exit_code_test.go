package vm

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func TestSignalExitCodeConvention(t *testing.T) {
	for _, tc := range []struct {
		number signal.Number
		want   uint8
	}{
		{signal.HUP, 129},
		{signal.INT, 130},
		{signal.QUIT, 131},
		{signal.KILL, 137},
		{signal.TERM, 143},
	} {
		if got := signal.ExitCode(tc.number); got != tc.want {
			t.Fatalf("%v 的退出码=%d want %d", tc.number, got, tc.want)
		}
	}

	if code := signal.ExitCode(signal.Number(127)); code != 255 {
		t.Fatalf("128+127=%d want 255", code)
	}
}

func TestSignalTerminationKeepsCodeAndSignal(t *testing.T) {
	kernel, process := signalProgramKernel(t)
	if err := kernel.Signal(process.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	status, err := kernel.RunRegistered(context.Background(), process)
	if err != nil {
		t.Fatal(err)
	}
	if status.Signal != signal.TERM {
		t.Fatalf("Signal=%v want SIGTERM", status.Signal)
	}
	if status.Code != signal.ExitCode(signal.TERM) {
		t.Fatalf("Code=%d want %d", status.Code, signal.ExitCode(signal.TERM))
	}
}

func TestNormalExitKeepsCodeWithoutSignal(t *testing.T) {
	kernel, process := exitProgramKernel(t, 42)
	status, err := kernel.RunRegistered(context.Background(), process)
	if err != nil {
		t.Fatal(err)
	}
	if status.Signal != 0 {
		t.Fatalf("正常退出不应带信号: %v", status.Signal)
	}
	if status.Code != 42 {
		t.Fatalf("Code=%d want 42", status.Code)
	}
}

func signalProgramKernel(t *testing.T) (*Kernel, proc.Proc) {
	t.Helper()
	registry, err := registryFor("long")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 4)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process
}

func exitProgramKernel(t *testing.T, code uint8) (*Kernel, proc.Proc) {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"exit": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return code, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 4)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "exit", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process
}
