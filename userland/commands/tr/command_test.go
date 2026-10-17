package tr

import (
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"a-z", "A-Z"}, "abc\n", "ABC\n"},
		{[]string{"-d", "0-9"}, "a1b2", "ab"},
		{[]string{"-s", "a-z", "a-z"}, "aabb", "ab"},
		{[]string{"abc", "xy"}, "abc", "xyy"},
		{[]string{"-c", "-d", "a-z"}, "ab1", "ab"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
	for _, args := range [][]string{{}, {"a"}, {"-d", "a", "b"}, {"--bad"}} {
		result := commandtest.Run(t, Run, dir, args, "x")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
	if result := commandtest.Run(t, Run, dir, []string{"--help"}, ""); result.Code != 2 {
		t.Fatalf("--help: %+v", result)
	}
}
