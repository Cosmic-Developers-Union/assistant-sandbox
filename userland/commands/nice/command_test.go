package nice

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

type priorityStub struct {
	current  int
	adjust   *int
	ran      string
	preset   error
	noNice   bool
	exitCode uint8
}

func (s *priorityStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}

func (s *priorityStub) Finish(proc.PID, proc.PID, uint8) error { return nil }

func (s *priorityStub) Run(ctx context.Context, spec proc.RunSpec) (proc.Proc, error) {
	if s.preset != nil {
		return proc.Proc{}, s.preset
	}
	s.adjust = spec.NiceAdjust
	s.ran = spec.Name
	return proc.Proc{ExitCode: s.exitCode, State: proc.Zombie}, nil
}

func (s *priorityStub) Nice(proc.PID) (int, error) {
	if s.noNice {
		return 0, errno.NotImplemented
	}
	return s.current, nil
}

func (s *priorityStub) SetNice(proc.PID, int) error { return errno.NotImplemented }

func runWithControlOnly(t *testing.T, stub *controlOnly) commandtest.Result {
	t.Helper()
	var result commandtest.Result
	err := commandtest.InvokeWith(context.Background(), Run, t.TempDir(), nil, strings.NewReader(""),
		&resultWriter{&result, "out"}, &resultWriter{&result, "err"}, commandtest.Options{Control: stub})
	result.Code = exitOf(err)
	return result
}

func runWith(t *testing.T, stub *priorityStub, args ...string) commandtest.Result {
	t.Helper()
	var result commandtest.Result
	ctx := context.Background()
	err := commandtest.InvokeWith(ctx, Run, t.TempDir(), args, strings.NewReader(""),
		&resultWriter{&result, "out"}, &resultWriter{&result, "err"}, commandtest.Options{Control: stub})
	result.Code = exitOf(err)
	return result
}

type resultWriter struct {
	result *commandtest.Result
	stream string
}

func (w *resultWriter) Write(p []byte) (int, error) {
	if w.stream == "out" {
		w.result.Stdout += string(p)
	} else {
		w.result.Stderr += string(p)
	}
	return len(p), nil
}

func exitOf(err error) int {
	if err == nil {
		return 0
	}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	return 1
}

func TestRunsUtilityWithAdjustment(t *testing.T) {
	stub := &priorityStub{}
	r := runWith(t, stub, "-n", "5", "true", "--flag")
	if r.Code != 0 {
		t.Fatalf("执行应成功: %+v", r)
	}
	if stub.ran != "true" || stub.adjust == nil || *stub.adjust != 5 {
		t.Fatalf("增量应传给子进程: ran=%q adjust=%v", stub.ran, stub.adjust)
	}
}

func TestNegativeIncrement(t *testing.T) {
	stub := &priorityStub{}
	r := runWith(t, stub, "-n", "-3", "true")
	if r.Code != 0 || stub.adjust == nil || *stub.adjust != -3 {
		t.Fatalf("负增量: %+v adjust=%v", r, stub.adjust)
	}
	stub = &priorityStub{}
	r = runWith(t, stub, "-n", "+2", "true")
	if r.Code != 0 || stub.adjust == nil || *stub.adjust != 2 {
		t.Fatalf("带符号正增量: %+v", r)
	}
}

func TestNoUtilityPrintsCurrentNice(t *testing.T) {
	stub := &priorityStub{current: 4}
	r := runWith(t, stub, "-n", "5")
	if r.Code != 0 || strings.TrimSpace(r.Stdout) != "4" {
		t.Fatalf("无 utility 应输出当前 nice: %+v", r)
	}
	if stub.ran != "" {
		t.Fatalf("无 utility 不应启动子进程: %q", stub.ran)
	}
}

func TestInvalidIncrement(t *testing.T) {
	stub := &priorityStub{}
	r := runWith(t, stub, "-n", "abc", "true")
	if r.Code != 2 || stub.ran != "" {
		t.Fatalf("非法增量应在执行前拒绝: %+v", r)
	}
}

func TestMissingUtilityReported(t *testing.T) {
	stub := &priorityStub{preset: errno.NotFound}
	r := runWith(t, stub, "nosuch")
	if r.Code != 127 || !strings.Contains(r.Stderr, "未注册沙箱命令") {
		t.Fatalf("未登记程序按 127 报告: %+v", r)
	}
}

type controlOnly struct{}

func (controlOnly) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (controlOnly) Finish(proc.PID, proc.PID, uint8) error { return nil }
func (controlOnly) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}

func TestMissingPriorityPort(t *testing.T) {

	r := runWithControlOnly(t, &controlOnly{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "优先级控制能力") {
		t.Fatalf("缺端口应如实失败: %+v", r)
	}
}

func TestNiceQueryFailure(t *testing.T) {
	stub := &priorityStub{noNice: true}
	r := runWith(t, stub)
	if r.Code != 1 || !strings.Contains(r.Stderr, "读取优先级失败") {
		t.Fatalf("查询失败应如实报告: %+v", r)
	}
}

func TestRunErrorPaths(t *testing.T) {

	var sink bytes.Buffer
	err := commandtest.Invoke(context.Background(), Run, t.TempDir(), []string{"true"}, strings.NewReader(""), &sink, &sink)
	if err == nil {
		t.Fatal("脱离 VM 的执行应失败")
	}
	stub := &priorityStub{preset: errno.Permission}
	r := runWith(t, stub, "true")
	if r.Code != 126 {
		t.Fatalf("不可执行按 126 = %+v", r)
	}

	r = commandtest.Run(t, Run, t.TempDir(), []string{"true"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "优先级控制能力") {
		t.Fatalf("nil Control = %+v", r)
	}
}
