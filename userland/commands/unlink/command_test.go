package unlinkcmd

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"victim", "second"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"victim"}, ""); result.Code != 0 {
		t.Fatalf("删除文件: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "victim")); !os.IsNotExist(err) {
		t.Fatalf("文件未删除: %v", err)
	}
	result := commandtest.Run(t, Run, dir, []string{"folder"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("目录: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "folder")); err != nil {
		t.Fatalf("目录被删除: %v", err)
	}
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{}, 2},
		{[]string{"second", "second"}, 2},
		{[]string{"absent"}, 1},
		{[]string{"--bad"}, 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != test.code || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v，want code=%d", test.args, result, test.code)
		}
	}
	result = commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 {
		t.Fatalf("--help: %+v", result)
	}
}
