package toolcompat

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func newContext(out, diagnostic io.Writer) *RunContext {
	return &RunContext{Dir: "/work", Env: []string{"LANG=C"}, Stdio: Stdio{Out: out, Err: diagnostic}}
}

func TestRegisterAndLookup(t *testing.T) {
	tool := &Tool{Name: "toolcompat-test-alpha", Synopsis: "syn", Usage: "alpha", Run: func(*RunContext, []string) int { return 0 }}
	Register(tool)
	found, ok := Lookup(tool.Name)
	if !ok || found != tool {
		t.Fatalf("Lookup 未取回登记的命令: %v %v", found, ok)
	}
	if _, ok := Lookup("toolcompat-test-absent"); ok {
		t.Fatal("未登记的命令不应被找到")
	}
	for name, candidate := range map[string]*Tool{"空名": {}, "nil": nil} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s 应 panic", name)
				}
			}()
			Register(candidate)
		}()
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("重复登记应 panic")
			}
		}()
		Register(&Tool{Name: tool.Name})
	}()
}

func TestRunContextPathAndEnv(t *testing.T) {
	rc := newContext(io.Discard, io.Discard)
	if got := rc.Path("rel"); got != "/work/rel" {
		t.Fatalf("相对路径解析错误: %q", got)
	}
	if got := rc.Path("/abs"); got != "/abs" {
		t.Fatalf("绝对路径不应改写: %q", got)
	}
	if rc.Getenv("LANG") != "C" || rc.Getenv("MISSING") != "" {
		t.Fatal("环境查询错误")
	}
}

func TestParseAndUniversalFlags(t *testing.T) {
	cmd := &Tool{Name: "toolcompat-test-beta", Synopsis: "syn", Usage: "beta [OPTION]... [FILE]..."}
	var out, diagnostic bytes.Buffer
	rc := newContext(&out, &diagnostic)

	fs := NewFlags(cmd.Name)
	verbose := fs.BoolP("verbose", "v", false, "详细")
	number := fs.IntP("number", "n", 0, "编号")
	operands, code := Parse(rc, cmd, fs, []string{"-vn", "3", "file"})
	if code != -1 || len(operands) != 1 || operands[0] != "file" || !*verbose || *number != 3 {
		t.Fatalf("解析得到 code=%d operands=%v verbose=%v number=%d", code, operands, *verbose, *number)
	}

	fs = NewFlags(cmd.Name)
	fs.IntP("number", "n", 0, "编号")
	if _, code := Parse(rc, cmd, fs, []string{"--numb", "7"}); code != -1 {
		t.Fatalf("长选项前缀展开失败: code=%d", code)
	} else if got, _ := fs.GetInt("number"); got != 7 {
		t.Fatalf("前缀展开取值错误: %d", got)
	}
	diagnostic.Reset()
	if _, code := Parse(rc, cmd, fs, []string{"--unknown-flag"}); code != 2 || !strings.Contains(diagnostic.String(), "not every GNU flag") {
		t.Fatalf("未知参数应报 2: code=%d stderr=%q", code, diagnostic.String())
	}

	fs = NewFlags(cmd.Name)
	fs.Bool("alpha-one", false, "一")
	fs.Bool("alpha-two", false, "二")
	diagnostic.Reset()
	if _, code := Parse(rc, cmd, fs, []string{"--alpha"}); code != 2 || !strings.Contains(diagnostic.String(), "ambiguous") {
		t.Fatalf("歧义前缀应报 2: code=%d stderr=%q", code, diagnostic.String())
	}

	out.Reset()
	fs = NewFlags(cmd.Name)
	if _, code := Parse(rc, cmd, fs, []string{"--help"}); code != 0 || !strings.Contains(out.String(), "Usage") {
		t.Fatalf("--help 得到 code=%d out=%q", code, out.String())
	}
	out.Reset()
	fs = NewFlags(cmd.Name)
	if _, code := Parse(rc, cmd, fs, []string{"--version"}); code != 0 || !strings.Contains(out.String(), Version) {
		t.Fatalf("--version 得到 code=%d out=%q", code, out.String())
	}

	fs = NewFlags(cmd.Name)
	fs.BoolP("verbose", "v", false, "详细")
	operands, code = ParseRequireOrder(rc, cmd, fs, []string{"-v", "file", "-v"})
	if code != -1 || len(operands) != 2 || operands[1] != "-v" {
		t.Fatalf("ParseRequireOrder 得到 code=%d operands=%v", code, operands)
	}
}

