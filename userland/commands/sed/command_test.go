package sed

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
		{[]string{"s/a/b/"}, "a\n", "b\n"},
		{[]string{"-e", "s/a/b/", "-e", "s/b/c/"}, "a\n", "c\n"},
		{[]string{"-n", "2p"}, "x\ny\n", "y\n"},
		{[]string{"1d"}, "x\ny\n", "y\n"},
		{[]string{"="}, "x\n", "1\nx\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
	for _, args := range [][]string{{}, {"--bad", "s/a/b/"}, {"s/[a/"}} {
		result := commandtest.Run(t, Run, dir, args, "x\n")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}
