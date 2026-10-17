package m4

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type controlStub struct {
	specs []proc.RunSpec
	code  uint8
	err   error
	echo  string
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
	if c.echo != "" && spec.Streams.Output != nil {
		if _, err := fmt.Fprint(spec.Streams.Output, c.echo); err != nil {
			return proc.Proc{}, err
		}
	}
	return proc.Proc{ExitCode: c.code}, nil
}

func withoutEntropy(t *testing.T, run func(context.Context, []string) error) func(context.Context, []string) error {
	t.Helper()
	return func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		set.Entropy = nil
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
}

func TestMacroSemantics(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{"定义与调用", "define(`greet', `hello')greet", "hello"},
		{"参数展开", "define(`ex', `$1+$2=$3')ex(a, b, c)", "a+b=c"},
		{"零号参数", "define(`who', `I am `$0'')who", "I am who"},
		{"参数个数", "define(`n', `$#')n-n()", "0-1"},
		{"越界参数为空", "define(`e', `[$9]')e(1)", "[]"},
		{"双引号延迟展开", "define(`q', ``raw text'')q", "raw text"},
		{"改引号", "changequote(<<,>>)define(<<a>>, <<b>>)a", "b"},
		{"改引号后恢复", "changequote(<<,>>)changequote`'define(`c',`d')c", "d"},
		{"空开始引号关闭引号", "changequote(`',)a`b", "a`b"},
		{"注释原样透传", "x # not macro\ny", "x # not macro\ny"},
		{"改注释定界符", "changecom(//)a // hid den\nb", "a // hid den\nb"},
		{"dnl 丢弃到行尾", "a\ndnl hidden\nb", "a\nb"},
		{"ifelse 相等", "ifelse(a, a, yes, no)", "yes"},
		{"ifelse 不相等无否则", "ifelse(a, b, yes)", ""},
		{"ifelse 四参否则", "ifelse(a, b, yes, no)", "no"},
		{"define 空名忽略", "define(`', `x')ok", "ok"},
		{"substr 负起点为空", "substr(abcd, -1)", ""},
		{"ifelse 链式", "ifelse(a,b,1,c,c,2,d,d,3,other)", "2"},
		{"shift 去头", "define(`s', `shift($@)')s(1,2,3)", "2,3"},
		{"len", "len(abc)", "3"},
		{"index 命中", "index(abcd, c)", "2"},
		{"index 未命中", "index(abcd, z)", "-1"},
		{"substr 带长度", "substr(abcd, 1, 2)", "bc"},
		{"substr 到结尾", "substr(abcd, 2)", "cd"},
		{"substr 越界为空", "substr(abcd, 9)", ""},
		{"translit 替换", "translit(abc, b, x)", "axc"},
		{"translit 删除", "translit(abc, b)", "ac"},
		{"translit 区间", "translit(abc, a-c, xyz)", "xyz"},
		{"incr", "incr(41)", "42"},
		{"decr", "decr(0)", "-1"},
		{"ifdef 已定义", "define(`v', 1)ifdef(`v', yes, no)", "yes"},
		{"ifdef 未定义", "ifdef(`w', yes, no)", "no"},
		{"pushdef 覆盖与弹出", "define(`x', one)pushdef(`x', two)x-popdef(`x')x-popdef(`x')x", "two-one-x"},
		{"undefine", "define(`x', v)undefine(`x')x", "x"},
		{"defn 复制文本宏", "define(`x', v)define(`y', defn(`x'))y", "v"},
		{"defn 未定义为空", "defn(nosuch)", ""},
		{"defn 内建非单独取用为空", "defn(define, undefine)", ""},
		{"内建改名", "define(`mydef', defn(`define'))mydef(`y', `ok')y", "ok"},
		{"盲内建无括号按文本", "define", "define"},
		{"星号与艾特", "define(`s', `$*|$@')s(a,b)", "a,b|a,b"},
		{"m4wrap 在最后处理", "m4wrap(final )a", "afinal "},
		{"m4wrap 先进先出", "m4wrap(one)m4wrap(two)", "onetwo"},
		{"divnum", "A divnum B", "A 0 B"},
		{"divert 数字前导空白", "divnum divert( 3)divnum", "0 3"},
		{"undivert 无参数按号序", "divert(2)two divert(1)one divert(0)undivert", "one two "},
		{"undivert 按点名取回", "divert(2)two divert(1)one divert(0)undivert(2)-undivert(1)", "two -one "},
		{"divert 负一丢弃", "divert(-1)gone divert(0)kept", "kept"},
		{"divert 越界丢弃", "divert(10)x divert(0)y", "y"},
		{"括号配对不计逗号", "define(`one', `$1')one((a,b), c)", "(a,b)"},
		{"引号内的逗号不是分隔", "define(`one', `$1')one(`a,b')", "a,b"},
		{"参数前部空白丢弃", "define(`one', `$1')one( x )", "x "},
		{"内建展开可再扫描", "define(`pair', `$2 $1')pair(b, a)", "a b"},
		{"未定义名字原样保留", "plain words stay", "plain words stay"},
		{"美元号原样保留", "cost $5 and $x", "cost $5 and $x"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, t.TempDir(), nil, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("code=%d stdout=%q stderr=%q，期望 %q", result.Code, result.Stdout, result.Stderr, test.want)
			}
		})
	}
}

