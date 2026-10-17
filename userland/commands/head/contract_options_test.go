package head

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractBytesAndFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("abcdef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
		code  int
	}{
		{[]string{"-c", "3"}, "abcdef", "abc", 0},
		{[]string{"-c", "0"}, "abc", "", 0},
		{[]string{"-c", "3", "data"}, "", "abc", 0},
		{[]string{"-c", "-1"}, "a\nb\n", "a\nb\n", 0},
		{[]string{"-n", "1048577"}, "", "", 2},
		{[]string{"-c", "1048577"}, "", "", 2},
		{[]string{"absent"}, "", "", 2},
		{[]string{"-n", "1", "absent"}, "", "", 2},
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

func TestBytesDownstreamFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-c", "4"}, strings.NewReader("abcd"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
