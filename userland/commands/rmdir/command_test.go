package rmdir

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "full"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "full", "child"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"empty"}, ""); result.Code != 0 {
		t.Fatalf("空目录: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "empty")); !os.IsNotExist(err) {
		t.Fatalf("空目录未删除: %v", err)
	}
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"full"}, 2},
		{[]string{"absent"}, 2},
		{[]string{}, 2},
		{[]string{"--bad"}, 2},
		{[]string{"--help"}, 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != test.code || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v，want code=%d", test.args, result, test.code)
		}
	}
}
