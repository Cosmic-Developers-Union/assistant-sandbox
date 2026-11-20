package devfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type stubTerminal struct {
	input   bytes.Buffer
	output  bytes.Buffer
	readErr error
}

func (s *stubTerminal) Read(buffer []byte) (int, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	return s.input.Read(buffer)
}

func (s *stubTerminal) Write(buffer []byte) (int, error) {
	return s.output.Write(buffer)
}

func newFS(t *testing.T, terminal Terminal) *FS {
	t.Helper()
	fs, err := New(Options{Entropy: &entropy.Counter{}, Terminal: terminal, Owner: 0})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func open(t *testing.T, fs *FS, name string, flags uint32) vmfs.File {
	t.Helper()
	handle, err := fs.Open(context.Background(), "/"+name, flags, 0)
	if err != nil {
		t.Fatalf("打开 %s: %v", name, err)
	}
	return handle
}

func TestNullReadsEOFAndDiscardsWrites(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, nil)
	handle := open(t, fs, nullName, vmfs.ReadWrite)
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	buffer := make([]byte, 16)
	if _, err := handle.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("/dev/null 读应得 EOF: %v", err)
	}
	written, err := handle.Write([]byte("discarded"))
	if err != nil || written != len("discarded") {
		t.Fatalf("写入应被丢弃并报告接受: %d %v", written, err)
	}

	if _, err := fs.Open(ctx, "/nope", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未知节点应报 ENOENT: %v", err)
	}
}

func TestZeroProducesZeroBytes(t *testing.T) {
	fs := newFS(t, nil)
	handle := open(t, fs, zeroName, vmfs.ReadWrite)
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	buffer := make([]byte, 32)

	for index := range buffer {
		buffer[index] = 0xFF
	}
	read, err := handle.Read(buffer)
	if err != nil || read != len(buffer) {
		t.Fatalf("读取=%d err=%v", read, err)
	}
	for index, value := range buffer {
		if value != 0 {
			t.Fatalf("第 %d 字节=%d，应为 0", index, value)
		}
	}

	if _, err := handle.Read(buffer); err != nil {
		t.Fatalf("重复读取不应结束: %v", err)
	}

	if written, err := handle.Write([]byte("ignored")); err != nil || written != len("ignored") {
		t.Fatalf("写入应被丢弃: %d %v", written, err)
	}
}

func TestRandomReadsFromInjectedEntropy(t *testing.T) {
	for _, name := range []string{randomName, urandomName} {
		fs := newFS(t, nil)
		handle := open(t, fs, name, vmfs.ReadOnly)
		first := make([]byte, 16)
		if read, err := handle.Read(first); err != nil || read != 16 {
			t.Fatalf("%s 读取=%d err=%v", name, read, err)
		}
		if err := handle.Close(); err != nil {
			t.Error(err)
		}

		again := newFS(t, nil)
		other := open(t, again, name, vmfs.ReadOnly)
		second := make([]byte, 16)
		if _, err := other.Read(second); err != nil {
			t.Fatal(err)
		}
		if err := other.Close(); err != nil {
			t.Error(err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("%s 应从注入熵源取字节（两次不一致）: %x vs %x", name, first, second)
		}

		if bytes.Equal(first, make([]byte, 16)) {
			t.Fatalf("%s 不应恒为零", name)
		}
	}

	fs := newFS(t, nil)
	handle := open(t, fs, randomName, vmfs.ReadWrite)
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := handle.Write([]byte("x")); !errors.Is(err, errno.Permission) {
		t.Fatalf("随机设备写入应报 EACCES: %v", err)
	}
}

func TestEntropyFaultIsPropagated(t *testing.T) {
	fs, err := New(Options{Entropy: failingEntropy{}})
	if err != nil {
		t.Fatal(err)
	}
	handle := open(t, fs, randomName, vmfs.ReadOnly)
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := handle.Read(make([]byte, 8)); !errors.Is(err, errno.NoSpace) {
		t.Fatalf("熵源故障应上报: %v", err)
	}
}

type failingEntropy struct{}

func (failingEntropy) Read(context.Context, []byte) (int, error) { return 0, errno.NoSpace }

func TestTTYRequiresTerminal(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, nil)
	if _, err := fs.Open(ctx, "/"+ttyName, vmfs.ReadWrite, 0); !errors.Is(err, errno.NoDevice) {
		t.Fatalf("无控制终端应报 ENXIO: %v", err)
	}

	terminal := &stubTerminal{}
	terminal.input.WriteString("typed")
	fs2 := newFS(t, terminal)
	handle := open(t, fs2, ttyName, vmfs.ReadWrite)
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := handle.Write([]byte("prompt")); err != nil {
		t.Fatal(err)
	}
	if terminal.output.String() != "prompt" {
		t.Fatalf("终端输出=%q", terminal.output.String())
	}
	buffer := make([]byte, 5)
	if read, err := handle.Read(buffer); err != nil || read != 5 {
		t.Fatalf("终端读取=%d err=%v", read, err)
	}
	if string(buffer) != "typed" {
		t.Fatalf("终端输入=%q", buffer)
	}
}

