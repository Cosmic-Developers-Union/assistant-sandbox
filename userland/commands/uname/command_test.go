package uname

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
		{nil, "Linux\n"},
		{[]string{"-s"}, "Linux\n"},
		{[]string{"-m"}, "riscv64\n"},
		{[]string{"-r"}, "sandbox\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"-a"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "riscv64") || !strings.Contains(result.Stdout, "assistant") {
		t.Fatalf("-a: %+v", result)
	}
	for _, args := range [][]string{{"operand"}, {"--bad"}, {"--help"}} {
		result = commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}