func TestHelpVersionAliases(t *testing.T) {
	cmd := &Tool{Name: "toolcompat-test-gamma", Synopsis: "syn", Usage: "gamma"}
	var out, diagnostic bytes.Buffer
	rc := newContext(&out, &diagnostic)
	fs := NewFlags(cmd.Name)
	if _, code := Parse(rc, cmd, fs, []string{"-h"}); code != 0 || !strings.Contains(out.String(), "Usage") {
		t.Fatalf("-h 别名得到 code=%d out=%q", code, out.String())
	}
	out.Reset()
	fs = NewFlags(cmd.Name)
	if _, code := Parse(rc, cmd, fs, []string{"-V"}); code != 0 {
		t.Fatalf("-V 别名得到 code=%d", code)
	}

	fs = NewFlags(cmd.Name)
	hardlink := fs.BoolP("human", "h", false, "命令自有短选项")
	if _, code := Parse(rc, cmd, fs, []string{"-h"}); code != -1 || !*hardlink {
		t.Fatalf("自有 -h 被别名覆盖: code=%d", code)
	}

	for _, test := range []struct {
		args []string
		want []string
	}{
		{[]string{"-h"}, []string{"--help"}},
		{[]string{"-V"}, []string{"--version"}},
		{[]string{"-Vh"}, []string{"--version", "--help"}},
		{[]string{"-nx"}, []string{"-nx"}},
		{[]string{"--", "-h"}, []string{"--", "-h"}},
		{[]string{"-x", "-h"}, []string{"-x", "--help"}},
	} {
		if got := AliasHelpVersion(test.args); strings.Join(got, " ") != strings.Join(test.want, " ") {
			t.Fatalf("AliasHelpVersion(%v)=%v，want %v", test.args, got, test.want)
		}
	}
}

func TestDiagnosticsHelpers(t *testing.T) {
	cmd := &Tool{Name: "toolcompat-test-delta", Synopsis: "syn", Usage: "delta"}
	var out, diagnostic bytes.Buffer
	rc := newContext(&out, &diagnostic)
	if code := UsageError(rc, cmd, "missing operand"); code != 2 || !strings.Contains(diagnostic.String(), "missing operand") {
		t.Fatalf("UsageError 得到 code=%d stderr=%q", code, diagnostic.String())
	}
	diagnostic.Reset()
	if code := NotSupported(rc, cmd, "-Z"); code != 2 || !strings.Contains(diagnostic.String(), "-Z") {
		t.Fatalf("NotSupported 得到 code=%d stderr=%q", code, diagnostic.String())
	}

	underlying := errors.New("底层失败")
	if !errors.Is(SysErr(underlying), underlying) {
		t.Fatal("SysErr 应原样返回")
	}
	if SysErrString(nil) != "" || SysErrString(underlying) != "底层失败" {
		t.Fatal("SysErrString 错误")
	}
	if !strings.Contains(fmt.Sprint(underlying), "底层失败") {
		t.Fatal("测试自身断言错误")
	}
}

func TestFlagValueHelpers(t *testing.T) {
	fs := pflag.NewFlagSet("helper", pflag.ContinueOnError)
	fs.Bool("bool", false, "布尔")
	fs.String("text", "", "文本")
	if flagTakesValue(fs.Lookup("bool")) {
		t.Fatal("布尔参数不取值")
	}
	if !flagTakesValue(fs.Lookup("text")) {
		t.Fatal("字符串参数取值")
	}
	if flagTakesValue(nil) {
		t.Fatal("nil 不取值")
	}
	if !flagBool(fs, "bool") && flagBool(fs, "missing") {
		t.Fatal("flagBool 语义错误")
	}
}
