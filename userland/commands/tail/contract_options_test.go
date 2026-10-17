package tail

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractWindows(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("a\nb\nc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
		code  int
	}{
		{[]string{"-n", "1"}, "a\nb\n", "b\n", 0},
		{[]string{"-n", "0"}, "a\n", "", 0},
		{[]string{"-n", "1"}, "a\nb", "b", 0},
		{[]string{"-n", "1", "data"}, "", "c\n", 0},
		{[]string{"-n", "+2"}, "a\nb\nc\n", "b\nc\n", 0},
		{[]string{"-n", "+9"}, "a\n", "", 0},
		{[]string{"-c", "2"}, "abcd", "cd", 0},
		{[]string{"-c", "0"}, "abc", "", 0},
		{[]string{"-c", "2", "data"}, "", "c\n", 0},
		{[]string{"-n", "x"}, "", "", 2},
		{[]string{"-n", "-1"}, "", "", 2},
		{[]string{"-n", "+0"}, "", "", 2},
		{[]string{"-c", "1048577"}, "", "", 2},
		{[]string{"absent"}, "", "", 2},
		{[]string{"--bad"}, "", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want code=%d stdout=%q", test.args, result, test.code, test.want)
		}
		if test.code != 0 && result.Stderr == "" {
			t.Fatalf("args=%v 报错却没有诊断: %+v", test.args, result)
		}
	}
}

func TestDownstreamFailure(t *testing.T) {
	for _, args := range [][]string{{"-n", "1"}, {"-c", "2"}} {
		err := commandtest.Invoke(t.Context(), Run, t.TempDir(), args, strings.NewReader("abcd"), commandtest.FailWriter{}, io.Discard)
		if err == nil {
			t.Fatalf("args=%v 输出失败被忽略", args)
		}
	}
}
