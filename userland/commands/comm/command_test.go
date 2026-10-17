package commcmd

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"one":  "a\nb\nc\n",
		"two":  "b\nc\nd\n",
		"only": "x\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{"one", "two"}, "a\n\t\tb\n\t\tc\n\td\n", 0},
		{[]string{"-1", "one", "two"}, "\tb\n\tc\nd\n", 0},
		{[]string{"-12", "one", "two"}, "b\nc\n", 0},
		{[]string{"-3", "one", "two"}, "a\n\td\n", 0},
		{[]string{"--total", "one", "two"}, "a\n\t\tb\n\t\tc\n\td\n1\t1\t2\t4 total\n", 0},
		{[]string{"one", "only"}, "a\nb\nc\n\tx\n", 0},
		{[]string{"one"}, "", 2},
		{[]string{"one", "two", "only"}, "", 2},
		{[]string{"-", "-"}, "", 2},
		{[]string{"one", "absent"}, "", 1},
		{[]string{"--bad", "one", "two"}, "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want code=%d stdout=%q", test.args, result, test.code, test.want)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 {
		t.Fatalf("--help 得到 %+v", result)
	}
}
