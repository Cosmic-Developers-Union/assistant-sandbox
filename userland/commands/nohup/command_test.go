package nohup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

type controlStub struct {
	specs  []proc.RunSpec
	status proc.Proc
	err    error
}

func (c *controlStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotSupported
}

func (c *controlStub) Finish(proc.PID, proc.PID, uint8) error { return nil }

func (c *controlStub) Run(_ context.Context, spec proc.RunSpec) (proc.Proc, error) {
	c.specs = append(c.specs, spec)
	if c.err != nil {
		return proc.Proc{}, c.err
	}
	if spec.Name == "echo" {
		if _, err := fmt.Fprintln(spec.Streams.Output, strings.Join(spec.Args, " ")); err != nil {
			return proc.Proc{}, err
		}
	}
	return c.status, nil
}

type fakeTerminal struct {
	io.Writer
	io.Reader
}

func (fakeTerminal) Close() error                 { return nil }
func (fakeTerminal) Dimensions() (uint16, uint16) { return 24, 80 }

func withTTY(t *testing.T, isTTY func(uint32) bool) {
	t.Helper()
	previous := terminalQuery
	terminalQuery = func(_ context.Context, _ fd.Descriptors, _ proc.PID, id uint32) bool { return isTTY(id) }
	t.Cleanup(func() { terminalQuery = previous })
}

func TestRunsUtilityWithHupIgnored(t *testing.T) {
	stub := &controlStub{}
	dir := t.TempDir()
	result := commandtest.RunWith(t, Run, dir, []string{"echo", "hi"}, "", commandtest.Options{
		Control: stub, Env: map[string]string{"HOME": dir, "KEY": "value"},
	})
	if result.Code != 0 || result.Stdout != "hi\n" || result.Stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
	if len(stub.specs) != 1 {
		t.Fatalf("执行次数: %d", len(stub.specs))
	}
	spec := stub.specs[0]
	if spec.Name != "echo" || strings.Join(spec.Args, " ") != "hi" || spec.Dir != dir {
		t.Fatalf("执行请求: %+v", spec)
	}
	if len(spec.IgnoreSignals) != 1 || spec.IgnoreSignals[0] != signal.HUP {
		t.Fatalf("必须忽略 SIGHUP: %v", spec.IgnoreSignals)
	}
	if spec.Env["KEY"] != "value" {
		t.Fatalf("环境应随子进程走: %v", spec.Env)
	}
	if _, err := os.Stat(filepath.Join(dir, "nohup.out")); err == nil {
		t.Fatal("标准输出不是终端时不应创建 nohup.out")
	}
}

func TestRedirectsToNohupOut(t *testing.T) {
	withTTY(t, func(uint32) bool { return true })
	dir := t.TempDir()
	stub := &controlStub{}
	for _, text := range []string{"first", "second"} {
		result := commandtest.RunWith(t, Run, dir, []string{"echo", text}, "", commandtest.Options{
			Control: stub, Env: map[string]string{"HOME": dir},
		})
		if result.Code != 0 || result.Stderr != "" || result.Stdout != "" {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", text, result.Code, result.Stdout, result.Stderr)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "nohup.out")); err != nil || string(data) != "first\nsecond\n" {
		t.Fatalf("nohup.out = (%q, %v)", data, err)
	}

	withTTY(t, func(id uint32) bool { return id == 1 })
	result := commandtest.RunWith(t, Run, dir, []string{"echo", "third"}, "", commandtest.Options{
		Control: stub, Env: map[string]string{"HOME": dir},
	})
	if result.Code != 0 {
		t.Fatal(result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "nohup.out")); err != nil || string(data) != "first\nsecond\nthird\n" {
		t.Fatalf("追加后 = (%q, %v)", data, err)
	}
}

func TestRefusesWhenRedirectImpossible(t *testing.T) {
	withTTY(t, func(uint32) bool { return true })
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nohup.out"), 0o700); err != nil {
		t.Fatal(err)
	}
	stub := &controlStub{}
	result := commandtest.RunWith(t, Run, dir, []string{"echo", "x"}, "", commandtest.Options{
		Control: stub, Env: map[string]string{},
	})
	if result.Code != 126 || len(stub.specs) != 0 || !strings.Contains(result.Stderr, "nohup.out") {
		t.Fatalf("两处都写不了应 126 且不执行: %+v specs=%d", result, len(stub.specs))
	}
}

func TestRedirectFallsBackToHome(t *testing.T) {
	withTTY(t, func(uint32) bool { return true })
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nohup.out"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := &controlStub{}
	result := commandtest.RunWith(t, Run, dir, []string{"echo", "fallback"}, "", commandtest.Options{
		Control: stub, Env: map[string]string{"HOME": home},
	})
	if result.Code != 0 || result.Stderr != "" || result.Stdout != "" {
		t.Fatalf("应退到 $HOME: %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(home, "nohup.out")); err != nil || string(data) != "fallback\n" {
		t.Fatalf("$HOME/nohup.out = (%q, %v)", data, err)
	}
}

func TestIsTerminalFollowsInstalledTerminal(t *testing.T) {
	table := fd.New()
	table.Attach(1, fd.Streams{Output: io.Discard, Error: io.Discard})
	if isTerminal(t.Context(), table, 1, 1) {
		t.Fatal("Attach 的标准流不应算终端")
	}
	terminal := fakeTerminal{Writer: io.Discard, Reader: strings.NewReader("")}
	if err := table.Install(1, 3, terminal); err != nil {
		t.Fatal(err)
	}
	if !isTerminal(t.Context(), table, 1, 3) {
		t.Fatal("Install 的终端对象应算终端")
	}
	if isTerminal(t.Context(), table, 99, 1) {
		t.Fatal("未知进程不应算终端")
	}
	if isTerminal(t.Context(), table, 1, 7) {
		t.Fatal("未安装的描述符不应算终端")
	}
}

func TestExitStatusMapping(t *testing.T) {
	for _, test := range []struct {
		name string
		stub *controlStub
		code int
	}{
		{"utility 退出 3", &controlStub{status: proc.Proc{ExitCode: 3}}, 3},
		{"被信号终止", &controlStub{status: proc.Proc{ExitCode: 129, ExitSignal: 1}}, 129},
		{"找不到程序", &controlStub{err: errno.NotFound}, 127},
		{"找到但不能执行", &controlStub{err: errno.NoExec}, 126},
		{"其它执行失败", &controlStub{err: errno.Busy}, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.RunWith(t, Run, t.TempDir(), []string{"false"}, "", commandtest.Options{
				Control: test.stub, Env: map[string]string{"HOME": t.TempDir()},
			})
			if result.Code != test.code {
				t.Fatalf("得到 %d，期望 %d（stderr %q）", result.Code, test.code, result.Stderr)
			}
		})
	}
}

func TestMissingOperandAndControlPort(t *testing.T) {
	result := commandtest.Run(t, Run, t.TempDir(), nil, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("缺少操作数: %+v", result)
	}
	result = commandtest.Run(t, Run, t.TempDir(), []string{"echo"}, "")
	if result.Code != 2 || !strings.Contains(result.Stderr, "缺少进程控制端口") {
		t.Fatalf("无控制端口: %+v", result)
	}
}
