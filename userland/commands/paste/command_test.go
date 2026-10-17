package pastecmd

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"one": "a\nb\n", "two": "1\n2\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
		code  int
	}{
		{[]string{"one", "two"}, "", "a\t1\nb\t2\n", 0},
		{[]string{"-d", ":", "one", "two"}, "", "a:1\nb:2\n", 0},
		{[]string{"-d", ":,", "one", "two"}, "", "a:1\nb:2\n", 0},
		{[]string{"-s", "-d", ":", "one"}, "", "a:b\n", 0},
		{[]string{"-s", "-d", ":", "one", "two"}, "", "a:b\n1:2\n", 0},
		{[]string{"one", "-"}, "x\ny\n", "a\tx\nb\ty\n", 0},
		{[]string{}, "p\nq\n", "p\nq\n", 0},
		{[]string{"absent"}, "", "", 1},
		{[]string{"--bad"}, "", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want code=%d stdout=%q", test.args, result, test.code, test.want)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 {
		t.Fatalf("--help 得到 %+v", result)
	}
}
