package sed

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func runSed(t *testing.T, script, input string) commandtest.Result {
	t.Helper()
	args := []string{script}
	if rest, ok := strings.CutPrefix(script, "-n "); ok {
		args = []string{"-n", rest}
	}
	return commandtest.Run(t, Run, t.TempDir(), args, input)
}

func TestRangeAddresses(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"行号区间", "-n 2,3p", "1\n2\n3\n4\n5\n", "2\n3\n"},
		{"区间到末行", "-n 2,$p", "1\n2\n3\n", "2\n3\n"},
		{"结束行号小于起始只覆盖一行", "-n 3,1p", "1\n2\n3\n4\n", "3\n"},
		{"起止同一行", "-n 2,2p", "1\n2\n3\n", "2\n"},
		{"正则结束地址不在进入行测试", "-n /2/,/2/p", "1\n2\n3\n4\n5\n", "2\n3\n4\n5\n"},
		{"正则区间正常闭合", "-n /2/,/4/p", "1\n2\n3\n4\n5\n", "2\n3\n4\n"},
		{"行号起始加正则结束", "-n 2,/4/p", "1\n2\n3\n4\n5\n", "2\n3\n4\n"},
		{"区间取反", "-n 2,3!p", "1\n2\n3\n4\n", "1\n4\n"},
		{"区间删除", "2,3d", "1\n2\n3\n4\n", "1\n4\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestTextCommands(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"a 在自动打印之后", "a A", "1\n2\n", "1\nA\n2\nA\n"},
		{"a 的 POSIX 反斜杠写法", "a\\A", "1\n", "1\nA\n"},
		{"i 在行之前", "i I", "1\n2\n", "I\n1\nI\n2\n"},
		{"c 单行替换", "c C", "1\n2\n", "C\nC\n"},
		{"-n 下 c 仍然写文本", "-n 2c C", "1\n2\n3\n", "C\n"},
		{"区间 c 只在最后一行写一次", "2,3c C", "1\n2\n3\n4\n", "1\nC\n4\n"},
		{"文本里的分号是字面量", "a x;y", "1\n", "1\nx;y\n"},
		{"带地址的 a", "2a A", "1\n2\n3\n", "1\n2\nA\n3\n"},
		{"c 与 -n 同时只写文本", "-n 1,2c C", "1\n2\n3\n", "C\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}

	result := runSed(t, "a\\\nhello", "1\n")
	if result.Code != 0 || result.Stdout != "1\nhello\n" {
		t.Fatalf("续行形式得到 %+v", result)
	}
}

func TestTransliterate(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"基本映射", "y/abc/xyz/", "abc\n", "xyz\n"},
		{"只映射出现的字节", "y/ab/xy/", "aab\n", "xxy\n"},
		{"自定义定界符", "y|ab|xy|", "ab\n", "xy\n"},
		{"转义", "y/\\t/ /", "a\tb\n", "a b\n"},
		{"带地址的 y", "2y/a/b/", "a\na\n", "a\nb\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}

	if result := runSed(t, "y/ab/x/", "ab\n"); result.Code != 2 || result.Stderr == "" {
		t.Fatalf("长度不同应报错: %+v", result)
	}
}

func TestHoldSpace(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"h 复制到保持空间再 g 取回", "-n h;g;p", "1\n", "1\n"},
		{"x 交换两个空间", "-n 1h;2x;p", "1\n2\n", "1\n1\n"},
		{"H 追加（保持空间先补一个换行）", "-n 1h;2H;2g;p", "1\n2\n", "1\n1\n2\n"},
		{"G 把保持空间接到模式空间之后", "-n 1h;2G;p", "1\n2\n", "1\n2\n1\n"},
		{"经典 H;$!d;x 逆序缓冲", "-n H;$!d;x;p", "1\n2\n3\n", "\n1\n2\n3\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestAddressPrefixDoesNotBreakScriptSplitting(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"带地址的 s 里有分号", "1s/a;b/c/", "a;b\n", "c\n"},
		{"地址正则里有 c", "-n /abc/p", "abc\nxyz\n", "abc\n"},
		{"区间地址里有 c", "-n /abc/,/xyz/p", "abc\nmid\nxyz\ntail\n", "abc\nmid\nxyz\n"},
		{"冒号与分号混用", "s/a/b/;s/b/c/", "a\n", "c\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestExtendedEdgeCases(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"取反地址后的 a", "1!a A", "1\n2\n", "1\n2\nA\n"},
		{"空的 a 文本追加空行", "a", "1\n", "1\n\n"},
		{"区间内的 q 立即结束", "-n 2,3p;2q", "1\n2\n3\n4\n", "2\n"},
		{"未知转义按字面处理", "y/\\q/x/", "q\n", "x\n"},
		{"回车转义", "y/\\r/ /", "a\rb\n", "a b\n"},
		{"冒号开头的标签不是命令", "-n 1p", "1\n", "1\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}

	if result := runSed(t, "y/a\\", "a\n"); result.Code != 2 {
		t.Fatalf("未完成反斜杠应报错: %+v", result)
	}
}
