package procfs

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type fixedProvider struct {
	entries []Entry
	self    proc.PID
	selfErr error
	err     error
}

func (p *fixedProvider) Processes(context.Context) ([]Entry, error) {
	if p.err != nil {
		return nil, p.err
	}
	return append([]Entry(nil), p.entries...), nil
}

func (p *fixedProvider) Self(context.Context) (proc.PID, error) {
	if p.selfErr != nil {
		return 0, p.selfErr
	}
	return p.self, nil
}

func sampleProvider() *fixedProvider {
	return &fixedProvider{
		self: 7,
		entries: []Entry{
			{ID: 1, Parent: 0, Generation: 1, Program: "init", Args: []string{"--boot"}, State: "ready", PGID: 1, SID: 1},
			{ID: 7, Parent: 1, Generation: 3, Program: "bash", Args: []string{"-c", "echo hi"}, State: "running", PGID: 7, SID: 1, OpenFDs: []uint32{0, 1, 2}},
		},
	}
}

func newFS(t *testing.T, provider Provider) *FS {
	t.Helper()
	fs, err := New(provider)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func readFile(t *testing.T, fs *FS, path string) string {
	t.Helper()
	handle, err := fs.Open(context.Background(), path, vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRootListsProcsAndSelf(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, sampleProvider())
	entries, err := fs.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	names := entryNames(entries)

	if strings.Join(names, ",") != "1,7,self" {
		t.Fatalf("根目录=%v", names)
	}

	again, err := fs.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(entryNames(again), ",") != strings.Join(names, ",") {
		t.Fatal("目录视图必须可复现")
	}
}

func TestSelfResolvesToCaller(t *testing.T) {
	ctx := context.Background()
	provider := sampleProvider()
	fs := newFS(t, provider)

	target, err := fs.Readlink(ctx, "/self")
	if err != nil || target != "7" {
		t.Fatalf("self 目标=%q err=%v", target, err)
	}

	lstat, err := fs.Lstat(ctx, "/self")
	if err != nil || lstat.Mode()&stdfs.ModeSymlink == 0 {
		t.Fatalf("Lstat 应报告符号链接: %+v err=%v", lstat, err)
	}
	stat, err := fs.Stat(ctx, "/self")
	if err != nil || !stat.IsDir() {
		t.Fatalf("Stat 应跟随到目录: %+v err=%v", stat, err)
	}

	if a, b := readFile(t, fs, "/self/status"), readFile(t, fs, "/7/status"); a != b {
		t.Fatalf("self 与进程号应等价:\n%s\n%s", a, b)
	}

	provider.self = 1
	target, err = fs.Readlink(ctx, "/self")
	if err != nil || target != "1" {
		t.Fatalf("self 应跟随调用方: %q err=%v", target, err)
	}
	if a, b := readFile(t, fs, "/self/status"), readFile(t, fs, "/1/status"); a != b {
		t.Fatal("self 应解析到新的调用方")
	}
}

func TestSelfRequiresProvider(t *testing.T) {
	ctx := context.Background()
	provider := sampleProvider()
	provider.selfErr = errno.NotSupported
	fs := newFS(t, provider)
	if _, err := fs.Readlink(ctx, "/self"); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("Readlink self 应上报原因: %v", err)
	}
	if _, err := fs.Stat(ctx, "/self"); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("Stat self 应上报原因: %v", err)
	}
}

func TestStatusContentCarriesKnownFacts(t *testing.T) {
	fs := newFS(t, sampleProvider())
	content := readFile(t, fs, "/7/status")
	for _, want := range []string{
		"Name:\tbash", "Pid:\t7", "PPid:\t1", "Pgid:\t7", "Sid:\t1",
		"State:\trunning", "Generation:\t3",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("status 缺少 %q:\n%s", want, content)
		}
	}

	if strings.Contains(content, "ExitCode") {
		t.Fatalf("运行中不应有 ExitCode:\n%s", content)
	}

	provider := sampleProvider()
	provider.entries[0].State = "zombie"
	provider.entries[0].ExitCode = 3
	provider.entries[0].ExitSignal = 15
	fs2 := newFS(t, provider)
	exited := readFile(t, fs2, "/1/status")
	if !strings.Contains(exited, "ExitCode:\t3") || !strings.Contains(exited, "ExitSignal:\t15") {
		t.Fatalf("已终止进程应带退出事实:\n%s", exited)
	}
}

