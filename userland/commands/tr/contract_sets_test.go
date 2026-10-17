package tr

import (
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestEscapedSets(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{`\n`, "x"}, "\n", "x"},
		{[]string{`\t`, "x"}, "\t", "x"},
		{[]string{`\r`, "x"}, "\r", "x"},
		{[]string{`\\`, "x"}, "\\", "x"},
		{[]string{`\101`, "x"}, "A", "x"},
		{[]string{"a-c", "x-z"}, "abc", "xyz"},
		{[]string{"a-c", "xy"}, "abc", "xyy"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
	for _, args := range [][]string{{`\q`, "x"}, {"z-a", "x"}} {
		result := commandtest.Run(t, Run, dir, args, "x")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestComplementAndSqueeze(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-c", "a-z", "A-Z"}, "abc123", "ABC123"},
		{[]string{"-s", "a-z", "a-z"}, "aabbcc", "abc"},
		{[]string{"-d", "-s", "0-9"}, "a1b22", "ab"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
}