func TestTTYPermissionsAreEnforced(t *testing.T) {
	ctx := context.Background()

	fs := newFS(t, &stubTerminal{})
	if _, err := fs.Open(ctx, "/"+ttyName, vmfs.ReadWrite, 0); err != nil {
		t.Fatalf("默认权限下所有者应可打开: %v", err)
	}
	info, err := fs.Stat(ctx, "/"+ttyName)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("/dev/tty 默认权限=%v want 0600", info.Mode().Perm())
	}

	readOnly, err := New(Options{
		Entropy: &entropy.Counter{}, Terminal: &stubTerminal{},
		Mode: map[string]stdfs.FileMode{ttyName: 0o400},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly.Open(ctx, "/"+ttyName, vmfs.WriteOnly, 0); !errors.Is(err, errno.Permission) {
		t.Fatalf("只读终端写打开应报 EACCES: %v", err)
	}
	if _, err := readOnly.Open(ctx, "/"+ttyName, vmfs.ReadOnly, 0); err != nil {
		t.Fatalf("只读终端读打开应成功: %v", err)
	}

	none, err := New(Options{
		Entropy: &entropy.Counter{},
		Mode:    map[string]stdfs.FileMode{nullName: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := none.Open(ctx, "/"+nullName, vmfs.ReadOnly, 0); !errors.Is(err, errno.Permission) {
		t.Fatalf("不可读节点应报 EACCES: %v", err)
	}
	if _, err := none.Open(ctx, "/"+nullName, vmfs.WriteOnly, 0); !errors.Is(err, errno.Permission) {
		t.Fatalf("不可写节点应报 EACCES: %v", err)
	}
}

func TestOtherDevicesDefaultToWorldAccessible(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, nil)
	for _, name := range []string{nullName, zeroName, randomName, urandomName} {
		info, err := fs.Stat(ctx, "/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o666 {
			t.Errorf("%s 默认权限=%v want 0666", name, info.Mode().Perm())
		}
		if info.Mode()&stdfs.ModeCharDevice == 0 {
			t.Errorf("%s 应是字符设备: %v", name, info.Mode())
		}
	}
}

func TestReadOnlyHandleRejectsWrites(t *testing.T) {
	fs := newFS(t, nil)
	handle := open(t, fs, nullName, vmfs.ReadOnly)
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := handle.Write([]byte("x")); !errors.Is(err, errno.Permission) {
		t.Fatalf("只读句柄写入应报 EACCES: %v", err)
	}
}

func TestSeekIsRejected(t *testing.T) {
	fs := newFS(t, &stubTerminal{})
	for _, name := range []string{nullName, zeroName, randomName, ttyName} {
		handle := open(t, fs, name, vmfs.ReadWrite)
		if _, err := handle.Seek(0, 0); !errors.Is(err, errno.NotSupported) {
			t.Errorf("%s 定位应报 ENOTSUP: %v", name, err)
		}
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestDirectoryViewListsDevices(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, nil)
	entries, err := fs.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	want := []string{"fd", "null", "random", "stderr", "stdin", "stdout", "tty", "urandom", "zero"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("设备列表=%v want %v", names, want)
	}

	again, err := fs.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(entries) {
		t.Fatal("目录视图必须可复现")
	}

	if _, err := fs.ReadDir(ctx, "/"+nullName); !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("对设备读目录应报 ENOTDIR: %v", err)
	}

	if _, err := fs.Open(ctx, "/", vmfs.ReadOnly, 0); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("打开根目录应报 EISDIR: %v", err)
	}
}

func TestMutationsAreRejected(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, nil)
	if err := fs.Mkdir(ctx, "/new", 0o755); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Mkdir 应报 EROFS: %v", err)
	}
	if err := fs.Remove(ctx, "/"+nullName); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Remove 应报 EROFS: %v", err)
	}
	if err := fs.Rename(ctx, "/"+nullName, "/n2"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Rename 应报 EROFS: %v", err)
	}
	if err := fs.Link(ctx, "/"+nullName, "/l"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Link 应报 EROFS: %v", err)
	}
	if err := fs.Symlink(ctx, "/"+nullName, "/l"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Symlink 应报 EROFS: %v", err)
	}

	if _, err := fs.Readlink(ctx, "/"+nullName); !errors.Is(err, errno.Invalid) {
		t.Errorf("Readlink 应报 EINVAL: %v", err)
	}
}

