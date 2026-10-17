package common

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type stepResult struct {
	outcome cpu.Outcome
	err     error
}

type scriptedCore struct {
	steps []stepResult
	index int
}

func (c *scriptedCore) Reset(context.Context, cpu.Machine) error { return nil }

func (c *scriptedCore) State(context.Context) (cpu.Machine, error) { return cpu.Machine{}, nil }

func (c *scriptedCore) WordSize() cpu.WordSize {
	return cpu.WordSize{Bits: 64, Bytes: 8, LittleEndian: true}
}

func (c *scriptedCore) Step(context.Context) (cpu.Outcome, error) {
	if c.index >= len(c.steps) {
		return 0, errno.Wrap(errno.Invalid, "测试脚本已耗尽")
	}
	result := c.steps[c.index]
	c.index++
	return result.outcome, result.err
}

type scriptedDispatcher struct {
	exits []Exit
	errs  []error
	traps []cpu.Trap
	index int
}

func (d *scriptedDispatcher) Dispatch(_ context.Context, trap cpu.Trap) (Exit, error) {
	d.traps = append(d.traps, trap)
	index := d.index
	d.index++
	if index < len(d.errs) && d.errs[index] != nil {
		return Exit{}, d.errs[index]
	}
	if index < len(d.exits) {
		return d.exits[index], nil
	}
	return Exit{}, nil
}

func trap(address uint64) cpu.Trap {
	return cpu.Trap{Kind: cpu.TrapEcall, Address: address, Instruction: 0x00000073}
}

func TestRunnerStopsOnExit(t *testing.T) {
	core := &scriptedCore{steps: []stepResult{
		{outcome: cpu.Trapped, err: trap(0x1000)},
		{outcome: cpu.Continued},
	}}
	dispatcher := &scriptedDispatcher{exits: []Exit{{Requested: true, Code: 7}}}
	runner, err := NewRunner(Config{Core: core, Dispatcher: dispatcher})
	if err != nil {
		t.Fatal(err)
	}
	code, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Fatalf("退出状态=%d want 7", code)
	}
	if len(dispatcher.traps) != 1 || dispatcher.traps[0].Address != 0x1000 {
		t.Fatalf("trap=%+v", dispatcher.traps)
	}
	if core.index != 1 {
		t.Fatalf("退出后不应继续执行：已执行 %d 步", core.index)
	}
}

func TestRunnerContinuesAfterSyscall(t *testing.T) {
	core := &scriptedCore{steps: []stepResult{
		{outcome: cpu.Trapped, err: trap(0x1000)},
		{outcome: cpu.Continued},
		{outcome: cpu.Trapped, err: trap(0x1004)},
	}}

	dispatcher := &scriptedDispatcher{exits: []Exit{{}, {Requested: true, Code: 0}}}
	runner, err := NewRunner(Config{Core: core, Dispatcher: dispatcher})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.traps) != 2 {
		t.Fatalf("trap 次数=%d want 2", len(dispatcher.traps))
	}
}

func TestRunnerStopsOnHalt(t *testing.T) {
	core := &scriptedCore{steps: []stepResult{{outcome: cpu.Halted}}}
	runner, err := NewRunner(Config{Core: core, Dispatcher: &scriptedDispatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	code, err := runner.Run(context.Background())
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestRunnerPropagatesFault(t *testing.T) {
	fault := Fault{Trap: cpu.Trap{Kind: cpu.TrapMemoryFault, Address: 0x2000, Cause: errno.Range}}
	core := &scriptedCore{steps: []stepResult{{outcome: cpu.Trapped, err: fault}}}
	dispatcher := &scriptedDispatcher{errs: []error{fault}}
	runner, err := NewRunner(Config{Core: core, Dispatcher: dispatcher})
	if err != nil {
		t.Fatal(err)
	}
	code, err := runner.Run(context.Background())
	var got Fault
	if !errors.As(err, &got) {
		t.Fatalf("err=%v want Fault", err)
	}

	if !errors.Is(err, errno.Range) {
		t.Fatalf("故障应保留底层身份: %v", err)
	}

	if code != 0 {
		t.Fatalf("故障时不应给出退出状态: %d", code)
	}

	if message := got.Error(); !containsAll(message, "memory-fault", "0x2000") {
		t.Fatalf("故障文本=%q", message)
	}
}

func TestRunnerPropagatesExecutionErrors(t *testing.T) {
	for _, want := range []error{errno.Limit, context.Canceled} {
		core := &scriptedCore{steps: []stepResult{{outcome: 0, err: want}}}
		runner, err := NewRunner(Config{Core: core, Dispatcher: &scriptedDispatcher{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(context.Background()); !errors.Is(err, want) {
			t.Fatalf("err=%v want %v", err, want)
		}
	}
}

func TestRunnerRejectsMalformedResults(t *testing.T) {
	core := &scriptedCore{steps: []stepResult{{outcome: cpu.Trapped}}}
	runner, err := NewRunner(Config{Core: core, Dispatcher: &scriptedDispatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background()); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v want EINVAL", err)
	}
	unknown := &scriptedCore{steps: []stepResult{{outcome: cpu.Outcome(99)}}}
	runner, err = NewRunner(Config{Core: unknown, Dispatcher: &scriptedDispatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background()); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v want EINVAL", err)
	}
}

func TestRunnerHonoursCancellation(t *testing.T) {
	core := &scriptedCore{steps: []stepResult{{outcome: cpu.Continued}}}
	runner, err := NewRunner(Config{Core: core, Dispatcher: &scriptedDispatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if core.index != 0 {
		t.Fatalf("取消后不应执行指令：已执行 %d 步", core.index)
	}
}

func TestRunnerValidation(t *testing.T) {
	if _, err := NewRunner(Config{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v want EINVAL", err)
	}
	if _, err := NewRunner(Config{Core: &scriptedCore{}}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v want EINVAL", err)
	}
	if _, err := NewRunner(Config{Dispatcher: &scriptedDispatcher{}}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v want EINVAL", err)
	}
}

func TestExitOnlyHandler(t *testing.T) {
	handler := ExitOnly{}
	ctx := context.Background()
	for _, syscall := range []Syscall{SysExit, SysExitGroup} {
		reply := handler.Handle(ctx, Call{Syscall: syscall, Args: [6]uint64{7}})
		if !reply.Exit || reply.Value != 7 {
			t.Fatalf("%v: reply=%+v", syscall, reply)
		}
	}
	for _, syscall := range []Syscall{SysUnknown, Syscall(999)} {
		reply := handler.Handle(ctx, Call{Syscall: syscall})
		if reply.Exit || reply.Errno != errno.NotImplemented {
			t.Fatalf("%v: reply=%+v", syscall, reply)
		}
	}
}

func TestSyscallNames(t *testing.T) {
	for syscall, want := range map[Syscall]string{
		SysUnknown: "unknown", SysExit: "exit", SysExitGroup: "exit_group",
		SysRead: "read", SysWrite: "write", SysOpenAt: "openat", SysClose: "close",
		SysLseek: "lseek", SysGetPID: "getpid", SysGetPPID: "getppid", SysGetTID: "gettid",
		SysGetUID: "getuid", SysGetEUID: "geteuid", SysGetGID: "getgid", SysGetEGID: "getegid",
		SysClockGettime: "clock_gettime",
	} {
		if got := syscall.String(); got != want {
			t.Fatalf("%v=%q want %q", syscall, got, want)
		}
	}
	if got := Syscall(999).String(); got != "syscall(999)" {
		t.Fatalf("未知调用号=%q", got)
	}
}

func containsAll(text string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			return false
		}
	}
	return true
}
