package memfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func newFS(t *testing.T) (*FS, *clock.Virtual) {
	t.Helper()
	clk := &clock.Virtual{}
	f, err := New(clk, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return f, clk
}

func TestNewRejectsMissingClock(t *testing.T) {
	if _, err := New(nil, Options{}); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestBasicFileLifecycle(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/a.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := file.Write([]byte("hello world")); n != 11 || err != nil {
		t.Fatal(n, err)
	}

	other, err := f.Open(ctx, "/a.txt", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(other)
	if err != nil || string(data) != "hello world" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	again, _ := file.Stat()
	if again.Size() != 11 || again.Name() != "a.txt" {
		t.Fatalf("stat=%+v", again)
	}
	meta, ok := again.Sys().(*vmfs.Metadata)
	if !ok || meta.NLink != 1 || meta.Inode == 0 {
		t.Fatalf("meta=%+v ok=%v", again.Sys(), ok)
	}

	trunc, err := f.Open(ctx, "/a.txt", vmfs.WriteOnly|vmfs.Truncate, 0)
	if err != nil {
		t.Fatal(err)
	}
	if size, _ := trunc.Stat(); size.Size() != 0 {
		t.Fatalf("size=%d", size.Size())
	}
	for _, c := range []io.Closer{file, other, trunc} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenFlagSemantics(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	if _, err := f.Open(ctx, "/x", vmfs.Exclusive, 0o644); !errors.Is(err, errno.Invalid) {
		t.Fatal("无 Create 的 Exclusive 应无效")
	}
	if _, err := f.Open(ctx, "/x", vmfs.Create|vmfs.Exclusive, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/x", vmfs.Create|vmfs.Exclusive, 0o644); !errors.Is(err, errno.Exist) {
		t.Fatal(err)
	}

	if _, err := f.Open(ctx, "/x", vmfs.Append, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	if err := f.Mkdir(ctx, "/d", 0o755); err != nil {
		t.Fatal(err)
	}
	// O_RDONLY 允许打开目录(目录句柄是 getdents64 的入口),
	// 写访问仍报 EISDIR。
	dir, err := f.Open(ctx, "/d", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/d", vmfs.ReadWrite, 0); !errors.Is(err, errno.IsDirectory) {
		t.Fatal(err)
	}

	if _, err := f.Open(ctx, "/no/child", vmfs.Create, 0o644); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}

	if _, err := f.Open(ctx, "relative", vmfs.ReadOnly, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	if _, err := f.Open(ctx, "/a\x00b", vmfs.ReadOnly, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	if _, err := f.Open(ctx, "/"+string(make([]byte, maxPathLength)), vmfs.ReadOnly, 0); !errors.Is(err, errno.NameTooLong) {
		t.Fatal(err)
	}
}

func TestUnlinkKeepsOpenHandle(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/keep.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("persistent")); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(ctx, "/keep.txt"); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Stat(ctx, "/keep.txt"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}

	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "persistent" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryOperations(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	for _, dir := range []string{"/b", "/a", "/a/inner"} {
		if err := f.Mkdir(ctx, dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Mkdir(ctx, "/a", 0o755); !errors.Is(err, errno.Exist) {
		t.Fatal(err)
	}

	entries, err := f.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "a" || entries[1].Name() != "b" {
		for _, e := range entries {
			t.Logf("entry=%s", e.Name())
		}
		t.Fatal("目录项应按名称排序")
	}
	if !entries[0].IsDir() {
		t.Fatal("应为目录")
	}

	if err := f.Remove(ctx, "/a"); !errors.Is(err, errno.NotEmpty) {
		t.Fatal(err)
	}

	if _, err := f.Open(ctx, "/a/inner/file", vmfs.Create, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := f.ReadDir(ctx, "/a/inner/file"); !errors.Is(err, errno.NotDirectory) {
		t.Fatal(err)
	}

	if err := f.Rename(ctx, "/a", "/a/inner/moved"); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	if _, err := f.Open(ctx, "/src", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/dst", vmfs.Create, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Rename(ctx, "/src", "/dst"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, "/src"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, "/dst"); err != nil {
		t.Fatal(err)
	}

	if err := f.Rename(ctx, "/dst", "/a/inner"); !errors.Is(err, errno.IsDirectory) {
		t.Fatal(err)
	}

	if err := f.Remove(ctx, "/a/inner/file"); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(ctx, "/a/inner"); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(ctx, "/a"); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkSemantics(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/real.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("target")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if err := f.Symlink(ctx, "real.txt", "/link"); err != nil {
		t.Fatal(err)
	}

	data, err := readAll(ctx, f, "/link")
	if err != nil || string(data) != "target" {
		t.Fatalf("data=%q err=%v", data, err)
	}

	info, err := f.Lstat(ctx, "/link")
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("info=%v err=%v", info, err)
	}

	if info, err := f.Stat(ctx, "/link"); err != nil || info.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("info=%v err=%v", info, err)
	}

	if target, err := f.Readlink(ctx, "/link"); err != nil || target != "real.txt" {
		t.Fatalf("target=%q err=%v", target, err)
	}

	if err := f.Symlink(ctx, "missing", "/dangling"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, "/dangling"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}

	if err := f.Symlink(ctx, "/loop2", "/loop1"); err != nil {
		t.Fatal(err)
	}
	if err := f.Symlink(ctx, "/loop1", "/loop2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, "/loop1"); !errors.Is(err, errno.LinkLoop) {
		t.Fatal(err)
	}

	if err := f.Mkdir(ctx, "/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.Symlink(ctx, "/dir", "/dlink"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/dlink/file", vmfs.Create, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := f.Symlink(ctx, "../real.txt", "/dir/up"); err != nil {
		t.Fatal(err)
	}
	if data, err := readAll(ctx, f, "/dir/up"); err != nil || string(data) != "target" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func readAll(ctx context.Context, f *FS, path string) (data []byte, resultErr error) {
	file, err := f.Open(ctx, path, vmfs.ReadOnly, 0)
	if err != nil {
		return nil, err
	}
	data, resultErr = io.ReadAll(file)
	return data, errors.Join(resultErr, file.Close())
}

func TestHardLinks(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/one", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("shared")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if err := f.Link(ctx, "/one", "/two"); err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat(ctx, "/one")
	meta := info.Sys().(*vmfs.Metadata)
	if meta.NLink != 2 {
		t.Fatalf("nlink=%d", meta.NLink)
	}

	if err := f.SetTimes(ctx, "/two", 0, 0); err != nil {
		t.Fatal(err)
	}
	w, err := f.Open(ctx, "/two", vmfs.WriteOnly|vmfs.Truncate, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("via-two")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Error(err)
	}
	data, err := readAll(ctx, f, "/one")
	if err != nil || string(data) != "via-two" {
		t.Fatalf("data=%q err=%v", data, err)
	}

	if err := f.Mkdir(ctx, "/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.Link(ctx, "/d", "/dlink"); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	if err := f.Remove(ctx, "/one"); err != nil {
		t.Fatal(err)
	}
	if data, err := readAll(ctx, f, "/two"); err != nil || string(data) != "via-two" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestPermissions(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/ro", vmfs.Create|vmfs.WriteOnly, 0o444)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if _, err := f.Open(ctx, "/ro", vmfs.WriteOnly, 0); !errors.Is(err, errno.Permission) {
		t.Fatal("只读文件不应可写")
	}
	if _, err := f.Open(ctx, "/ro", vmfs.ReadOnly, 0); err != nil {
		t.Fatal(err)
	}

	if err := f.Mkdir(ctx, "/closed", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/closed/inner", vmfs.Create, 0o644); !errors.Is(err, errno.Permission) {
		t.Fatal(err)
	}

	if err := f.Remove(ctx, "/ro"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, "/ro"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
}

func TestTimestampsComeFromGuestClock(t *testing.T) {
	f, clk := newFS(t)
	ctx := context.Background()
	if err := clk.Sleep(ctx, 5000); err != nil {
		t.Fatal(err)
	}
	file, err := f.Open(ctx, "/timed", vmfs.Create, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	info, _ := f.Stat(ctx, "/timed")
	if info.ModTime().UnixNano() != 5000 {
		t.Fatalf("mtime=%d", info.ModTime().UnixNano())
	}

	if err := clk.Sleep(ctx, 3000); err != nil {
		t.Fatal(err)
	}
	w, err := f.Open(ctx, "/timed", vmfs.WriteOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Error(err)
	}
	info, _ = f.Stat(ctx, "/timed")
	if info.ModTime().UnixNano() != 8000 {
		t.Fatalf("mtime=%d", info.ModTime().UnixNano())
	}
}

func TestBudgets(t *testing.T) {
	clk := &clock.Virtual{}
	f, err := New(clk, Options{MaxNodes: 3, MaxFileBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := f.Mkdir(ctx, "/d", 0o755); err != nil {
		t.Fatal(err)
	}

	big, err := f.Open(ctx, "/d/big", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := big.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := f.Open(ctx, "/d/over", vmfs.Create, 0o644); !errors.Is(err, errno.NoSpace) {
		t.Fatal(err)
	}

	if _, err := big.Write([]byte("12345")); !errors.Is(err, errno.NoSpace) {
		t.Fatal(err)
	}
	if _, err := big.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotAndRestore(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	write := func(p, s string) {
		file, err := f.Open(ctx, p, vmfs.Create|vmfs.ReadWrite, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}
	write("/before", "v1")
	snap, err := f.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	write("/before", "v2")
	write("/after", "new")
	if err := f.Remove(ctx, "/before"); err != nil {
		t.Fatal(err)
	}

	if err := snap.Restore(ctx, f); err != nil {
		t.Fatal(err)
	}
	data, err := readAll(ctx, f, "/before")
	if err != nil || string(data) != "v1" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := f.Stat(ctx, "/after"); !errors.Is(err, errno.NotFound) {
		t.Fatal("快照后的文件应消失")
	}

	if err := f.Mkdir(ctx, "/post", 0o755); err != nil {
		t.Fatal(err)
	}

	if err := snap.Restore(ctx, nil); !errors.Is(err, errno.NotSupported) {
		t.Fatal(err)
	}
}

func TestDirEntryAndMetadataConversions(t *testing.T) {
	f, clk := newFS(t)
	ctx := context.Background()
	if err := clk.Sleep(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/f", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Symlink(ctx, "/f", "/sym"); err != nil {
		t.Fatal(err)
	}
	entries, err := f.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := entry.Info(); err != nil {
			t.Fatal(err)
		}
		if entry.Name() == "" {
			t.Fatal("目录项必须有名称")
		}
	}

	info, err := f.Lstat(ctx, "/sym")
	if err != nil || info.Size() != int64(len("/f")) {
		t.Fatalf("size=%d err=%v", info.Size(), err)
	}

	if f.Clock() == nil {
		t.Fatal("clock 访问器返回 nil")
	}
}

func TestHandleEdgePaths(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	handle, err := f.Open(ctx, "/h", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := handle.Seek(-1, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if _, err := handle.Seek(0, 99); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	if _, err := handle.Seek(10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Read(make([]byte, 2)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}

	if off, err := handle.Seek(0, 2); err != nil || off != 0 {
		t.Fatalf("off=%d err=%v", off, err)
	}
	if _, err := handle.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if off, err := handle.Seek(-1, 1); err != nil || off != 0 {
		t.Fatalf("off=%d err=%v", off, err)
	}

	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	if _, err := handle.Read(make([]byte, 1)); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	if _, err := handle.Write(nil); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	if _, err := handle.Seek(0, 0); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	if _, err := handle.Stat(); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsCancel(t *testing.T) {
	f, _ := newFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := f.Open(context.Background(), "", vmfs.ReadOnly, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	cancelled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := f.Mkdir(cancelled, "/x", 0o755); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.Remove(cancelled, "/x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.Rename(cancelled, "/x", "/y"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.Link(cancelled, "/x", "/y"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.Symlink(cancelled, "/x", "/y"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := f.Readlink(cancelled, "/x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.SetTimes(cancelled, "/x", 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := f.Lstat(cancelled, "/x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := f.ReadDir(cancelled, "/"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCheckWritableAndSymlinkBudgets(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()

	if err := f.Mkdir(ctx, "/", 0o755); !errors.Is(err, errno.Exist) {
		t.Fatalf("mkdir / = %v want EEXIST", err)
	}
	if err := f.Remove(ctx, "/"); !errors.Is(err, errno.Permission) {
		t.Fatalf("remove / = %v want EACCES", err)
	}
	if err := f.Rename(ctx, "/", "/x"); !errors.Is(err, errno.Permission) {
		t.Fatalf("rename / = %v want EACCES", err)
	}

	if err := f.Symlink(ctx, "", "/l"); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := f.Symlink(ctx, "/"+string(make([]byte, maxPathLength)), "/l"); !errors.Is(err, errno.NameTooLong) {
		t.Fatal(err)
	}

	if err := f.Symlink(ctx, "ok", "/good"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, "/good"); err == nil {
		t.Fatal("悬空链接应失败")
	}

	if err := f.Remove(ctx, "/good"); err != nil {
		t.Fatal(err)
	}
}

type failingClock struct{}

func (failingClock) Wall(context.Context) (int64, error)       { return 0, errno.Cancelled }
func (failingClock) Monotonic(context.Context) (uint64, error) { return 0, nil }
func (failingClock) Sleep(context.Context, uint64) error       { return errno.Cancelled }

func TestPathAndClockFailureBranches(t *testing.T) {

	broken, err := New(&clock.Virtual{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	broken.clock = failingClock{}
	ctx := context.Background()

	if _, err := broken.Open(ctx, "/f", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := broken.Mkdir(ctx, "/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Open(ctx, "/d/../f", vmfs.ReadOnly, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := broken.Open(ctx, "/../../f", vmfs.ReadOnly, 0); err != nil {
		t.Fatal(err)
	}

	if err := broken.Symlink(ctx, ".", "/dot"); err != nil {
		t.Fatal(err)
	}
	if entries, err := broken.ReadDir(ctx, "/dot"); err != nil || len(entries) == 0 {
		t.Fatalf("dot link entries=%v err=%v", entries, err)
	}

	entries, _ := broken.ReadDir(ctx, "/")
	sawFile, sawDir := false, false
	for _, entry := range entries {
		switch entry.Type() {
		case 0:
			sawFile = true
		case fs.ModeDir:
			sawDir = true
		}
		if _, err := entry.Info(); err != nil {
			t.Fatal(err)
		}
	}
	if !sawFile || !sawDir {
		t.Fatalf("sawFile=%v sawDir=%v", sawFile, sawDir)
	}
}

func TestPathValidationErrors(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	long := "/" + strings.Repeat("a", maxPathLength+1)
	longName := "/" + strings.Repeat("b", maxNameLength+1)
	for _, p := range []string{"", "relative", "/bad\x00name", long, longName} {
		if _, err := f.Stat(ctx, p); err == nil {
			t.Fatalf("Stat(%q) 应失败", p)
		}
		if _, err := f.Open(ctx, p, vmfs.Create|vmfs.ReadWrite, 0o644); err == nil {
			t.Fatalf("Open(%q) 应失败", p)
		}
		if err := f.Mkdir(ctx, p, 0o755); err == nil {
			t.Fatalf("Mkdir(%q) 应失败", p)
		}
		if err := f.Remove(ctx, p); err == nil {
			t.Fatalf("Remove(%q) 应失败", p)
		}
	}

	if _, err := f.Stat(ctx, longName); !errors.Is(err, errno.NameTooLong) {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenFlagAndTraversalErrors(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	if _, err := f.Open(ctx, "/x", vmfs.Exclusive, 0o644); !errors.Is(err, errno.Invalid) {
		t.Fatalf("Exclusive 无 Create: %v", err)
	}
	if _, err := f.Open(ctx, "/x", vmfs.Append, 0o644); !errors.Is(err, errno.Invalid) {
		t.Fatalf("Append 无读写: %v", err)
	}
	if _, err := f.Open(ctx, "/x", vmfs.Exclusive|vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/x", vmfs.Exclusive|vmfs.Create|vmfs.ReadWrite, 0o644); !errors.Is(err, errno.Exist) {
		t.Fatalf("err=%v", err)
	}

	if _, err := f.Open(ctx, "/x/child", vmfs.Create|vmfs.ReadWrite, 0o644); !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Mkdir(ctx, "/d", 0o755); err != nil {
		t.Fatal(err)
	}
	// 只读打开目录合法,写访问报 EISDIR。
	if _, err := f.Open(ctx, "/d", vmfs.ReadOnly, 0); err != nil {
		t.Fatalf("err=%v", err)
	}
	if _, err := f.Open(ctx, "/d", vmfs.WriteOnly, 0); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("err=%v", err)
	}
}

func TestSymlinkLookupAndBudgetErrors(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()

	if err := f.Symlink(ctx, "/missing", "/dangling"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/dangling", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("err=%v", err)
	}
	if _, err := f.Lstat(ctx, "/dangling"); err != nil {
		t.Fatal(err)
	}
	if target, err := f.Readlink(ctx, "/dangling"); err != nil || target != "/missing" {
		t.Fatalf("target=%q err=%v", target, err)
	}

	if err := f.Symlink(ctx, "/loop-b", "/loop-a"); err != nil {
		t.Fatal(err)
	}
	if err := f.Symlink(ctx, "/loop-a", "/loop-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(ctx, "/loop-a"); !errors.Is(err, errno.LinkLoop) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Symlink(ctx, "", "/empty-target"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v", err)
	}
	longTarget := strings.Repeat("t", maxPathLength+1)
	if err := f.Symlink(ctx, longTarget, "/long-target"); !errors.Is(err, errno.NameTooLong) {
		t.Fatalf("err=%v", err)
	}

	file, err := f.Open(ctx, "/plain", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if _, err := f.Readlink(ctx, "/plain"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v", err)
	}

	small, err := New(&clock.Virtual{}, Options{MaxNodes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := small.Mkdir(ctx, "/a", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := small.Mkdir(ctx, "/b", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := small.Mkdir(ctx, "/c", 0o755); !errors.Is(err, errno.NoSpace) {
		t.Fatalf("err=%v", err)
	}
}

func TestRenameAndLinkErrors(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	if _, err := f.Open(ctx, "/f", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir(ctx, "/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir(ctx, "/e", 0o755); err != nil {
		t.Fatal(err)
	}

	if err := f.Rename(ctx, "/", "/renamed"); !errors.Is(err, errno.Permission) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Rename(ctx, "/d", "/d/self"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Rename(ctx, "/d", "/d"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v", err)
	}

	if _, err := f.Open(ctx, "/e/child", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Rename(ctx, "/d", "/e"); !errors.Is(err, errno.NotEmpty) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Rename(ctx, "/d", "/f"); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Rename(ctx, "/f", "/e"); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Link(ctx, "/d", "/hard-d"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Link(ctx, "/f", "/e"); !errors.Is(err, errno.Exist) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Symlink(ctx, "/f", "/e"); !errors.Is(err, errno.Exist) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Remove(ctx, "/"); !errors.Is(err, errno.Permission) {
		t.Fatalf("err=%v", err)
	}

	if err := f.Remove(ctx, "/e"); !errors.Is(err, errno.NotEmpty) {
		t.Fatalf("err=%v", err)
	}
}

func TestDirectoryPermissionDenials(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()

	if err := f.Mkdir(ctx, "/ro", 0o555); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(ctx, "/ro/new", vmfs.Create|vmfs.ReadWrite, 0o644); !errors.Is(err, errno.Permission) {
		t.Fatalf("Open err=%v", err)
	}
	if err := f.Mkdir(ctx, "/ro/sub", 0o755); !errors.Is(err, errno.Permission) {
		t.Fatalf("Mkdir err=%v", err)
	}
	if _, err := f.Open(ctx, "/src", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Rename(ctx, "/src", "/ro/moved"); !errors.Is(err, errno.Permission) {
		t.Fatalf("Rename err=%v", err)
	}
	if err := f.Link(ctx, "/src", "/ro/hard"); !errors.Is(err, errno.Permission) {
		t.Fatalf("Link err=%v", err)
	}
	if err := f.Symlink(ctx, "/src", "/ro/soft"); !errors.Is(err, errno.Permission) {
		t.Fatalf("Symlink err=%v", err)
	}

	if err := f.Mkdir(ctx, "/noRead", 0o311); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadDir(ctx, "/noRead"); !errors.Is(err, errno.Permission) {
		t.Fatalf("ReadDir err=%v", err)
	}

	if _, err := f.ReadDir(ctx, "/src"); !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("ReadDir err=%v", err)
	}
}

func TestFileHandleErrors(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/small", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := file.Seek(-5, io.SeekStart); !errors.Is(err, errno.Invalid) {
		t.Fatalf("负偏移 err=%v", err)
	}

	if _, err := file.Seek(0, 9); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知 whence err=%v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("x")); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后写入 err=%v", err)
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后读取 err=%v", err)
	}

	limited, err := New(&clock.Virtual{}, Options{MaxFileBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	small, err := limited.Open(ctx, "/q", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := small.Write([]byte("abcde")); n != 0 || !errors.Is(err, errno.NoSpace) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, err := small.Write([]byte("abcd")); n != 4 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := small.Close(); err != nil {
		t.Error(err)
	}
}

func TestCanceledContextsEverywhere(t *testing.T) {
	f, _ := newFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Open(ctx, "/x", vmfs.Create|vmfs.ReadWrite, 0o644); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open err=%v", err)
	}
	if _, err := f.Stat(ctx, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stat err=%v", err)
	}
	if _, err := f.Lstat(ctx, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lstat err=%v", err)
	}
	if _, err := f.ReadDir(ctx, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadDir err=%v", err)
	}
	if err := f.Mkdir(ctx, "/d", 0o755); !errors.Is(err, context.Canceled) {
		t.Fatalf("Mkdir err=%v", err)
	}
	if err := f.Remove(ctx, "/d"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remove err=%v", err)
	}
	if err := f.Rename(ctx, "/a", "/b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Rename err=%v", err)
	}
	if err := f.Link(ctx, "/a", "/b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Link err=%v", err)
	}
	if err := f.Symlink(ctx, "/a", "/b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Symlink err=%v", err)
	}
	if _, err := f.Readlink(ctx, "/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Readlink err=%v", err)
	}
	if err := f.SetTimes(ctx, "/a", 1, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("SetTimes err=%v", err)
	}
}

func TestChmodKeepsTypeAndFollowsLinks(t *testing.T) {
	ctx := context.Background()
	f, err := New(&clock.Virtual{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := f.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}

	if err := f.Chmod(ctx, "/data", fs.ModeDir|0o600); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat(ctx, "/data")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != 0o600 {
		t.Fatalf("模式位 %v（类型位被改写？）", info.Mode())
	}

	if err := f.Chmod(ctx, "/data", fs.ModeSetuid|0o755); err != nil {
		t.Fatal(err)
	}
	if info, err = f.Stat(ctx, "/data"); err != nil || info.Mode()&fs.ModeSetuid == 0 || info.Mode().Perm() != 0o755 {
		t.Fatalf("setuid 未保留: %v %v", info.Mode(), err)
	}

	if err := f.Symlink(ctx, "/data", "/link"); err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod(ctx, "/link", 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err = f.Stat(ctx, "/data"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("未跟随链接: %v %v", info.Mode(), err)
	}

	if err := f.Chmod(ctx, "/absent", 0o600); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失路径: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.Chmod(cancelled, "/data", 0o777); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消: %v", err)
	}
	if info, err = f.Stat(ctx, "/data"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("取消后仍改了模式: %v %v", info.Mode(), err)
	}
}

func TestAccessTimeFollowsSetTimes(t *testing.T) {
	ctx := context.Background()
	f, err := New(&clock.Virtual{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := f.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if _, err := f.AccessTime(ctx, "/data"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetTimes(ctx, "/data", 12345, 67890); err != nil {
		t.Fatal(err)
	}
	if got, err := f.AccessTime(ctx, "/data"); err != nil || got != 12345 {
		t.Fatalf("atime = %d, %v", got, err)
	}

	if _, err := readAll(ctx, f, "/data"); err != nil {
		t.Fatal(err)
	}
	if got, err := f.AccessTime(ctx, "/data"); err != nil || got != 12345 {
		t.Fatalf("读之后 atime = %d, %v", got, err)
	}
	if _, err := f.AccessTime(ctx, "/absent"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失路径: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.AccessTime(cancelled, "/data"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消: %v", err)
	}
}

func TestDirectoryHandleCursorAndSeek(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	for _, name := range []string{"/alpha", "/beta", "/gamma"} {
		file, err := f.Open(ctx, name, vmfs.Create|vmfs.ReadWrite, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Mkdir(ctx, "/sub", 0o755); err != nil {
		t.Fatal(err)
	}

	dir, err := f.Open(ctx, "/", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()

	// 非目录读取路径:Read 报 EISDIR,Write 报 EBADF。
	if _, err := dir.Read(make([]byte, 4)); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("read=%v want EISDIR", err)
	}
	if _, err := dir.Write([]byte("x")); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("write=%v want EBADF", err)
	}

	reader, ok := dir.(interface {
		Readdir(limit int) ([]fs.DirEntry, error)
	})
	if !ok {
		t.Fatal("目录句柄应实现 Readdir")
	}

	// limit 截断游标推进:每次返回至多 limit 条,按名称排序。
	first, err := reader.Readdir(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Name() != "alpha" || first[1].Name() != "beta" {
		t.Fatalf("第一批=%v", names(first))
	}
	second, err := reader.Readdir(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].Name() != "gamma" || second[1].Name() != "sub" {
		t.Fatalf("第二批=%v", names(second))
	}
	if rest, err := reader.Readdir(2); err != nil || len(rest) != 0 {
		t.Fatalf("读完应返回 0 条: %v %v", names(rest), err)
	}

	// Seek(0) 回卷后再次读到全部条目。
	if _, err := dir.(io.Seeker).Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	rewound, err := reader.Readdir(8)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names(rewound), ",") != "alpha,beta,gamma,sub" {
		t.Fatalf("回卷后=%v", names(rewound))
	}

	// seekdir 语义:Seek(c) 后从 cookie 为 c 的条目继续(含自身),
	// 已删除的条目消失,仍存在的条目不遗漏。
	info, err := rewound[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	cookie := info.Sys().(*vmfs.Metadata).Inode
	if _, err := dir.(io.Seeker).Seek(int64(cookie), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(ctx, "/beta"); err != nil {
		t.Fatal(err)
	}
	resumed, err := reader.Readdir(8)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names(resumed), ",") != "alpha,gamma,sub" {
		t.Fatalf("从 cookie=%d 恢复=%v", cookie, names(resumed))
	}
}

func names(entries []fs.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

func TestHandleTruncateSyncAndSetTimes(t *testing.T) {
	f, clk := newFS(t)
	ctx := context.Background()

	file, err := f.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}

	// 扩张补零(空洞)。
	if err := file.(interface{ Truncate(int64) error }).Truncate(16); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(file, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer[:10]) != "0123456789" || string(buffer[10:]) != "\x00\x00\x00\x00\x00\x00" {
		t.Fatalf("扩张后内容=%q", buffer)
	}

	// 收缩丢尾。
	if err := file.(interface{ Truncate(int64) error }).Truncate(4); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 4 {
		t.Fatalf("收缩后尺寸=%d", info.Size())
	}

	// 只读句柄与负长度 EINVAL;目录 EISDIR。
	readOnly, err := f.Open(ctx, "/data", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.(interface{ Truncate(int64) error }).Truncate(2); !errors.Is(err, errno.Invalid) {
		t.Fatalf("只读句柄=%v want EINVAL", err)
	}
	if err := file.(interface{ Truncate(int64) error }).Truncate(-1); !errors.Is(err, errno.Invalid) {
		t.Fatalf("负长度=%v want EINVAL", err)
	}
	if err := f.Mkdir(ctx, "/d", 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := f.Open(ctx, "/d", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.(interface{ Truncate(int64) error }).Truncate(2); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("目录=%v want EISDIR", err)
	}

	// memfs 同步是真实空操作(内存介质),数据可继续读取。
	if err := file.(interface{ Sync(bool) error }).Sync(true); err != nil {
		t.Fatal(err)
	}

	// SetTimes 更新纳秒时间并反映到文件状态。
	_ = clk.Advance(5_000_000_000)
	if err := file.(interface{ SetTimes(int64, int64) error }).SetTimes(111, 222); err != nil {
		t.Fatal(err)
	}
	info, err = file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().UnixNano() != 222 {
		t.Fatalf("mtime=%d want 222", info.ModTime().UnixNano())
	}
	if atime, err := f.AccessTime(ctx, "/data"); err != nil || atime != 111 {
		t.Fatalf("atime=%d err=%v", atime, err)
	}

	// 预算:扩张超过单文件上限报 ENOSPC。
	capped, err := New(clk, Options{MaxFileBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	small, err := capped.Open(ctx, "/cap", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := small.(interface{ Truncate(int64) error }).Truncate(9); !errors.Is(err, errno.NoSpace) {
		t.Fatalf("单文件预算=%v want ENOSPC", err)
	}
}
