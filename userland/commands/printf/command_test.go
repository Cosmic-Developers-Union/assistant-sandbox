package printfcmd

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
		{[]string{"%s-%d\n", "x", "7"}, "x-7\n"},
		{[]string{"%s\n", "a", "b"}, "a\nb\n"},
		{[]string{"\\101\\n"}, "A\n"},
		{[]string{"%5.2f\n", "1.5"}, " 1.50\n"},
		{[]string{"--", "-literal"}, "-literal"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
	for _, args := range [][]string{{}, {"%d\n", "notanumber"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help: %+v", result)
	}
}
