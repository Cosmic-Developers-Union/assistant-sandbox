package contract

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

const (
	FixtureEcho = "contract-echo"

	FixtureFail = "contract-fail"

	FixtureSleep = "contract-sleep"
)

type Reporter interface {
	Helper()
	Error(args ...any)
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

type Factory func() (Session, error)

type Vector struct {
	Name string
	Run  func(t Reporter, factory Factory)
}

func Vectors() []Vector {
	return []Vector{
		{"ExecReturnsExitStateAndOutput", vectorExecReturnsExitStateAndOutput},
		{"WaitIsRepeatableAndConcurrent", vectorWaitIsRepeatableAndConcurrent},
		{"CancelWaitDoesNotKillProcess", vectorCancelWaitDoesNotKillProcess},
		{"UnknownProgramFailsWithoutResidue", vectorUnknownProgramFailsWithoutResidue},
		{"NonzeroExitSurfacesExitCode", vectorNonzeroExitSurfacesExitCode},
		{"SignalTerminatesProcess", vectorSignalTerminatesProcess},
		{"CloseIsIdempotentAndRefusesNewTasks", vectorCloseIsIdempotentAndRefusesNewTasks},
	}
}

func Suite(t *testing.T, factory Factory) {
	t.Helper()
	for _, vector := range Vectors() {
		t.Run(vector.Name, func(t *testing.T) {
			vector.Run(t, factory)
		})
	}
}

func session(t Reporter, factory Factory) Session {
	t.Helper()
	created, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func vectorExecReturnsExitStateAndOutput(t Reporter, factory Factory) {
	t.Helper()
	s := session(t, factory)
	defer closeQuiet(s)
	var out, diag syncBuffer
	process, err := s.Exec(context.Background(), ExecSpec{
		Program: FixtureEcho, Args: []string{"hello", "world"}, Dir: "/work",
		Stdout: &out, Stderr: &diag,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Code != 0 || state.Reason != ReasonExit {
		t.Fatalf("state=%+v", state)
	}
	if out.String() != "hello world\n" {
		t.Fatalf("out=%q", out.String())
	}
}

func vectorWaitIsRepeatableAndConcurrent(t Reporter, factory Factory) {
	t.Helper()
	s := session(t, factory)
	defer closeQuiet(s)
	var out syncBuffer
	process, err := s.Exec(context.Background(), ExecSpec{
		Program: FixtureEcho, Args: []string{"stable"}, Dir: "/work", Stdout: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	const waiters = 4
	results := make(chan ExitState, waiters)
	for range waiters {
		go func() {
			state, err := process.Wait(context.Background())
			if err != nil {
				t.Error(err)
			}
			results <- state
		}()
	}
	for range waiters {
		if state := <-results; state != first {
			t.Fatalf("state=%+v first=%+v", state, first)
		}
	}
}

func vectorCancelWaitDoesNotKillProcess(t Reporter, factory Factory) {
	t.Helper()
	s := session(t, factory)
	defer closeQuiet(s)
	process, err := s.Exec(context.Background(), ExecSpec{
		Program: FixtureSleep, Dir: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := process.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的等待应返回取消错误: %v", err)
	}

	if err := process.Signal(context.Background(), "SIGTERM"); err != nil {
		t.Fatal(err)
	}
	state, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Reason != ReasonSignaled || state.Signal != "SIGTERM" {
		t.Fatalf("state=%+v", state)
	}
}

func vectorUnknownProgramFailsWithoutResidue(t Reporter, factory Factory) {
	t.Helper()
	s := session(t, factory)
	defer closeQuiet(s)
	baseline, err := s.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(context.Background(), ExecSpec{Program: "definitely-missing", Dir: "/work"}); err == nil {
		t.Fatal("未知程序必须失败")
	}
	after, err := s.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(after.Processes) != len(baseline.Processes) {
		t.Fatalf("残留进程: before=%+v after=%+v", baseline, after)
	}
}

func vectorNonzeroExitSurfacesExitCode(t Reporter, factory Factory) {
	t.Helper()
	s := session(t, factory)
	defer closeQuiet(s)
	process, err := s.Exec(context.Background(), ExecSpec{
		Program: FixtureFail, Dir: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Code != 3 || state.Reason != ReasonExit {
		t.Fatalf("state=%+v", state)
	}
}

func vectorSignalTerminatesProcess(t Reporter, factory Factory) {
	t.Helper()
	s := session(t, factory)
	defer closeQuiet(s)
	process, err := s.Exec(context.Background(), ExecSpec{
		Program: FixtureSleep, Dir: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(context.Background(), "SIGTERM"); err != nil {
		t.Fatal(err)
	}
	state, err := process.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Reason != ReasonSignaled || state.Signal != "SIGTERM" || state.Code != 143 {
		t.Fatalf("state=%+v", state)
	}
}

func vectorCloseIsIdempotentAndRefusesNewTasks(t Reporter, factory Factory) {
	t.Helper()
	s := session(t, factory)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close 必须幂等: %v", err)
	}
	if _, err := s.Exec(context.Background(), ExecSpec{Program: FixtureEcho, Dir: "/work"}); err == nil {
		t.Fatal("关闭后的会话必须拒绝新任务")
	}
}

func closeQuiet(s Session) {
	_ = s.Close(context.Background())
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
