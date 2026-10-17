package env

import (
	"context"
	"errors"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type execution struct {
	parent proc.PID
	name   string
	args   []string
	dir    string
	env    map[string]string
}

type controlStub struct {
	calls  []execution
	status proc.Proc
	err    error
}

func (c *controlStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotSupported
}

func (c *controlStub) Finish(proc.PID, proc.PID, uint8) error { return nil }

func (c *controlStub) Run(_ context.Context, spec proc.RunSpec) (proc.Proc, error) {
	c.calls = append(c.calls, execution{parent: spec.Parent, name: spec.Name, args: spec.Args, dir: spec.Dir, env: spec.Env})
	return c.status, c.err
}

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"FOO=bar"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "FOO=bar\n") {
		t.Fatalf("赋值: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-i"}, "")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("-i: %+v", result)
	}

	stub := &controlStub{}
	result = commandtest.RunWithControl(t, Run, dir, []string{"-i", "KEY=value", "sh", "-c", "printf ok"}, "", stub)
	if result.Code != 0 {
		t.Fatalf("执行: %+v", result)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("执行次数: %+v", stub.calls)
	}
	call := stub.calls[0]
	if call.parent != 1 || call.name != "sh" || call.dir != dir || strings.Join(call.args, " ") != "-c printf ok" {
		t.Fatalf("执行请求: %+v", call)
	}
	if len(call.env) != 1 || call.env["KEY"] != "value" {
		t.Fatalf("结果环境: %+v", call.env)
	}

	stub = &controlStub{status: proc.Proc{ExitCode: 3}}
	result = commandtest.RunWithControl(t, Run, dir, []string{"false"}, "", stub)
	if result.Code != 3 {
		t.Fatalf("退出码: %+v", result)
	}

	stub = &controlStub{err: errno.NotFound}
	result = commandtest.RunWithControl(t, Run, dir, []string{"missing"}, "", stub)
	if result.Code != 127 || result.Stderr == "" {
		t.Fatalf("未登记: %+v", result)
	}
	for _, err := range []error{errno.NoExec, errno.Permission} {
		stub = &controlStub{err: err}
		result = commandtest.RunWithControl(t, Run, dir, []string{"file"}, "", stub)
		if result.Code != 126 || result.Stderr == "" {
			t.Fatalf("%v: %+v", err, result)
		}
	}

	stub = &controlStub{err: errors.New("内核故障")}
	result = commandtest.RunWithControl(t, Run, dir, []string{"true"}, "", stub)
	if result.Code != 2 || !strings.Contains(result.Stderr, "内核故障") {
		t.Fatalf("执行失败: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--bad"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("--bad 得到 %+v", result)
	}
}

func TestExecutionNeedsControlPort(t *testing.T) {
	result := commandtest.Run(t, Run, t.TempDir(), []string{"-i", "true"}, "")
	if result.Code != 2 || !strings.Contains(result.Stderr, "缺少进程控制端口") {
		t.Fatalf("无控制端口: %+v", result)
	}
}
