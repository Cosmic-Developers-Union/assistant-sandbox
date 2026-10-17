package rmdir

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestParents(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "root", "sub", "deep"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root", "keep"), []byte("k"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-p", "root/sub/deep"}, "")
	if result.Code == 0 || result.Stderr == "" {
		t.Fatalf("非空父目录应报错: %+v", result)
	}
	for _, gone := range []string{"deep", "sub"} {
		if _, err := os.Stat(filepath.Join(dir, "root", gone)); !os.IsNotExist(err) {
			t.Fatalf("%s 应被删除: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "root", "keep")); err != nil {
		t.Fatalf("非空父目录被破坏: %v", err)
	}
}
