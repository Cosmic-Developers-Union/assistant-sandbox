package cp

import (
	"syscall"
	"time"

	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"data", "copied"}, "")
	if result.Code != 0 {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(dir, "copied")); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "copied"))
	if string(body) != "alpha\n" {
		t.Fatal(string(body))
	}
	result = commandtest.Run(t, Run, dir, []string{"--assistant-invalid-flag"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatal(result)
	}
}

func TestPreserveAttributes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(source, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dir, "dst")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-p", "src", "dst"}, ""); result.Code != 0 {
		t.Fatalf("cp -p 覆盖已存在目标: %+v", result)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("模式 = %o，期望 0600", info.Mode().Perm())
	}
	if info.ModTime().Unix() != stamp.Unix() {
		t.Fatalf("mtime = %v，期望 %v", info.ModTime().UTC(), stamp)
	}

	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if got := time.Unix(stat.Atim.Sec, stat.Atim.Nsec).Unix(); got != stamp.Unix() {
			t.Fatalf("atime = %d，期望 %d", got, stamp.Unix())
		}
	}
}
