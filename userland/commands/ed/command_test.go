package ed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type shellCall struct {
	parent  proc.PID
	name    string
	args    []string
	streams fd.Streams
}

type controlStub struct {
	calls  []shellCall
	status proc.Proc
	err    error
}

func (c *controlStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotSupported
}

func (c *controlStub) Finish(proc.PID, proc.PID, uint8) error { return nil }

func (c *controlStub) Run(_ context.Context, spec proc.RunSpec) (proc.Proc, error) {
	c.calls = append(c.calls, shellCall{parent: spec.Parent, name: spec.Name, args: spec.Args, streams: spec.Streams})
	return c.status, c.err
}

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPipeline(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, nil, "a\nhello\n.\nw out\n,p\nq\n")
	if result.Code != 0 {
		t.Fatalf("会话: %+v", result)
	}

	if result.Stdout != "6\nhello\n" {
		t.Fatalf("stdout: %q", result.Stdout)
	}
	if body := read(t, dir, "out"); body != "hello\n" {
		t.Fatalf("out: %q", body)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s"}, "r out\n,p\nq\nq\n")
	if result.Code != 1 || result.Stdout != "hello\n" || result.Stderr != "?\n" {
		t.Fatalf("r 读回: %+v", result)
	}
}

func TestInitialOperand(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f", "abc\n")
	result := commandtest.Run(t, Run, dir, []string{"f"}, "1p\nq\n")
	if result.Code != 0 || result.Stdout != "4\nabc\n" {
		t.Fatalf("初始文件: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"nope"}, "q\n")
	if result.Code != 1 || result.Stdout != "" || result.Stderr == "" {
		t.Fatalf("初始文件缺失: %+v", result)
	}
}

func TestOptions(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-p", "*"}, "q\n")
	if result.Code != 0 || result.Stdout != "*" {
		t.Fatalf("-p: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s"}, "a\nhi\n.\nw out\nq\n")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("-s: %+v", result)
	}
	if body := read(t, dir, "out"); body != "hi\n" {
		t.Fatalf("out: %q", body)
	}

	result = commandtest.Run(t, Run, dir, []string{"a", "b"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("操作数: %+v", result)
	}
}

func TestDiagnostics(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, nil, "a\nx\n.\ns/y/z/\nh\nq\nq\n")
	if result.Code != 1 {
		t.Fatalf("诊断: %+v", result)
	}
	if result.Stderr != "?\n没有匹配\n?\n" {
		t.Fatalf("stderr: %q", result.Stderr)
	}
	result = commandtest.Run(t, Run, dir, nil, "Y\n")
	if result.Code != 1 || result.Stderr != "?\n" {
		t.Fatalf("未知命令: %+v", result)
	}
}

func TestShellEscape(t *testing.T) {
	dir := t.TempDir()
	stub := &controlStub{}
	result := commandtest.RunWithControl(t, Run, dir, nil, "!printf hi\nq\n", stub)
	if result.Code != 0 {
		t.Fatalf("!: %+v", result)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("执行次数: %d", len(stub.calls))
	}
	call := stub.calls[0]
	if call.parent != 1 || call.name != "bash" || len(call.args) != 2 || call.args[0] != "-c" || call.args[1] != "printf hi" {
		t.Fatalf("执行请求: %+v", call)
	}
	if call.streams.Output == nil || call.streams.Error == nil || call.streams.Input == nil {
		t.Fatalf("标准流必须透传: %+v", call.streams)
	}

	stub = &controlStub{status: proc.Proc{ExitCode: 3}}
	result = commandtest.RunWithControl(t, Run, dir, nil, "!false\nq\n", stub)
	if result.Code != 0 || result.Stdout != "!\n" {
		t.Fatalf("子进程失败: %+v", result)
	}

	stub = &controlStub{err: errors.New("启动失败")}
	result = commandtest.RunWithControl(t, Run, dir, nil, "!ls\nq\n", stub)
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("启动失败: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, nil, "!ls\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "进程控制端口") {
		t.Fatalf("无端口: %+v", result)
	}
}

func TestFileErrors(t *testing.T) {
	dir := t.TempDir()

	result := commandtest.Run(t, Run, dir, nil, "a\nx\n.\nw nodir/out\nq\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "nodir/out") {
		t.Fatalf("w 失败: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, nil, "a\nx\n.\nW nodir/out\nq\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "nodir/out") {
		t.Fatalf("W 失败: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, nil, "r .\nq\n")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("r 目录: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, nil, "e .\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "?") {
		t.Fatalf("e 目录: %+v", result)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"--bogus"}, {"-x"}, {"--help"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("选项 %v: %+v", args, result)
		}
	}
}
