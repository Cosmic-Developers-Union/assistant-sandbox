package tail

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
		{[]string{"-n", "1"}, "a\nb\n", "b\n", 0},
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
