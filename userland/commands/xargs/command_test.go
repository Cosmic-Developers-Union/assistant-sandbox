package xargs

import (
	"context"
	"fmt"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type controlStub struct {
	calls  [][]string
	status proc.Proc
	err    error
}

func (c *controlStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotSupported
}

func (c *controlStub) Finish(proc.PID, proc.PID, uint8) error { return nil }

func (c *controlStub) Run(_ context.Context, spec proc.RunSpec) (proc.Proc, error) {
	c.calls = append(c.calls, append([]string{spec.Name}, spec.Args...))
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

func TestCommandBatching(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
	}{
		{"缺省 echo 一行", nil, "a b c\n", "a b c\n"},
		{"-n2 每批两项", []string{"-n2", "echo"}, "1 2 3 4 5\n", "1 2\n3 4\n5\n"},
		{"成组短选项", []string{"-txn1", "echo"}, "a b\n", "a\nb\n"},
		{"-0 只按 NUL 分", []string{"-0", "echo"}, "a\x00b c\x00", "a b c\n"},
		{"-I 整行替换", []string{"-I", "{}", "echo", "[{}]"}, "x\ny\n", "[x]\n[y]\n"},
		{"-I 空串不替换", []string{"-I", "", "echo", "pre{}post"}, "x\ny\n", "pre{}post\npre{}post\n"},
		{"-d 自定义分隔符", []string{"-d", ",", "echo"}, "a,b,c", "a b c\n"},
		{"-E 逻辑 EOF 串", []string{"-E", "_END_", "echo"}, "a\nb\n_END_\nc\n", "a b\n"},
		{"-L2 每批两逻辑行", []string{"-L", "2", "echo"}, "a b\nc\nd e\n", "a b c\nd e\n"},
		{"空输入仍跑一次", []string{"echo", "ran"}, "   \n", "ran\n"},
		{"-r 空输入不跑", []string{"-r", "echo", "ran"}, "   \n", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &controlStub{}
			result := commandtest.RunWithControl(t, Run, t.TempDir(), test.args, test.input, stub)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("code %d, stdout %q, stderr %q", result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestCommandTraceWritesArgv(t *testing.T) {
	stub := &controlStub{}
	result := commandtest.RunWithControl(t, Run, t.TempDir(), []string{"-t", "-n1", "echo"}, "a b\n", stub)
	if result.Code != 0 || result.Stdout != "a\nb\n" || result.Stderr != "echo a\necho b\n" {
		t.Fatalf("code %d, stdout %q, stderr %q", result.Code, result.Stdout, result.Stderr)
	}
}

func TestCommandExitStatusMapping(t *testing.T) {
	for _, test := range []struct {
		name   string
		stub   *controlStub
		status int
	}{
		{"子进程退出 1", &controlStub{status: proc.Proc{ExitCode: 1}}, 123},
		{"子进程退出 125", &controlStub{status: proc.Proc{ExitCode: 125}}, 123},
		{"子进程退出 255", &controlStub{status: proc.Proc{ExitCode: 255}}, 124},
		{"子进程被信号终止", &controlStub{status: proc.Proc{ExitCode: 130, ExitSignal: 2}}, 125},
		{"找不到程序", &controlStub{err: errno.NotFound}, 127},
		{"找到但不能执行", &controlStub{err: errno.NoExec}, 126},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.RunWithControl(t, Run, t.TempDir(), []string{"false"}, "a\n", test.stub)
			if result.Code != test.status {
				t.Fatalf("得到 %d，期望 %d（stderr %q）", result.Code, test.status, result.Stderr)
			}
		})
	}
}

func TestCommandRejectsUnsupportedAndBadOptions(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"-p 需要控制终端", []string{"-p", "echo"}},
		{"-P 不支持并行", []string{"-P", "2", "echo"}},
		{"未知选项", []string{"--nope", "echo"}},
		{"-n 需要正数", []string{"-n0", "echo"}},
		{"-L 需要正数", []string{"-L0", "echo"}},
		{"-s 需要正数", []string{"-s0", "echo"}},
		{"选项缺少参数", []string{"-I"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &controlStub{}
			result := commandtest.RunWithControl(t, Run, t.TempDir(), test.args, "a\n", stub)
			if result.Code != 2 || result.Stderr == "" || len(stub.calls) != 0 {
				t.Fatalf("code %d, stderr %q, 调用 %v", result.Code, result.Stderr, stub.calls)
			}
		})
	}
}

func TestCommandInputErrors(t *testing.T) {
	for _, input := range []string{"'a b", `a "b`, `a\`} {
		stub := &controlStub{}
		result := commandtest.RunWithControl(t, Run, t.TempDir(), []string{"echo"}, input, stub)
		if result.Code != 1 || result.Stderr == "" || len(stub.calls) != 0 {
			t.Fatalf("输入 %q: code %d, stderr %q, 调用 %v", input, result.Code, result.Stderr, stub.calls)
		}
	}
}

func TestCommandNeedsControlPort(t *testing.T) {
	result := commandtest.Run(t, Run, t.TempDir(), []string{"echo"}, "a\n")
	if result.Code != 2 || !strings.Contains(result.Stderr, "缺少进程控制端口") {
		t.Fatalf("code %d, stderr %q", result.Code, result.Stderr)
	}
}

func TestCommandSizeLimitUsesGuestCeiling(t *testing.T) {
	stub := &controlStub{}
	item := strings.Repeat("x", 1024)
	input := strings.Repeat(item+"\n", 200)
	result := commandtest.RunWithControl(t, Run, t.TempDir(), []string{"echo"}, input, stub)
	if result.Code != 0 {
		t.Fatalf("code %d, stderr %q", result.Code, result.Stderr)
	}
	if len(stub.calls) < 2 {
		t.Fatalf("应当分成多批，得到 %d 批", len(stub.calls))
	}
	for _, argv := range stub.calls {
		if argvSize(argv) > guestArgMax {
			t.Fatalf("批次超过自持上限: %d", argvSize(argv))
		}
	}
}

func TestCommandLongOptionAliases(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
	}{
		{"--null", []string{"--null", "echo"}, "a\x00b c\x00", "a b c\n"},
		{"--no-run-if-empty", []string{"--no-run-if-empty", "echo", "ran"}, "   \n", ""},
		{"--max-args=", []string{"--max-args=1", "echo"}, "a b\n", "a\nb\n"},
		{"--replace=", []string{"--replace={}", "echo", "[{}]"}, "x\n", "[x]\n"},
		{"--eof=", []string{"--eof=_E_", "echo"}, "a\n_E_\nb\n", "a\n"},
		{"--delimiter=", []string{"--delimiter=,", "echo"}, "a,b", "a b\n"},
		{"-i 默认替换串", []string{"-i", "echo", "[{}]"}, "x\n", "[x]\n"},
		{"-e 已废弃的 EOF 别名", []string{"-e_E_", "echo"}, "a\n_E_\n", "a\n"},
		{"-P1 顺序执行", []string{"-P1", "echo"}, "a\n", "a\n"},
		{"--max-procs=1", []string{"--max-procs=1", "echo"}, "a\n", "a\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &controlStub{}
			result := commandtest.RunWithControl(t, Run, t.TempDir(), test.args, test.input, stub)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("code %d, stdout %q, stderr %q", result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestCommandMissingOptionValues(t *testing.T) {
	for _, args := range [][]string{
		{"-n"}, {"--max-args"}, {"-L"}, {"-s"}, {"-I"}, {"-E"}, {"--eof"}, {"-d"}, {"--delimiter"}, {"-P"}, {"--max-procs"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stub := &controlStub{}
			result := commandtest.RunWithControl(t, Run, t.TempDir(), args, "a\n", stub)
			if result.Code != 2 || result.Stderr == "" || len(stub.calls) != 0 {
				t.Fatalf("code %d, stderr %q, 调用 %v", result.Code, result.Stderr, stub.calls)
			}
		})
	}
}

func TestCommandPlanErrorsReachStderr(t *testing.T) {
	stub := &controlStub{}
	result := commandtest.RunWithControl(t, Run, t.TempDir(), []string{"-x", "-s", "8", "echo"}, strings.Repeat("item ", 4), stub)
	if result.Code != 1 || result.Stderr == "" || len(stub.calls) != 0 {
		t.Fatalf("code %d, stderr %q, 调用 %v", result.Code, result.Stderr, stub.calls)
	}
}

func TestApplySizeLimit(t *testing.T) {
	o, err := applySizeLimit(options{maxArgs: -1}, nil)
	if err != nil || o.maxChars != guestArgMax-argMaxHeadroom || o.strictSize {
		t.Fatalf("得到 %+v %v", o, err)
	}

	o, err = applySizeLimit(options{maxArgs: 1}, nil)
	if err != nil || o.maxChars != lineMax {
		t.Fatalf("-n 得到 %+v %v", o, err)
	}

	o, err = applySizeLimit(options{maxArgs: -1, maxChars: guestArgMax * 2, strictSize: true}, nil)
	if err != nil || o.maxChars != guestArgMax-argMaxHeadroom || o.strictSize {
		t.Fatalf("截断得到 %+v %v", o, err)
	}

	huge := map[string]string{"BIG": strings.Repeat("x", guestArgMax)}
	if _, err := applySizeLimit(options{maxArgs: -1}, huge); err == nil {
		t.Fatal("环境过大应当报错")
	}
}

func TestCommandAttachedOptionValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
	}{
		{"-n 分开写值", []string{"-n", "1", "echo"}, "a b\n", "a\nb\n"},
		{"-L 分开写值", []string{"-L", "1", "echo"}, "a\nb\n", "a\nb\n"},
		{"-s 分开写值", []string{"-s", "4096", "-n1", "echo"}, "a\n", "a\n"},
		{"-s 粘在一起", []string{"-s4096", "-n1", "echo"}, "a\n", "a\n"},
		{"-I 粘在一起", []string{"-I{}", "echo", "[{}]"}, "x\n", "[x]\n"},
		{"-E 粘在一起", []string{"-E_END_", "echo"}, "a\n_END_\nb\n", "a\n"},
		{"-d 粘在一起", []string{"-d,", "echo"}, "a,b", "a b\n"},
		{"-- 之后是命令自己的选项", []string{"-n", "1", "--", "echo", "-n"}, "a\n", "-n a\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &controlStub{}
			result := commandtest.RunWithControl(t, Run, t.TempDir(), test.args, test.input, stub)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("code %d, stdout %q, stderr %q", result.Code, result.Stdout, result.Stderr)
			}
		})
	}

	for _, args := range [][]string{{"-P", "2", "echo"}, {"-P2", "echo"}, {"--max-procs=2", "echo"}, {"--max-procs", "0", "echo"}} {
		stub := &controlStub{}
		result := commandtest.RunWithControl(t, Run, t.TempDir(), args, "a\n", stub)
		if result.Code != 2 || !strings.Contains(result.Stderr, "只接受 -P 1") {
			t.Fatalf("%v: code %d, stderr %q", args, result.Code, result.Stderr)
		}
	}

	stub := &controlStub{}
	if result := commandtest.RunWithControl(t, Run, t.TempDir(), []string{"-n1", "ls", "-la"}, "a\n", stub); result.Code != 0 {
		t.Fatalf("code %d, stderr %q", result.Code, result.Stderr)
	}
	if len(stub.calls) != 1 || stub.calls[0][1] != "-la" {
		t.Fatalf("utility 的 argv 被改写: %v", stub.calls)
	}
}
