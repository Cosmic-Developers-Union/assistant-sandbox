package tee

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
		{[]string{}, "kept", "kept", 0},
		{[]string{"saved"}, "kept", "kept", 0},
		{[]string{"-a", "saved"}, "added", "added", 0},
		{[]string{"--bad"}, "", "", 2},
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
func TestFileAndInputFailures(t *testing.T) {
	dir := t.TempDir()
	r := commandtest.Run(t, Run, dir, []string{"missing/file"}, "正文")
	if r.Code != 2 || r.Stdout != "正文" {
		t.Fatal(r)
	}
	r = commandtest.Run(t, Run, dir, nil, strings.Repeat("x", 4<<20+1))
	if r.Code != 2 {
		t.Fatal(r)
	}
}
