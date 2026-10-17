package head

import (
	"io"
	"os"
	"path/filepath"
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
		{[]string{"-n", "1"}, "a\nb\n", "a\n", 0},
		{[]string{"-n", "0"}, "a\n", "", 0},
		{[]string{"-n", "-1"}, "", "", 2},
		{[]string{}, "last", "last", 0},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v result=%+v want=%q code=%d", test.args, result, test.want, test.code)
		}
	}
}

func TestDownstreamOutputFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{}, strings.NewReader("a:1\nb:2\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}

func TestMultipleInputsResetLineBudget(t *testing.T) {
	dir := t.TempDir()
	for _, file := range []struct {
		name, content string
	}{{"a.txt", "a1\na2\na3\n"}, {"b.txt", "b1\nb2\nb3\n"}} {
		if err := os.WriteFile(filepath.Join(dir, file.name), []byte(file.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"-n", "2", "a.txt", "b.txt"}, "")
	if result.Code != 0 || result.Stdout != "a1\na2\nb1\nb2\n" {
		t.Fatalf("多文件行数得到 %+v", result)
	}
}

func TestBytesTakeBoundedPrefix(t *testing.T) {
	result := commandtest.Run(t, Run, t.TempDir(), []string{"-c", "1"}, strings.Repeat("x", 4<<20+2))
	if result.Code != 0 || result.Stdout != "x" {
		t.Fatalf("大输入取 1 字节得到 %+v", result)
	}

	dir := t.TempDir()
	for _, file := range []struct {
		name, content string
	}{{"a.txt", "abcdef"}, {"b.txt", "ghij"}} {
		if err := os.WriteFile(filepath.Join(dir, file.name), []byte(file.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result = commandtest.Run(t, Run, dir, []string{"-c", "2", "a.txt", "b.txt"}, "")
	if result.Code != 0 || result.Stdout != "abgh" {
		t.Fatalf("-c 多文件应各取前两字节: %+v", result)
	}
}
