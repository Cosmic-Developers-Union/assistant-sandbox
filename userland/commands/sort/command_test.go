package sort

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
		{[]string{"-n"}, "10\n2\n1\n", "1\n2\n10\n", 0},
		{[]string{"-ru"}, "b\na\na\n", "b\na\n", 0},
		{[]string{}, "", "", 0},
		{[]string{"--bad"}, "", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v result=%+v want=%q code=%d", test.args, result, test.want, test.code)
		}
	}
}

func TestDownstreamOutputFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-n"}, strings.NewReader("a:1\nb:2\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
