package wc

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractCounters(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"f1": "a\n", "f2": "b\n", "utf8": "héllo"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-l"}, "a b\nc\n", "2\n"},
		{[]string{"-w"}, "a b\nc\n", "3\n"},
		{[]string{"-c"}, "abc", "3\n"},
		{[]string{"-m"}, "héllo", "5\n"},
		{[]string{"-mc"}, "héllo", "5 6\n"},
		{[]string{"-lw", "f1", "f2"}, "", "1 1 f1\n1 1 f2\n2 2 total\n"},
		{[]string{"-c", "f1", "f2"}, "", "2 f1\n2 f2\n4 total\n"},
		{[]string{"-c", "utf8"}, "", "6 utf8\n"},
		{nil, "a b\nc\n", "2 3 6\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
}

func TestContractErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"absent"}, {"-l", "absent"}, {"--bad"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestDownstreamFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-l"}, strings.NewReader("a\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
