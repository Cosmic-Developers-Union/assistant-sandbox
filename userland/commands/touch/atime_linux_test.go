//go:build linux

package touch

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func accessTimeOf(t *testing.T, path string) (int64, bool) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return time.Unix(stat.Atim.Sec, stat.Atim.Nsec).Unix(), true
}

func TestModificationTimeKeepsAccessTimeLinux(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinned := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(target, pinned, pinned); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-m", "-t", "202001010000", "keep"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if got, ok := accessTimeOf(t, target); ok && got != pinned.Unix() {
		t.Fatalf("atime 应保持 %v，实际 %v", pinned, time.Unix(got, 0).UTC())
	}
}

func TestAccessTimeActuallyChanges(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinned := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(target, pinned, pinned); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-a", "-t", "202001010000", "keep"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("该平台不暴露 syscall.Stat_t")
	}
	if got := time.Unix(stat.Atim.Sec, stat.Atim.Nsec).Unix(); got != time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("atime = %d (%v)", got, time.Unix(stat.Atim.Sec, stat.Atim.Nsec).UTC())
	}
	if info.ModTime().Unix() != pinned.Unix() {
		t.Fatalf("mtime 应保持不变: %v", info.ModTime().UTC())
	}
}
