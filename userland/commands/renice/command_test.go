package renice

import (
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

type stub struct {
	current  int
	sets     map[proc.PID]int
	table    []proc.Proc
	setErr   error
	noNice   bool
	hasTable bool
}

func newStub() *stub {
	return &stub{sets: map[proc.PID]int{}}
}

func (s *stub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}

func (s *stub) Finish(proc.PID, proc.PID, uint8) error { return nil }

func (s *stub) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}

func (s *stub) Nice(pid proc.PID) (int, error) {
	if s.noNice {
		return 0, errno.NoProcess
	}
	return s.current, nil
}

func (s *stub) SetNice(pid proc.PID, nice int) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.sets[pid] = nice
	return nil
}

func (s *stub) Processes() []proc.Proc {
	if !s.hasTable {
		return nil
	}
	return s.table
}

func TestRelativeAdjustment(t *testing.T) {
	stub := newStub()
	stub.current = 3
	r := run(t, stub, "-n", "-5", "-p", "7")
	if r.Code != 0 || stub.sets[7] != -2 {
		t.Fatalf("相对调整 = %+v sets=%v", r, stub.sets)
	}
}

func TestAbsoluteObsoleteForm(t *testing.T) {
	stub := newStub()
	stub.current = 3
	r := run(t, stub, "10", "-p", "7")
	if r.Code != 0 || stub.sets[7] != 10 {
		t.Fatalf("过时形式应设绝对值: %+v sets=%v", r, stub.sets)
	}
}

func TestMultiplePIDsAndDedupe(t *testing.T) {
	stub := newStub()
	r := run(t, stub, "-n", "1", "5", "5", "9")
	if r.Code != 0 || len(stub.sets) != 2 || stub.sets[5] != 1 || stub.sets[9] != 1 {
		t.Fatalf("多 PID 去重 = %+v sets=%v", r, stub.sets)
	}
}

func TestMissingPID(t *testing.T) {
	stub := newStub()
	stub.noNice = true
	r := run(t, stub, "-n", "1", "42")
	if r.Code != 1 || !strings.Contains(r.Stderr, "ESRCH") {
		t.Fatalf("不存在的 pid 应报 ESRCH: %+v", r)
	}
	if len(stub.sets) != 0 {
		t.Fatalf("失败时不应有任何设置: %v", stub.sets)
	}
}

func TestProcessGroupSelection(t *testing.T) {
	stub := newStub()
	stub.hasTable = true
	stub.table = []proc.Proc{
		{ID: 3, PGID: 2},
		{ID: 4, PGID: 2},
		{ID: 5, PGID: 6},
	}
	r := run(t, stub, "-g", "-n", "2", "2")
	if r.Code != 0 || len(stub.sets) != 2 || stub.sets[3] != 2 || stub.sets[4] != 2 {
		t.Fatalf("进程组成员都应调整: %+v sets=%v", r, stub.sets)
	}

	r = run(t, stub, "-g", "-n", "1", "99")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有这个进程组") {
		t.Fatalf("空进程组应报错: %+v", r)
	}
}

func TestUserSelection(t *testing.T) {
	stub := newStub()
	stub.hasTable = true
	stub.table = []proc.Proc{{ID: 2}, {ID: 8}}
	r := run(t, stub, "-u", "-n", "1", "sandbox")
	if r.Code != 0 || len(stub.sets) != 2 {
		t.Fatalf("声明用户的全部进程: %+v sets=%v", r, stub.sets)
	}

	r = run(t, stub, "-u", "-n", "1", "alice")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有这个用户") {
		t.Fatalf("未知用户应报错: %+v", r)
	}
}

func TestMutuallyExclusiveSelection(t *testing.T) {
	r := run(t, newStub(), "-p", "-g", "-n", "1", "5")
	if r.Code != 2 || !strings.Contains(r.Stderr, "互斥") {
		t.Fatalf("-p/-g 互斥: %+v", r)
	}
}

func TestBadOperands(t *testing.T) {
	r := run(t, newStub(), "-n", "1")
	if r.Code != 2 {
		t.Fatalf("缺操作数应退出 2: %+v", r)
	}
	r = run(t, newStub(), "-n", "x", "5")
	if r.Code != 2 {
		t.Fatalf("非整数增量应退出 2: %+v", r)
	}
	r = run(t, newStub(), "-n", "1", "abc")
	if r.Code != 1 || !strings.Contains(r.Stderr, "不是进程 ID") {
		t.Fatalf("非 PID 操作数: %+v", r)
	}
}

func TestSetFailureAggregated(t *testing.T) {
	stub := newStub()
	stub.setErr = errno.Permission
	r := run(t, stub, "-n", "1", "5")
	if r.Code != 1 || !strings.Contains(r.Stderr, "EACCES") {
		t.Fatalf("设置失败应如实上报: %+v", r)
	}
}

func TestNoPriorityPort(t *testing.T) {
	r := run(t, nil, "-n", "1", "5")
	if r.Code != 1 || !strings.Contains(r.Stderr, "优先级控制能力") {
		t.Fatalf("缺端口应如实失败: %+v", r)
	}
}

func run(t *testing.T, control *stub, args ...string) commandtest.Result {
	t.Helper()
	var result commandtest.Result
	var port proc.Control
	if control != nil {
		port = control
	}
	err := commandtest.InvokeWith(context.Background(), Run, t.TempDir(), args, strings.NewReader(""),
		&resultWriter{&result, "out"}, &resultWriter{&result, "err"}, commandtest.Options{Control: port})
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
