package cut

import (
	"io"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args        []string
		input, want string
		code        int
	}{
		{[]string{"-d", ":", "-f", "2"}, "a:2\nno-delimiter\n", "2\nno-delimiter\n", 0},
		{[]string{"-d", ":", "-f", "1,3"}, "a:b\n", "a\n", 0},
		{[]string{"-f", "x"}, "", "", 2},
		{[]string{"-f", "0"}, "", "", 2},
		{[]string{"-d", "xx", "-f", "1"}, "", "", 2},
		{[]string{"--bad"}, "", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v result=%+v want=%q code=%d", test.args, result, test.want, test.code)
		}
	}
}

func TestDownstreamOutputFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-d", ":", "-f", "1"}, strings.NewReader("a:1\nb:2\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
func TestPlainRowOutputFailure(t *testing.T) {
	if err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-f", "1"}, strings.NewReader("plain"), commandtest.FailWriter{}, io.Discard); err == nil {
		t.Fatal("未包含分隔符的行写入失败被忽略")
	}
}

func TestBytesModes(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		in   string
		want string
	}{
		{"单字节", []string{"-b", "1"}, "abcd\n", "a\n"},
		{"区间", []string{"-b", "2-3"}, "abcd\n", "bc\n"},
		{"开放区间", []string{"-b", "3-"}, "abcd\n", "cd\n"},
		{"升序去重（宿主口径）", []string{"-b", "3,1,1"}, "abcd\n", "ac\n"},
		{"越界位置被忽略", []string{"-b", "2-9"}, "ab\n", "b\n"},
		{"-c 与 -b 同义", []string{"-c", "2"}, "abcd\n", "b\n"},
		{"多行", []string{"-b", "1", "-"}, "ab\ncd\n", "a\nc\n"},
		{"末行无换行也补换行（宿主口径）", []string{"-b", "1", "-"}, "ab", "a\n"},
		{"空行保留", []string{"-b", "1", "-"}, "\n\n", "\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, t.TempDir(), test.args, test.in)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%v 得到 code=%d stdout=%q stderr=%q", test.args, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestSuppressAndComplement(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		in   string
		want string
	}{
		{"-s 压掉无分隔符的行", []string{"-s", "-d", ":", "-f", "2", "-"}, "a:b\nnope\nc:d\n", "b\nd\n"},
		{"不给 -s 时原样输出", []string{"-d", ":", "-f", "2", "-"}, "a:b\nnope\n", "b\nnope\n"},
		{"字段取反按输入顺序", []string{"--complement", "-d", ":", "-f", "2", "-"}, "a:b:c\n", "a:c\n"},
		{"字段乱序写法按升序", []string{"-d", ":", "-f", "3,1", "-"}, "a:b:c\n", "a:c\n"},
		{"字段重复写法去重", []string{"-d", ":", "-f", "1,1,2", "-"}, "a:b:c\n", "a:b\n"},
		{"字节取反", []string{"--complement", "-b", "2", "-"}, "abcd\n", "acd\n"},
		{"区间取反", []string{"--complement", "-b", "2-3", "-"}, "abcd\n", "ad\n"},
		{"取反与开放区间", []string{"--complement", "-b", "3-", "-"}, "abcd\n", "ab\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, t.TempDir(), test.args, test.in)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%v 得到 code=%d stdout=%q stderr=%q", test.args, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestModeSelectionErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"-b", "1", "-f", "1", "-"}, {"-b", "1", "-c", "1", "-"}, {"-"}, {"-b", "1", "-f", "1"},
	} {
		if result := commandtest.Run(t, Run, dir, args, "abc\n"); result.Code == 0 {
			t.Fatalf("%v 应当失败: %+v", args, result)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"-n", "-b", "2-3", "-"}, "abcd\n")
	if result.Code != 0 || result.Stdout != "bc\n" {
		t.Fatalf("-n 得到 %+v", result)
	}
}