func TestCmdlineIsNULSeparated(t *testing.T) {
	provider := sampleProvider()
	provider.entries[1].Args = []string{"-c", "echo hello world"}
	fs := newFS(t, provider)
	content := readFile(t, fs, "/7/cmdline")
	parts := strings.Split(content, "\x00")
	want := []string{"bash", "-c", "echo hello world"}
	if len(parts) != len(want) {
		t.Fatalf("参数个数=%d want %d: %q", len(parts), len(want), content)
	}
	for index, part := range want {
		if parts[index] != part {
			t.Fatalf("参数 %d=%q want %q", index, parts[index], part)
		}
	}
	if strings.Contains(content, " ") && !strings.Contains(content, "hello world") {
		t.Fatal("含空格的参数必须保持完整")
	}
}

func TestFDDirectoryReflectsOpenDescriptors(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, sampleProvider())
	entries, err := fs.ReadDir(ctx, "/7/fd")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(entryNames(entries), ",") != "0,1,2" {
		t.Fatalf("fd 目录=%v", entryNames(entries))
	}
	for _, entry := range entries {
		if entry.Type()&stdfs.ModeSymlink == 0 {
			t.Fatalf("fd 项应为符号链接: %+v", entry)
		}
	}

	if _, err := fs.Stat(ctx, "/7/fd/9"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("不存在的描述符应报 ENOENT: %v", err)
	}

	provider := sampleProvider()
	provider.entries[1].OpenFDs = nil
	fs2 := newFS(t, provider)
	empty, err := fs2.ReadDir(ctx, "/7/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("无描述符时 fd 目录应为空: %v", entryNames(empty))
	}
}

func TestProcessDisappearanceIsReportedAsMissing(t *testing.T) {
	ctx := context.Background()
	provider := sampleProvider()
	fs := newFS(t, provider)
	if _, err := fs.Stat(ctx, "/7/status"); err != nil {
		t.Fatalf("存在时应可读: %v", err)
	}

	provider.entries = provider.entries[:1]
	for _, path := range []string{"/7", "/7/status", "/7/cmdline", "/7/fd"} {
		if _, err := fs.Stat(ctx, path); !errors.Is(err, errno.NotFound) {
			t.Errorf("%s 在进程消失后应报 ENOENT: %v", path, err)
		}
	}
	if _, err := fs.Open(ctx, "/7/status", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Errorf("消失后打开应报 ENOENT: %v", err)
	}

	entries, err := fs.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if containsName(entryNames(entries), "7") {
		t.Fatalf("消失的进程不得出现在根目录: %v", entryNames(entries))
	}

	if _, err := fs.Stat(ctx, "/999"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("不存在的进程应报 ENOENT: %v", err)
	}
}

func TestOpenIsSnapshotConsistent(t *testing.T) {
	ctx := context.Background()
	provider := sampleProvider()
	fs := newFS(t, provider)
	handle, err := fs.Open(ctx, "/7/status", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	first, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}

	provider.entries[1].State = "changed-after-open"

	if _, err := handle.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	second, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("同一句柄内容必须稳定:\n%s\n%s", first, second)
	}
	if strings.Contains(string(second), "changed-after-open") {
		t.Fatal("打开后状态变化不应影响已打开的句柄")
	}

	if !strings.Contains(readFile(t, fs, "/7/status"), "changed-after-open") {
		t.Fatal("新的打开应看到新状态")
	}
}

func TestReadOnlyViewRejectsAllWrites(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, sampleProvider())

	for _, flags := range []uint32{vmfs.WriteOnly, vmfs.ReadWrite, vmfs.Create, vmfs.Truncate, vmfs.Append} {
		if _, err := fs.Open(ctx, "/7/status", flags, 0o644); !errors.Is(err, errno.Permission) {
			t.Errorf("flags=%d 应报 EACCES: %v", flags, err)
		}
	}

	if err := fs.Mkdir(ctx, "/new", 0o755); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Mkdir 应报 EROFS: %v", err)
	}
	if err := fs.Remove(ctx, "/7"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Remove 应报 EROFS: %v", err)
	}
	if err := fs.Rename(ctx, "/7", "/8"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Rename 应报 EROFS: %v", err)
	}
	if err := fs.Link(ctx, "/7/status", "/link"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Link 应报 EROFS: %v", err)
	}
	if err := fs.Symlink(ctx, "/7", "/link"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("Symlink 应报 EROFS: %v", err)
	}

	handle, err := fs.Open(ctx, "/7/status", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := handle.Write([]byte("x")); !errors.Is(err, errno.ReadOnlyFS) {
		t.Errorf("句柄写入应报 EROFS: %v", err)
	}
}