func TestConstructionAndCancellation(t *testing.T) {

	if _, err := New(Options{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺熵源应报 EINVAL: %v", err)
	}
	fs := newFS(t, &stubTerminal{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fs.Stat(cancelled, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := fs.ReadDir(cancelled, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := fs.Open(cancelled, "/"+nullName, vmfs.ReadOnly, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := fs.Mkdir(cancelled, "/x", 0o755); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestClosedHandleRejectsOperations(t *testing.T) {
	fs := newFS(t, nil)
	handle := open(t, fs, zeroName, vmfs.ReadWrite)
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	buffer := make([]byte, 4)
	if _, err := handle.Read(buffer); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后读取应报 ECANCELED: %v", err)
	}
	if _, err := handle.Write(buffer); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后写入应报 ECANCELED: %v", err)
	}
	if _, err := handle.Seek(0, 0); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后定位应报 ECANCELED: %v", err)
	}
	if _, err := handle.Stat(); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Stat 应报 ECANCELED: %v", err)
	}
}

func TestPathValidation(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, nil)
	for _, path := range []string{"", "null", "/null/extra", "//null"} {
		if _, err := fs.Stat(ctx, path); err == nil {
			t.Errorf("路径 %q 应被拒绝", path)
		}
	}

	handle := open(t, fs, randomName, vmfs.ReadOnly)
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if read, err := handle.Read(nil); err != nil || read != 0 {
		t.Fatalf("零长度读取应返回 0: %d %v", read, err)
	}
}

func TestLstatMatchesStat(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, nil)
	stat, err := fs.Stat(ctx, "/"+nullName)
	if err != nil {
		t.Fatal(err)
	}
	lstat, err := fs.Lstat(ctx, "/"+nullName)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode() != lstat.Mode() || stat.Name() != lstat.Name() {
		t.Fatalf("Stat 与 Lstat 应等价: %v vs %v", stat.Mode(), lstat.Mode())
	}
}

// TestSubdirDelegatesSharedMemory 验证 N09 的设备侧:/dev/shm 由可写
// 子目录承载,设备节点仍只读,子目录内可创建/读取/删除。
func TestSubdirDelegatesSharedMemory(t *testing.T) {
	ctx := context.Background()
	shm, err := memfs.New(&clock.Virtual{}, memfs.Options{TotalBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	devices, err := New(Options{Entropy: &entropy.Counter{}, Subdirs: map[string]vmfs.System{"shm": shm}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := devices.ReadDir(ctx, "/")
	if err != nil || !slices.ContainsFunc(entries, func(entry stdfs.DirEntry) bool { return entry.Name() == "shm" && entry.IsDir() }) {
		t.Fatalf("根目录缺少 shm: %v err=%v", entries, err)
	}
	file, err := devices.Open(ctx, "/shm/a", vmfs.Create|vmfs.ReadWrite, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("shared")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := devices.Open(ctx, "/shm/a", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(content)
	if err != nil || string(data) != "shared" {
		t.Fatalf("内容=%q err=%v", data, err)
	}
	if err := content.Close(); err != nil {
		t.Fatal(err)
	}
	if err := devices.Remove(ctx, "/shm/a"); err != nil {
		t.Fatal(err)
	}
	// 设备节点本身仍不可写。
	if _, err := devices.Open(ctx, "/null", vmfs.ReadWrite, 0); err != nil {
		t.Fatal(err)
	}
	if err := devices.Mkdir(ctx, "/other", 0o755); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("设备视图根仍只读: %v", err)
	}
}
