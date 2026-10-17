package ln

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"a", "b"}, ""); result.Code != 0 {
		t.Fatalf("硬链接: %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(dir, "b"))
	if err != nil || string(content) != "value" {
		t.Fatalf("硬链接内容: %q %v", content, err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-s", "a", "c"}, ""); result.Code != 0 {
		t.Fatalf("符号链接: %+v", result)
	}
	info, err := os.Lstat(filepath.Join(dir, "c"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("符号链接类型: %v %v", info, err)
	}
	target, err := os.Readlink(filepath.Join(dir, "c"))
	if err != nil || target != "a" {
		t.Fatalf("符号链接目标: %q %v", target, err)
	}
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"a"}, 2},
		{[]string{"a", "b", "c"}, 2},
		{[]string{"-f", "a", "b"}, 0},
		{[]string{"--bad", "a", "b"}, 2},
		{[]string{"--help"}, 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != test.code {
			t.Fatalf("args=%v 得到 %+v，want code=%d", test.args, result, test.code)
		}
	}
}