func TestDirectoryPathsRejectOpen(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, sampleProvider())
	if _, err := fs.Open(ctx, "/", vmfs.ReadOnly, 0); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("根目录应报 EISDIR: %v", err)
	}
	if _, err := fs.Open(ctx, "/7", vmfs.ReadOnly, 0); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("进程目录应报 EISDIR: %v", err)
	}

	if _, err := fs.Open(ctx, "/self", vmfs.ReadOnly, 0); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("self 应报 EISDIR: %v", err)
	}

	if _, err := fs.ReadDir(ctx, "/7/status"); !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("对文件读目录应报 ENOTDIR: %v", err)
	}
}

func TestInvalidPathsAreRejected(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, sampleProvider())
	for _, path := range []string{"", "relative", "/7//status"} {
		if _, err := fs.Stat(ctx, path); !errors.Is(err, errno.Invalid) {
			t.Errorf("路径 %q 应报 EINVAL: %v", path, err)
		}
	}

	if _, err := fs.Stat(ctx, "/notanumber"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("非法进程名应报 ENOENT: %v", err)
	}

	if _, err := fs.Stat(ctx, "/7/nope"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("不存在条目应报 ENOENT: %v", err)
	}

	if _, err := fs.Stat(ctx, "/99999999999999999999"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("溢出进程号应报 ENOENT: %v", err)
	}
}

func TestProviderFaultsArePropagated(t *testing.T) {
	ctx := context.Background()
	provider := sampleProvider()
	provider.err = errno.Limit
	fs := newFS(t, provider)
	if _, err := fs.ReadDir(ctx, "/"); !errors.Is(err, errno.Limit) {
		t.Fatalf("列目录应上报来源故障: %v", err)
	}
	if _, err := fs.Stat(ctx, "/1"); !errors.Is(err, errno.Limit) {
		t.Fatalf("Stat 应上报来源故障: %v", err)
	}
	if _, err := fs.Open(ctx, "/1/status", vmfs.ReadOnly, 0); !errors.Is(err, errno.Limit) {
		t.Fatalf("Open 应上报来源故障: %v", err)
	}
}

func TestConstructionAndCancellation(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 来源应报 EINVAL: %v", err)
	}
	fs := newFS(t, sampleProvider())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fs.Stat(cancelled, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := fs.ReadDir(cancelled, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := fs.Open(cancelled, "/1/status", vmfs.ReadOnly, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := fs.Mkdir(cancelled, "/x", 0o755); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestSeekAndClosedHandle(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, sampleProvider())
	handle, err := fs.Open(ctx, "/7/cmdline", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}

	size, err := handle.Seek(0, 2)
	if err != nil || size == 0 {
		t.Fatalf("定位到末尾: %d %v", size, err)
	}
	buffer := make([]byte, 8)
	if _, err := handle.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("末尾读取应得 EOF: %v", err)
	}

	if _, err := handle.Seek(-1, 0); !errors.Is(err, errno.Invalid) {
		t.Fatalf("负偏移应报 EINVAL: %v", err)
	}

	if _, err := handle.Seek(0, 9); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非法基准应报 EINVAL: %v", err)
	}

	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	if _, err := handle.Read(buffer); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后读取应报 ECANCELED: %v", err)
	}
	if _, err := handle.Stat(); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Stat 应报 ECANCELED: %v", err)
	}
	if _, err := handle.Seek(0, 0); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Seek 应报 ECANCELED: %v", err)
	}
}

func TestMetadataIsReadOnlyAndStable(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, sampleProvider())
	statusInfo, err := fs.Stat(ctx, "/7/status")
	if err != nil {
		t.Fatal(err)
	}
	if statusInfo.IsDir() {
		t.Fatal("status 不应是目录")
	}
	if statusInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("虚拟文件不得可写: %v", statusInfo.Mode())
	}
	if statusInfo.Size() == 0 {
		t.Fatal("status 应有长度")
	}

	entries, err := fs.ReadDir(ctx, "/7")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if entryInfo.Name() != entry.Name() {
			t.Fatalf("目录项信息名字不符: %q vs %q", entryInfo.Name(), entry.Name())
		}
	}
}

func TestReadlinkRejectsNonLinks(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, sampleProvider())
	if _, err := fs.Readlink(ctx, "/7/status"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非链接应报 EINVAL: %v", err)
	}
	if _, err := fs.Readlink(ctx, "/7"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("目录应报 EINVAL: %v", err)
	}

	target, err := fs.Readlink(ctx, "/7/fd/1")
	if err != nil || target == "" {
		t.Fatalf("fd 链接=%q err=%v", target, err)
	}
}

func entryNames(entries []stdfs.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
