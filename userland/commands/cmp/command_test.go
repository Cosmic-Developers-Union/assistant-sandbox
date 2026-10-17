package cmp

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"a": "alpha\n", "b": "alpha\n", "c": "beta\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"a", "b"}, 0},
		{[]string{"a", "c"}, 1},
		{[]string{"-s", "a", "c"}, 1},
		{[]string{"a"}, 2},
		{[]string{"a", "b", "c"}, 2},
		{[]string{"missing", "a"}, 2},
		{[]string{"--bad", "a", "b"}, 2},
		{[]string{"--help"}, 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != test.code {
			t.Fatalf("args=%v 得到 %+v，want code=%d", test.args, result, test.code)
		}
		if test.code == 2 && result.Stderr == "" {
			t.Fatalf("args=%v 报错却没有诊断: %+v", test.args, result)
		}
	}
}
