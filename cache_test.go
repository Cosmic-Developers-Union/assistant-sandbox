package sandbox

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDiskCompilationCacheKeepsCodeButNotShellState(t *testing.T) {
	dir, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	for i := range 2 {
		r, err := newTestRuntime(t.Context(), Config{CompilationCacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		script := "VALUE=private; echo first"
		want := "first\n"
		if i == 1 {
			script = "test -z \"$VALUE\"; echo second"
			want = "second\n"
		}
		result, err := r.Execute(t.Context(), dir, script, &out, &out)
		closeErr := r.Close()
		if err != nil || closeErr != nil || result.ExitCode != 0 || out.String() != want {
			t.Fatal(result, err, closeErr, out.String())
		}
	}
	count := 0
	if err := filepath.WalkDir(cache, func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			count++
		}
		return err
	}); err != nil || count == 0 {
		t.Fatal(count, err)
	}
}

func TestCompilationCacheRejectsUnsafeDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Error(err)
	}
	for _, path := range []string{file, filepath.Join(file, "child")} {
		if r, err := newTestRuntime(t.Context(), Config{CompilationCacheDir: path}); err == nil {
			if err := r.Close(); err != nil {
				t.Error(err)
			}
			t.Fatal("缓存接受非目录")
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	shared := filepath.Join(dir, "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Error(err)
	}
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Error(err)
	}
	if _, err := openCompilationCache(shared); err == nil {
		t.Fatal("接受共享目录")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Error(err)
	}
	if _, err := openCompilationCache(link); err == nil {
		t.Fatal("接受缓存符号链接")
	}
}

func TestCompilationCacheRejectsDamagedVersionDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	cache, err := openCompilationCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	versionDir := filepath.Join(dir, entries[0].Name())
	if err := os.Remove(versionDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionDir, []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openCompilationCache(dir); err == nil {
		t.Fatal("缓存版本目录损坏却未报错")
	}
}

type rejectedShellOutput struct{}

func (rejectedShellOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestSessionOutputFailurePreservesExecutedState(t *testing.T) {
	r, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	if _, err := r.Execute(t.Context(), dir, "VALUE=kept; echo output", rejectedShellOutput{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	var out bytes.Buffer
	result, err := r.Execute(t.Context(), dir, "echo \"$VALUE\"", &out, io.Discard)
	if err != nil || result.ExitCode != 0 || out.String() != "kept\n" {
		t.Fatal(result, err, out.String())
	}
	if _, err := r.Execute(t.Context(), dir, "echo diagnostic >&2", io.Discard, rejectedShellOutput{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	result, err = r.Execute(t.Context(), dir, "exit 7", io.Discard, io.Discard)
	if err != nil || result.ExitCode != 7 || !result.Closed {
		t.Fatal(result, err)
	}
	if _, err := r.Execute(t.Context(), dir, "true", io.Discard, rejectedShellOutput{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("状态重建提示的输出失败被吞掉", err)
	}
}

func TestCompilationCacheCannotBeMountedIntoGuest(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	r, err := newTestRuntime(t.Context(), Config{CompilationCacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := r.Bind(dir); err == nil {
		t.Fatal("允许客体修改机器码缓存")
	}
	if _, err := r.Start(t.Context(), dir, "assistant-bash", nil, nil); err == nil {
		t.Fatal("stdio 客体可以修改机器码缓存")
	}
	if _, err := r.Bind(cache); err == nil {
		t.Fatal("允许直接挂载机器码缓存")
	}
	if _, err := r.Bind(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := validateCacheOutsideWorkspace(dir, filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "workspace")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Bind(link); err == nil {
			t.Fatal("符号链接绕过缓存隔离")
		}
	}
}

func TestRelativeCompilationCacheRemainsBoundAfterHostDirectoryChange(t *testing.T) {
	t.Chdir(t.TempDir())
	work := t.TempDir()
	r, err := newTestRuntime(t.Context(), Config{CompilationCacheDir: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	t.Chdir(t.TempDir())
	result, err := r.Execute(t.Context(), work, "true", io.Discard, io.Discard)
	if err != nil || result.ExitCode != 0 {
		t.Fatal(result, err)
	}
}