func TestEvalSemantics(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{"优先级", "eval(1+2*3)", "7"},
		{"括号", "eval((1+2)*3)", "9"},
		{"取模", "eval(5%3)", "2"},
		{"整除截断", "eval(7/2)", "3"},
		{"一元负号", "eval(-3+5)", "2"},
		{"位或", "eval(1|2)", "3"},
		{"位异或", "eval(5^3)", "6"},
		{"位与", "eval(6&3)", "2"},
		{"左移", "eval(1<<4)", "16"},
		{"右移", "eval(16>>2)", "4"},
		{"比较", "eval(1<2)+eval(2<=1)+eval(3>2)+eval(2>=3)+eval(1==1)+eval(1!=1)", "1+0+1+0+1+0"},
		{"逻辑与或", "eval(1&&0||1)", "1"},
		{"逻辑非", "eval(!0)", "1"},
		{"按位非", "eval(~0)", "-1"},
		{"短路屏蔽除零", "eval(0 && 1/0)", "0"},
		{"短路屏蔽取模零", "eval(1 || 1%0)", "1"},
		{"八进制", "eval(010)", "8"},
		{"十六进制", "eval(0x1f)", "31"},
		{"十六进制大写", "eval(0X1F)", "31"},
		{"回绕", "eval(2147483647+1)", "-2147483648"},
		{"负数补零", "eval(-1, 10, 4)", "-0001"},
		{"十六进制输出", "eval(255, 16)", "FF"},
		{"二进制输出", "eval(255, 2)", "11111111"},
		{"补零宽度", "eval(5, 10, 3)", "005"},
		{"进制一", "eval(5, 1)", "11111"},
		{"负数移位量取模", "eval(16 >> 33)", "8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, t.TempDir(), nil, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("code=%d stdout=%q stderr=%q，期望 %q", result.Code, result.Stdout, result.Stderr, test.want)
			}
		})
	}
}

func TestEvalFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		hint  string
		code  int
	}{
		{"除数为零", "eval(7/0)", "除数为零", 1},
		{"取模数为零", "eval(7%0)", "取模数为零", 1},
		{"表达式不合法", "eval(2-)", "表达式不合法", 1},
		{"尾随垃圾", "eval(1 2)", "表达式不合法", 1},
		{"进制越界", "eval(1, 37)", "进制超出范围", 0},
		{"补零宽度为负", "eval(1, 10, -2)", "补零宽度为负", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, t.TempDir(), nil, test.input)
			if result.Code != test.code || !strings.Contains(result.Stderr, test.hint) {
				t.Fatalf("code=%d stderr=%q，期望码 %d 且含 %q", result.Code, result.Stderr, test.code, test.hint)
			}
		})
	}
}

