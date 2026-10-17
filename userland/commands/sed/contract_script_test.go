package sed

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractScripts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("a\nb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
	}{
		{"print-last", []string{"-n", "$p"}, "a\nb\nc\n", "c\n"},
		{"print-regex", []string{"-n", "/b/p"}, "a\nb\nc\n", "b\n"},
		{"negate", []string{"/b/!d"}, "a\nb\nc\n", "b\n"},
		{"quit", []string{"1q"}, "a\nb\n", "a\n"},
		{"line-address", []string{"2d"}, "a\nb\nc\n", "a\nc\n"},
		{"last-address", []string{"$d"}, "a\nb\nc\n", "a\nb\n"},
		{"equals", []string{"-n", "="}, "a\nb\n", "1\n2\n"},
		{"global", []string{"s/a/X/g"}, "aaa\n", "XXX\n"},
		{"print-flag", []string{"-n", "s/a/b/p"}, "a\nb\n", "b\n"},
		{"insensitive", []string{"s/A/b/I"}, "a\n", "b\n"},
		{"group", []string{`s/\(a\)b/[\1]/`}, "ab\n", "[a]\n"},
		{"bre-plus", []string{`s/a\+/X/`}, "aaa\n", "X\n"},
		{"whole-match", []string{"s/a/[&]/"}, "a\n", "[a]\n"},
		{"literal-amp", []string{`s/a/[\&]/`}, "a\n", "[&]\n"},
		{"escaped-delimiter", []string{`s/a\/b/x/`}, "a/b\n", "x\n"},
		{"semicolon-chain", []string{"s/a/b/;s/b/c/"}, "a\n", "c\n"},
		{"newline-chain", []string{"-e", "s/a/b/\ns/b/c/"}, "a\n", "c\n"},
		{"comment", []string{"# 注释\ns/a/b/"}, "a\n", "b\n"},
		{"script-from-operand", []string{"s/a/b/", "data"}, "", "b\nb\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("%s: args=%v 得到 %+v，want %q", test.name, test.args, result, test.want)
		}
	}
}

func TestContractScriptErrors(t *testing.T) {
	dir := t.TempDir()

	for _, script := range []string{
		"", "}", "{", "b missing", "t missing", ": ", "2,3:p", "p1", "s", "s/a/b",
		"s//x/", "s/a[/x/", "s/a/b/z", "/[/p", "1-2p", "1,2,3p", "1,", "y/ab/c/",
	} {
		args := []string{script}
		if script == "" {
			args = []string{"-e", ""}
		}
		result := commandtest.Run(t, Run, dir, args, "a\n")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("script=%q 得到 %+v", script, result)
		}
	}
}

func TestEmptyScriptOperand(t *testing.T) {
	result := commandtest.Run(t, Run, t.TempDir(), []string{"-e", "  "}, "a\n")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("空白脚本应被拒绝: %+v", result)
	}
}
