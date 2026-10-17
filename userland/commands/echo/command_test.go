package echocmd

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"hello", "world"}, "hello world\n"},
		{[]string{"-n", "x"}, "x"},
		{[]string{"-e", `a\tb`}, "a\tb\n"},
		{[]string{"-E", `a\tb`}, "a\\tb\n"},
		{[]string{"-ne", "x"}, "x"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help: %+v", result)
	}
}
