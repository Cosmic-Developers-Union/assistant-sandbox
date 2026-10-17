package filesystem

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func TestFilesystemLifecycleAndSymlinkBoundary(t *testing.T) {
	if _, err := NewWorkspace("/work").resolve("/etc/passwd"); !errors.Is(err, fs.ErrPermission) {
		t.Fatal("原生工作区扩大为宿主根目录", err)
	}
	dir := t.TempDir()
	store := NewFilesystem(filepath.ToSlash(dir))
	if err := store.Mkdir(t.Context(), "sub", 0700); err != nil {
		t.Fatal(err)
	}
	file, err := store.Open(t.Context(), "sub/file", vmfs.Create|vmfs.WriteOnly, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if err := store.Rename(t.Context(), "sub/file", "sub/renamed"); err != nil {
		t.Fatal(err)
	}
	file, err = store.Open(t.Context(), "sub/renamed", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if err != nil || string(data) != "value" {
		t.Fatal(string(data), err)
	}
	if info, err := store.Stat(t.Context(), "sub/renamed"); err != nil || info.Size() != 5 {
		t.Fatal(info, err)
	}
	if entries, err := store.ReadDir(t.Context(), "sub"); err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	if err := store.SetTimes(t.Context(), "sub/renamed", 100, 200); err != nil {
		t.Fatal(err)
	}
	if info, err := store.Stat(t.Context(), "sub/renamed"); err != nil || info.ModTime().UnixNano() != 200 {
		t.Fatal(info, err)
	}
	if _, err := store.Stat(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(t.Context(), "sub/renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(t.Context(), "sub/renamed"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("hidden"), 0600); err != nil {
		t.Error(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(t.Context(), "escape/secret", vmfs.ReadOnly, 0); err == nil {
		t.Fatal("符号链接越界")
	}
	for _, name := range []string{"../outside", "/etc/passwd", "bad\x00name", "bad\\name"} {
		if _, err := store.Open(t.Context(), name, 0, 0); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(name, err)
		}
		if _, err := store.Stat(t.Context(), name); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(name, err)
		}
		if _, err := store.ReadDir(t.Context(), name); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(name, err)
		}
		if err := store.Mkdir(t.Context(), name, 0700); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(name, err)
		}
		if err := store.Remove(t.Context(), name); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(name, err)
		}
		if err := store.Rename(t.Context(), name, "sub"); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(name, err)
		}
		if err := store.Rename(t.Context(), "sub", name); !errors.Is(err, fs.ErrPermission) {
			t.Fatal(name, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Open(ctx, "sub", 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, "sub"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := store.ReadDir(ctx, "sub"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, err := range []error{store.Mkdir(ctx, "x", 0700), store.Remove(ctx, "x"), store.Rename(ctx, "x", "y")} {
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if err := store.SetTimes(ctx, "x", 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := store.SetTimes(t.Context(), "../escape", 0, 0); !errors.Is(err, fs.ErrPermission) {
		t.Fatal(err)
	}
	missing := NewFilesystem(filepath.Join(dir, "missing"))
	if _, err := missing.Open(t.Context(), "x", 0, 0); err == nil {
		t.Fatal("缺失根目录")
	}
	if _, err := missing.Stat(t.Context(), "x"); err == nil {
		t.Fatal("缺失根目录")
	}
	if _, err := missing.ReadDir(t.Context(), "x"); err == nil {
		t.Fatal("缺失根目录")
	}
	for _, err := range []error{missing.Mkdir(t.Context(), "x", 0700), missing.Remove(t.Context(), "x"), missing.Rename(t.Context(), "x", "y")} {
		if err == nil {
			t.Fatal("缺失根目录")
		}
	}
}

func TestFilesystemLinkSymlinkReadlinkLstat(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystem(filepath.ToSlash(dir))
	ctx := context.Background()
	file, err := store.Open(ctx, "one", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if err := store.Link(ctx, "one", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	if err := store.Symlink(ctx, "one", "link"); err != nil {
		t.Fatal(err)
	}
	if target, err := store.Readlink(ctx, "link"); err != nil || target != "one" {
		t.Fatalf("target=%q err=%v", target, err)
	}
	info, err := store.Lstat(ctx, "link")
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("info=%v err=%v", info, err)
	}
	if info, err := store.Lstat(ctx, "one"); err != nil || info.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("info=%v err=%v", info, err)
	}

	if _, err := store.Lstat(ctx, "missing"); err == nil {
		t.Fatal("缺失路径应失败")
	}
	if _, err := store.Readlink(ctx, "one"); err == nil {
		t.Fatal("常规文件不应可读链接")
	}
}

func TestFilesystemSetTimesAndRemove(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystem(filepath.ToSlash(dir))
	ctx := context.Background()
	file, err := store.Open(ctx, "timed", vmfs.Create|vmfs.WriteOnly, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if err := store.SetTimes(ctx, "timed", 100, 200); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx, "timed")
	if err != nil || info.ModTime().UnixNano() != 200 {
		t.Fatalf("mtime=%d err=%v", info.ModTime().UnixNano(), err)
	}
	if err := store.Remove(ctx, "timed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, "timed"); err == nil {
		t.Fatal("删除后不应存在")
	}
}

func TestFilesystemChmod(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystem(filepath.ToSlash(dir))
	ctx := context.Background()
	file, err := store.Open(ctx, "script", vmfs.Create|vmfs.WriteOnly, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}

	modes, ok := any(store).(vmfs.Modes)
	if !ok {
		t.Fatal("原生文件系统应当实现 vmfs.Modes")
	}
	if err := modes.Chmod(ctx, "script", fs.ModeSetuid|0o755); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stat(ctx, "script")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 || info.Mode()&fs.ModeSetuid == 0 {
		t.Fatalf("模式位 %v", info.Mode())
	}

	if err := modes.Chmod(ctx, "absent", 0o600); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("缺失路径: %v", err)
	}
	if err := modes.Chmod(ctx, "../escape", 0o600); err == nil {
		t.Fatal("逃逸路径不应成功")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := modes.Chmod(cancelled, "script", 0o600); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消: %v", err)
	}
}

func TestFilesystemAccessTime(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystem(filepath.ToSlash(dir))
	ctx := context.Background()
	file, err := store.Open(ctx, "timed", vmfs.Create|vmfs.WriteOnly, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	times, ok := any(store).(vmfs.AccessTimes)
	if !ok {
		t.Fatal("linux 适配层应当实现 vmfs.AccessTimes")
	}
	const when = 1_600_000_000_000_000_000
	if err := store.SetTimes(ctx, "timed", when, when); err != nil {
		t.Fatal(err)
	}
	if got, err := times.AccessTime(ctx, "timed"); err != nil || got != when {
		t.Fatalf("atime = %d, %v（期望 %d）", got, err, when)
	}
	if _, err := times.AccessTime(ctx, "../escape"); err == nil {
		t.Fatal("逃逸路径不应成功")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := times.AccessTime(cancelled, "timed"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消: %v", err)
	}
}
