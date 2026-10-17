package executable

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func testProc() proc.Proc {
	return proc.Proc{ID: 2, Parent: 1, Program: "worker", Dir: "/work"}
}

func TestStartRejectsInvalidInput(t *testing.T) {
	b := NewBuiltin()
	if _, err := b.Start(t.Context(), nil, testProc()); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Start(ctx, Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }), testProc()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestWaitReturnsTerminalState(t *testing.T) {
	b := NewBuiltin()
	program := Func(func(context.Context, proc.Proc) (uint8, error) { return 7, nil })
	inst, err := b.Start(t.Context(), program, testProc())
	if err != nil {
		t.Fatal(err)
	}
	result, err := inst.Wait(t.Context())
	if err != nil || result.Code != 7 || result.Reason != Exited {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	again, err := inst.Wait(t.Context())
	if err != nil || again != result {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}

	if err := inst.Interrupt(signal.TERM); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
}

func TestInterruptDeliversSignalTermination(t *testing.T) {
	b := NewBuiltin()
	program := Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Minute):
			return 1, nil
		}
	})
	inst, err := b.Start(context.Background(), program, testProc())
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		if err := inst.Interrupt(signal.TERM); err != nil {
			t.Error(err)
		}
	}()
	result, err := inst.Wait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != Signaled || result.Signal != signal.TERM || result.Code != signal.ExitCode(signal.TERM) {
		t.Fatalf("result=%+v", result)
	}
}

func TestParentCancelIsNotSignal(t *testing.T) {
	b := NewBuiltin()
	program := Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := b.Start(ctx, program, testProc())
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	result, err := inst.Wait(t.Context())

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if result.Reason != Exited || result.Signal != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestFaultIsContained(t *testing.T) {
	b := NewBuiltin()
	program := Func(func(context.Context, proc.Proc) (uint8, error) { panic("客体内部错误") })
	inst, err := b.Start(t.Context(), program, testProc())
	if err != nil {
		t.Fatal(err)
	}
	result, err := inst.Wait(t.Context())
	if err == nil || result.Reason != Fault || result.Code != 125 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRefusesRunningInstance(t *testing.T) {
	b := NewBuiltin()
	program := Func(func(ctx context.Context, _ proc.Proc) (uint8, error) { <-ctx.Done(); return 0, nil })
	inst, err := b.Start(context.Background(), program, testProc())
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Close(); !errors.Is(err, errno.Busy) {
		t.Fatalf("err=%v", err)
	}
	if err := inst.Interrupt(signal.KILL); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := inst.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentWaiters(t *testing.T) {
	b := NewBuiltin()
	program := Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		time.Sleep(30 * time.Millisecond)
		return 3, nil
	})
	inst, err := b.Start(t.Context(), program, testProc())
	if err != nil {
		t.Fatal(err)
	}
	const waiters = 8
	results := make(chan Result, waiters)
	for range waiters {
		go func() {
			r, err := inst.Wait(t.Context())
			if err != nil {
				t.Error(err)
			}
			results <- r
		}()
	}
	for range waiters {
		if r := <-results; r.Code != 3 || r.Reason != Exited {
			t.Fatalf("r=%+v", r)
		}
	}
}

func TestInvalidSignal(t *testing.T) {
	b := NewBuiltin()
	program := Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })
	inst, err := b.Start(t.Context(), program, testProc())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := inst.Interrupt(0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := inst.Interrupt(signal.Max + 1); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestMergeNamesAndReasonString(t *testing.T) {
	a, err := New(map[string]Program{"alpha": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(map[string]Program{"beta": Func(func(context.Context, proc.Proc) (uint8, error) { return 1, nil })})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Merge(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if names := merged.(*Static).Names(); len(names) != 2 || names[0] != "alpha" {
		t.Fatalf("names=%v", names)
	}

	dup, _ := New(map[string]Program{"alpha": Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })})
	if _, err := Merge(a, dup); !errors.Is(err, errno.Exist) {
		t.Fatal(err)
	}

	if _, err := Merge(stubRegistry{}); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	if Quota.String() != "quota" || Reason(9).String() != "unknown" {
		t.Fatal("reason text")
	}

	var interruptError error = interrupt{n: signal.INT}
	if interruptError.Error() != "信号 SIGINT 终止" {
		t.Fatal(interruptError.Error())
	}
}

type stubRegistry struct{}

func (stubRegistry) Lookup(string) (Program, error) { return nil, errno.NotFound }

func TestBuiltinWaitCancelDuringRun(t *testing.T) {
	b := NewBuiltin()
	program := Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		time.Sleep(50 * time.Millisecond)
		return 0, nil
	})
	inst, err := b.Start(context.Background(), program, testProc())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := inst.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := inst.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}