func TestOptions(t *testing.T) {
	t.Run("定义在输入之前生效", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"-D", "x=hello"}, "x world")
		if result.Code != 0 || result.Stdout != "hello world" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("定义与撤销按出现顺序", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"-D", "x=1", "-U", "x", "-D", "y=2"}, "x y")
		if result.Code != 0 || result.Stdout != "x 2" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("定义只在其后的操作数之前生效", func(t *testing.T) {
		dir := t.TempDir()
		for name, content := range map[string]string{"f1": "[x]", "f2": "[x]"} {
			if err := os.WriteFile(path.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		result := commandtest.Run(t, Run, dir, []string{"f1", "-D", "x=1", "f2"}, "")
		if result.Code != 0 || result.Stdout != "[x][1]" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("定义为空的缺省值", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"-D", "x"}, "[x]")
		if result.Code != 0 || result.Stdout != "[]" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("无效宏名拒绝", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"-D", "1bad=1"}, "")
		if result.Code != 2 {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("行同步如实拒绝", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"-s"}, "x")
		if result.Code != 2 || !strings.Contains(result.Stderr, "行同步") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("未知选项拒绝", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"--nope"}, "")
		if result.Code != 2 {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
}

func TestFilesAndInclude(t *testing.T) {
	t.Run("无操作数读 stdin", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "__file__")
		if result.Code != 0 || result.Stdout != "stdin" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("横线表示 stdin 且只读一次", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"-", "-"}, "ab")
		if result.Code != 0 || result.Stdout != "ab" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("不存在的操作数报错继续", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"nope", "-"}, "ok")
		if result.Code != 1 || result.Stdout != "ok" || !strings.Contains(result.Stderr, "nope") {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("include 展开并可见来源", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(path.Join(dir, "sub.m4"), []byte("define(`w', `W')w __file__"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path.Join(dir, "main.m4"), []byte("include(`sub.m4') __file__"), 0o644); err != nil {
			t.Fatal(err)
		}
		result := commandtest.Run(t, Run, dir, []string{"main.m4"}, "")
		if result.Code != 0 || result.Stdout != "W sub.m4 main.m4" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("__line__ 跟随输入行", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "a\n__line__")
		if result.Code != 0 || result.Stdout != "a\n2" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("sinclude 缺文件静默", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "sinclude(`nope')ok")
		if result.Code != 0 || result.Stdout != "ok" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("include 缺文件终止", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "include(`nope')x")
		if result.Code != 1 || !strings.Contains(result.Stderr, "nope") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("include 递归展开", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(path.Join(dir, "sub.m4"), []byte("define(`greet', `hi')"), 0o644); err != nil {
			t.Fatal(err)
		}
		result := commandtest.Run(t, Run, dir, nil, "include(`sub.m4')greet")
		if result.Code != 0 || result.Stdout != "hi" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
}

func TestSystemMacros(t *testing.T) {
	t.Run("syscmd 透传标准流并记录规格", func(t *testing.T) {
		dir := t.TempDir()
		stub := &controlStub{echo: "hi\n"}
		result := commandtest.RunWithControl(t, Run, dir, nil, "A syscmd(echo hi) B", stub)
		if result.Code != 0 || result.Stdout != "A hi\n B" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
		if len(stub.specs) != 1 {
			t.Fatalf("应有一次子进程调用: %v", stub.specs)
		}
		spec := stub.specs[0]
		if spec.Name != "bash" || len(spec.Args) != 2 || spec.Args[0] != "-c" || spec.Args[1] != "echo hi" {
			t.Fatalf("RunSpec 不符: %+v", spec)
		}
		if spec.Dir != dir {
			t.Fatalf("子进程目录应为工作目录: %q", spec.Dir)
		}
	})
	t.Run("esyscmd 捕获输出作为展开", func(t *testing.T) {
		stub := &controlStub{echo: "CAP"}
		result := commandtest.RunWithControl(t, Run, t.TempDir(), nil, "x=esyscmd(c)y=sysval", stub)
		if result.Code != 0 || result.Stdout != "x=CAPy=0" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("退出码进入 sysval", func(t *testing.T) {
		stub := &controlStub{code: 5}
		result := commandtest.RunWithControl(t, Run, t.TempDir(), nil, "sysval syscmd(c) sysval", stub)
		if result.Code != 0 || result.Stdout != "0  5" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("找不到 bash 按错误 127", func(t *testing.T) {
		stub := &controlStub{err: errno.NotFound}
		result := commandtest.RunWithControl(t, Run, t.TempDir(), nil, "syscmd(c) sysval", stub)
		if result.Code != 0 || result.Stdout != " 127" || !strings.Contains(result.Stderr, "127") {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("无法执行按错误 126", func(t *testing.T) {
		stub := &controlStub{err: errno.NoExec}
		result := commandtest.RunWithControl(t, Run, t.TempDir(), nil, "syscmd(c) sysval", stub)
		if result.Code != 0 || result.Stdout != " 126" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("其他启动失败按错误 127", func(t *testing.T) {
		stub := &controlStub{err: errno.NotSupported}
		result := commandtest.RunWithControl(t, Run, t.TempDir(), nil, "syscmd(c) sysval", stub)
		if result.Code != 0 || result.Stdout != " 127" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("缺少控制端口按失败处理", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "syscmd(c) sysval")
		if result.Code != 0 || result.Stdout != " 127" || !strings.Contains(result.Stderr, "进程控制端口") {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("esyscmd 在启动失败时展开为空", func(t *testing.T) {
		stub := &controlStub{err: errno.NotFound}
		result := commandtest.RunWithControl(t, Run, t.TempDir(), nil, "[esyscmd(c)]", stub)
		if result.Code != 0 || result.Stdout != "[]" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
}

func TestMaketemp(t *testing.T) {
	t.Run("排他创建并展开为路径", func(t *testing.T) {
		dir := t.TempDir()
		result := commandtest.Run(t, Run, dir, nil, "maketemp(mktempXXXXXX)")
		if result.Code != 0 {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
		name := result.Stdout
		if !strings.HasPrefix(name, "mktemp") || len(name) != len("mktemp")+6 {
			t.Fatalf("展开不是模板加六个字符: %q", name)
		}
		if _, err := os.Stat(path.Join(dir, name)); err != nil {
			t.Fatalf("文件应已创建: %v", err)
		}
	})
	t.Run("模板缺 XXXXXX 报错", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "maketemp(mktempXXX)")
		if result.Code != 1 || !strings.Contains(result.Stderr, "XXXXXX") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("缺少随机源报错", func(t *testing.T) {
		result := commandtest.RunWith(t, withoutEntropy(t, Run), t.TempDir(), nil, "maketemp(mktempXXXXXX)", commandtest.Options{})
		if result.Code != 1 || !strings.Contains(result.Stderr, "maketemp") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("创建失败如实报错", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "maketemp(no/such/dir/mktempXXXXXX)")
		if result.Code != 1 || !strings.Contains(result.Stderr, "no/such/dir") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
}

func TestDiagnostics(t *testing.T) {
	t.Run("字符串缺结束引号", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "abc`def")
		if result.Code != 1 || !strings.Contains(result.Stderr, "结束引号") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("参数列表缺右括号", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "define(`f', `')f(1,2")
		if result.Code != 1 || !strings.Contains(result.Stderr, "右括号") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("嵌套过深", func(t *testing.T) {
		input := "define(`f', `')" + strings.Repeat("f(", 5000) + strings.Repeat(")", 5000)
		result := commandtest.Run(t, Run, t.TempDir(), nil, input)
		if result.Code != 1 || !strings.Contains(result.Stderr, "嵌套过深") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("参数太少告警", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "index(a)")
		if result.Code != 0 || !strings.Contains(result.Stderr, "参数太少") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("非数字参数告警", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "divert(x)divnum")
		if result.Code != 0 || result.Stdout != "0" || !strings.Contains(result.Stderr, "不是数字") {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("m4exit 指定退出码", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "a m4exit(7)b")
		if result.Code != 7 || result.Stdout != "a " {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("m4exit 零退出码", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "a m4exit(0)b")
		if result.Code != 0 || result.Stdout != "a " {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("m4exit 非法参数按错误一", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "m4exit(x)")
		if result.Code != 1 || !strings.Contains(result.Stderr, "不是数字") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("errprint 与 dumpdef 进 stderr", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "define(`x', v)errprint(oops)dumpdef(`x')")
		if result.Code != 0 || result.Stdout != "" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
		if !strings.Contains(result.Stderr, "oops") || !strings.Contains(result.Stderr, "x:\tv") {
			t.Fatalf("stderr=%q", result.Stderr)
		}
	})
	t.Run("dumpdef 无参数列出全部且内建带尖括号", func(t *testing.T) {
		result := commandtest.Run(t, Run, t.TempDir(), nil, "define(`z', v)dumpdef")
		if result.Code != 0 {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
		for _, want := range []string{"dnl:\t<dnl>\n", "z:\tv\n", "divert:\t<divert>\n"} {
			if !strings.Contains(result.Stderr, want) {
				t.Fatalf("stderr 缺 %q: %q", want, result.Stderr)
			}
		}
	})
}

func TestCanceledInput(t *testing.T) {
	dir := t.TempDir()
	descriptors := fd.New()
	var stderr strings.Builder
	descriptors.Attach(1, fd.Streams{Input: strings.NewReader(""), Output: new(strings.Builder), Error: &stderr})
	set := capability.Set{FS: filesystem.NewFilesystem(dir), FD: descriptors, Clock: &clock.Virtual{}, Entropy: &entropy.Counter{}}
	ctx, cancel := context.WithCancel(proc.WithContext(context.Background(), proc.Proc{ID: 1, Dir: dir, Resources: set, SID: 1, PGID: 1}))
	cancel()
	p := newProcessorWith(ctx, strings.NewReader(""), new(strings.Builder), &stderr, dir, nil, nil)
	p.src = strings.Repeat("a ", 5000)
	p.srcFile, p.srcLine = "test", 1
	p.file, p.line = "test", 1
	if code := p.process(); code != 1 {
		t.Fatalf("取消后的退出码应为 1，得到 %d", code)
	}
	if !strings.Contains(stderr.String(), "已取消") {
		t.Fatalf("stderr 缺取消诊断: %q", stderr.String())
	}
}

func TestWriteFailure(t *testing.T) {
	dir := t.TempDir()
	descriptors := fd.New()
	var stderr strings.Builder
	descriptors.Attach(1, fd.Streams{Input: strings.NewReader(""), Output: commandtest.FailWriter{}, Error: &stderr})
	set := capability.Set{FS: filesystem.NewFilesystem(dir), FD: descriptors, Clock: &clock.Virtual{}, Entropy: &entropy.Counter{}}
	ctx := proc.WithContext(context.Background(), proc.Proc{ID: 1, Dir: dir, Resources: set, SID: 1, PGID: 1})
	p := newProcessorWith(ctx, strings.NewReader("x"), commandtest.FailWriter{}, &stderr, dir, []string{"-"}, nil)
	if code := p.process(); code != 1 {
		t.Fatalf("写入失败的退出码应为 1，得到 %d", code)
	}
	if !strings.Contains(stderr.String(), "写入失败") {
		t.Fatalf("stderr 缺写入诊断: %q", stderr.String())
	}
}

func TestEvalExprDirect(t *testing.T) {
	for _, test := range []struct {
		expr string
		want int32
		err  string
	}{
		{"", 0, ""},
		{"((((1))))", 1, ""},
		{"1==1", 1, ""},
		{"0x", 0, "表达式不合法"},
		{"08", 0, "表达式不合法"},
		{"'a'", 0, "表达式不合法"},
		{"(", 0, "表达式不合法"},
		{")", 0, "表达式不合法"},
		{"()", 0, "表达式不合法"},
		{"1&", 1, "表达式不合法"},
		{"100<<33", 200, ""},
	} {
		v, got := evalExpr(test.expr)
		if v != test.want || got != test.err {
			t.Fatalf("evalExpr(%q) = (%d, %q)，期望 (%d, %q)", test.expr, v, got, test.want, test.err)
		}
	}
	if _, msg := evalExpr(strings.Repeat("(", 5000) + "1" + strings.Repeat(")", 5000)); msg != "括号嵌套过深" {
		t.Fatalf("深括号应报嵌套过深，得到 %q", msg)
	}
}

func TestMacroOpsValue(t *testing.T) {
	if got := (macroOps{}).Type(); got != "name" {
		t.Fatalf("macroOps.Type() = %q，期望 name", got)
	}
	if got := (macroOps{}).String(); got != "" {
		t.Fatalf("macroOps.String() = %q，期望空", got)
	}
}

func TestValidMacroName(t *testing.T) {
	for _, test := range []struct {
		name string
		ok   bool
	}{
		{"a", true}, {"_", true}, {"a_1", true}, {"", false}, {"1a", false}, {"a-b", false},
	} {
		if got := validMacroName(test.name); got != test.ok {
			t.Fatalf("validMacroName(%q) = %v，期望 %v", test.name, got, test.ok)
		}
	}
}

func TestControlStubContract(t *testing.T) {
	stub := &controlStub{err: errors.New("boom"), echo: "x"}
	if _, err := stub.Run(context.Background(), proc.RunSpec{}); err == nil {
		t.Fatal("应返回注入的错误")
	}
}
