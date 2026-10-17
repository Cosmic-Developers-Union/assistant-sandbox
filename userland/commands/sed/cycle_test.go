package sed

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestBranches(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"t 循环替换到不动点", ":x s/a/b/;tx", "aaa\n", "bbb\n"},
		{"b 跳到末尾跳过后续命令", "1b;s/a/b/", "a\na\n", "a\nb\n"},
		{"b 跳到命名标签", "b end;s/a/b/;:end", "a\n", "a\n"},
		{"t 未替换时不跳", "t skip;s/a/b/;:skip", "a\n", "b\n"},
		{"分支回到前面的替换", "s/x/y/;t done;s/y/z/;:done", "x\n", "y\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestMultilineCommands(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"N 把下一行接进来", "N;s/\\n/-/", "1\n2\n3\n", "1-2\n3\n"},
		{"P 只打印第一行", "-n N;P", "1\n2\n", "1\n"},
		{"P 对单行模式空间打印整行", "-n P", "1\n", "1\n"},
		{"D 删除第一行后重跑脚本", "N;N;P;D", "a\nb\nc\n", "a\nb\nc\n"},
		{"n 之后继续执行后面的命令", "-n s/a/A/;n;p", "a\nb\n", "b\n"},
		{"N 到输入末尾时结束周期", "-n N;P", "1\n2\n3\n", "1\n"},
		{"末行的 N 被地址挡住", "-n $!N;p", "1\n2\n3\n", "1\n2\n3\n"},
		{"经典连接：非末行合并", "$!N;s/\\n/ /", "1\n2\n3\n", "1 2\n3\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestBlocks(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"地址匹配才执行块", "-n 2{p;p}", "1\n2\n3\n", "2\n2\n"},
		{"地址不匹配整块跳过", "-n 1{p;p}", "1\n2\n", "1\n1\n"},
		{"嵌套块", "-n 1{2{p}}", "1\n2\n", ""},
		{"块内分支", "-n /2/{p;b};p", "1\n2\n3\n", "1\n2\n3\n"},
		{"取反地址的块", "-n 1!{p}", "1\n2\n", "2\n"},
		{"区间地址的块", "-n 2,3{p}", "1\n2\n3\n4\n", "2\n3\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestAppendedTextIsFlushedOnEveryExitPath(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"d 之后仍写", "2{a A\nd}", "1\n2\n", "1\nA\n"},
		{"q 之前仍写", "a A\nq", "1\n", "1\nA\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestDollarSpansWholeStream(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("1\n2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("3\n4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-n", "$p", "a", "b"}, "")
	if result.Code != 0 || result.Stdout != "4\n" {
		t.Fatalf("多文件 $p 得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-n", "3p", "a", "b"}, "")
	if result.Code != 0 || result.Stdout != "3\n" {
		t.Fatalf("多文件行号得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-n", "$!N;s/\\n/+/;p", "a", "b"}, "")
	if result.Code != 0 || result.Stdout != "1+2\n3+4\n" {
		t.Fatalf("跨文件 N 得到 %+v", result)
	}
}
