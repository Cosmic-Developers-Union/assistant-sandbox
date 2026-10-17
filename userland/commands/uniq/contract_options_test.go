package uniq

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractSelection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("a\na\nb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-c"}, "a\na\nb\n", "2 a\n1 b\n"},
		{[]string{"-d"}, "a\na\nb\n", "a\n"},
		{[]string{"-u"}, "a\na\nb\n", "b\n"},
		{[]string{"-i"}, "A\na\n", "A\n"},
		{[]string{"-f", "1"}, "x a\ny a\n", "x a\n"},
		{[]string{"-f", "9"}, "x a\n", "x a\n"},
		{[]string{"-s", "1"}, "xa\nya\n", "xa\n"},
		{[]string{"-s", "9"}, "ab\n", "ab\n"},
		{[]string{"in"}, "", "a\nb\n"},
		{[]string{"-c", "in"}, "", "2 a\n1 b\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
	for _, args := range [][]string{
		{"-d", "-u"}, {"-f", "-1"}, {"a", "b", "c"}, {"--bad"}, {"absent"}, {"in", "sub/out"},
	} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestOutputOperand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("a\na\nb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"in", "out"}, "")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("得到 %+v", result)
	}
	written, err := os.ReadFile(filepath.Join(dir, "out"))
	if err != nil || string(written) != "a\nb\n" {
		t.Fatalf("输出文件 %q %v", written, err)
	}
}

func TestDownstreamFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-c"}, strings.NewReader("a\na\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
