package mountfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func readFile(t *testing.T, system vmfs.System, name string) string {
	t.Helper()
	file, err := system.Open(context.Background(), name, vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func parentOf(name string) string {
	for index := len(name) - 1; index > 0; index-- {
		if name[index] == '/' {
			return name[:index]
		}
	}
	return "/"
}

func newFS(t *testing.T) *memfs.FS {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return system
}

func writeFile(t *testing.T, system vmfs.System, name, content string) {
	t.Helper()
	if dir := parentOf(name); dir != "/" {
		if err := system.Mkdir(context.Background(), dir, 0o755); err != nil && !errors.Is(err, errno.Exist) {
			t.Fatal(err)
		}
	}
	file, err := system.Open(context.Background(), name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func mountTwo(t *testing.T) (*Mounts, *memfs.FS, *memfs.FS) {
	t.Helper()
	root, extra := newFS(t), newFS(t)
	mounts := &Mounts{}
	if err := mounts.Mount("/", root); err != nil {
		t.Fatal(err)
	}
	if err := mounts.Mount("/mnt", extra); err != nil {
		t.Fatal(err)
	}
	return mounts, root, extra
}

func TestCrossMountRenameIsRejected(t *testing.T) {
	ctx := context.Background()
	mounts, root, extra := mountTwo(t)
	writeFile(t, root, "/a.txt", "root-data")
	writeFile(t, extra, "/mnt/b.txt", "extra-data")

	writeFile(t, mounts, "/src.txt", "payload")
	if err := mounts.Mkdir(ctx, "/mnt/dst", 0o755); err != nil {
		t.Fatal(err)
	}
	err := mounts.Rename(ctx, "/src.txt", "/mnt/dst/moved.txt")
	if !errors.Is(err, errno.CrossDevice) {
		t.Fatalf("跨挂载改名应报 EXDEV: %v", err)
	}

	if _, statErr := mounts.Stat(ctx, "/src.txt"); statErr != nil {
		t.Fatalf("被拒绝的改名不得移动源文件: %v", statErr)
	}
	if _, statErr := mounts.Stat(ctx, "/mnt/dst/moved.txt"); !errors.Is(statErr, errno.NotFound) {
		t.Fatalf("被拒绝的改名不得产生目标: %v", statErr)
	}
}

func TestCrossMountLinkIsRejected(t *testing.T) {
	ctx := context.Background()
	mounts, _, _ := mountTwo(t)
	writeFile(t, mounts, "/target.txt", "payload")
	if err := mounts.Mkdir(ctx, "/mnt/links", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mounts.Link(ctx, "/target.txt", "/mnt/links/hard.txt"); !errors.Is(err, errno.CrossDevice) {
		t.Fatalf("跨挂载硬链接应报 EXDEV: %v", err)
	}
}

func TestSameMountRenameWorks(t *testing.T) {
	ctx := context.Background()
	mounts, _, _ := mountTwo(t)
	writeFile(t, mounts, "/mnt/inner/old.txt", "content")
	if err := mounts.Rename(ctx, "/mnt/inner/old.txt", "/mnt/inner/new.txt"); err != nil {
		t.Fatalf("同挂载改名应成功: %v", err)
	}
	file, err := mounts.Open(ctx, "/mnt/inner/new.txt", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, []byte("content")) {
		t.Fatalf("内容=%q", content)
	}

	if _, err := mounts.Stat(ctx, "/mnt/inner/old.txt"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("改名的旧路径应消失: %v", err)
	}
}

func TestUnmountedPathIsInvisible(t *testing.T) {
	ctx := context.Background()
	mounts := &Mounts{}
	extra := newFS(t)
	if err := mounts.Mount("/opt", extra); err != nil {
		t.Fatal(err)
	}
	if _, err := mounts.Stat(ctx, "/etc/passwd"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未挂载路径应报 ENOENT: %v", err)
	}
	if _, err := mounts.Open(ctx, "/etc/passwd", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未挂载路径打开应报 ENOENT: %v", err)
	}
	if err := mounts.Rename(ctx, "/a", "/b"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未挂载路径改名应报 ENOENT: %v", err)
	}
	if err := mounts.Mkdir(ctx, "relative", 0o755); !errors.Is(err, errno.Invalid) {
		t.Fatalf("相对路径应报 EINVAL: %v", err)
	}
}

func TestLongestPrefixWins(t *testing.T) {
	ctx := context.Background()
	outer, inner := newFS(t), newFS(t)
	mounts := &Mounts{}
	if err := mounts.Mount("/a", outer); err != nil {
		t.Fatal(err)
	}
	if err := mounts.Mount("/a/b", inner); err != nil {
		t.Fatal(err)
	}

	writeFile(t, mounts, "/a/b/inner.txt", "inner")
	writeFile(t, mounts, "/a/outer/x.txt", "outer")

	content := readFile(t, mounts, "/a/b/inner.txt")
	if content != "inner" {
		t.Fatalf("内层内容=%q", content)
	}

	content = readFile(t, mounts, "/a/outer/x.txt")
	if content != "outer" {
		t.Fatalf("外层内容=%q", content)
	}

	writeFile(t, mounts, "/a/b/only-inner.txt", "x")
	if _, err := mounts.Stat(ctx, "/a/only-inner.txt"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("内层文件不应出现在外层的挂载内路径: %v", err)
	}

	if err := mounts.Rename(ctx, "/a/outer/x.txt", "/a/b/moved.txt"); !errors.Is(err, errno.CrossDevice) {
		t.Fatalf("嵌套边界改名应报 EXDEV: %v", err)
	}

	if err := mounts.Rename(ctx, "/a/b/inner.txt", "/a/b/renamed.txt"); err != nil {
		t.Fatalf("内层改名应成功: %v", err)
	}
	if content := readFile(t, mounts, "/a/b/renamed.txt"); content != "inner" {
		t.Fatalf("改名后内容=%q", content)
	}
}

func TestMountRejectsInvalidRegistrations(t *testing.T) {
	mounts := &Mounts{}
	system := newFS(t)
	if err := mounts.Mount("relative", system); !errors.Is(err, errno.Invalid) {
		t.Fatalf("相对挂载点应报 EINVAL: %v", err)
	}
	if err := mounts.Mount("/x", nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 目标应报 EINVAL: %v", err)
	}
	if err := mounts.Mount("/x", system); err != nil {
		t.Fatal(err)
	}
	if err := mounts.Mount("/x", system); !errors.Is(err, errno.Exist) {
		t.Fatalf("重复挂载点应报 EEXIST: %v", err)
	}

	if err := mounts.Mount("/x/", system); !errors.Is(err, errno.Exist) {
		t.Fatalf("规范化后的重复挂载点应报 EEXIST: %v", err)
	}
}

func TestSymlinkAndReadlinkAcrossMounts(t *testing.T) {
	ctx := context.Background()
	mounts, _, _ := mountTwo(t)
	if err := mounts.Symlink(ctx, "/mnt/target.txt", "/link.txt"); err != nil {
		t.Fatalf("跨挂载符号链接应可建立: %v", err)
	}
	target, err := mounts.Readlink(ctx, "/link.txt")
	if err != nil || target != "/mnt/target.txt" {
		t.Fatalf("target=%q err=%v", target, err)
	}

	if err := mounts.Symlink(ctx, "/target", "/unmounted/link"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未挂载位置建链接应报 ENOENT: %v", err)
	}
}

func TestReadDirMkdirRemoveDispatch(t *testing.T) {
	ctx := context.Background()
	mounts, _, _ := mountTwo(t)
	if err := mounts.Mkdir(ctx, "/mnt/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mounts, "/mnt/dir/file.txt", "x")
	entries, err := mounts.ReadDir(ctx, "/mnt/dir")
	if err != nil || len(entries) != 1 || entries[0].Name() != "file.txt" {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if err := mounts.Remove(ctx, "/mnt/dir/file.txt"); err != nil {
		t.Fatal(err)
	}
	if err := mounts.Remove(ctx, "/mnt/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := mounts.Lstat(ctx, "/mnt/dir"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("删除后应不存在: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := mounts.Rename(cancelled, "/mnt/a", "/mnt/b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := mounts.Link(cancelled, "/mnt/a", "/mnt/b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := mounts.Remove(cancelled, "/mnt/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}
