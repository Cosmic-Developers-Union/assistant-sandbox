package overlay

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"sort"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/readonly"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type fixture struct {
	overlay *Overlay
	upper   *memfs.FS
	lower   *memfs.FS
}

func newFixture(t *testing.T, lowerFiles map[string]string) *fixture {
	t.Helper()
	upper, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range sortedKeys(lowerFiles) {
		writeFile(t, lower, path, lowerFiles[path])
	}
	overlay, err := New(upper, lower)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{overlay: overlay, upper: upper, lower: lower}
}

func sortedKeys(files map[string]string) []string {
	keys := make([]string, 0, len(files))
	for name := range files {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

func writeFile(t *testing.T, system vmfs.System, path, content string) {
	t.Helper()
	ctx := context.Background()
	dir, _ := splitPath(path)
	ensureDirs(t, system, dir)
	file, err := system.Open(ctx, path, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
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

func ensureDirs(t *testing.T, system vmfs.System, path string) {
	t.Helper()
	if path == "/" || path == "" {
		return
	}
	parent, name := splitPath(path)
	ensureDirs(t, system, parent)
	if name == "" {
		return
	}

	if err := system.Mkdir(context.Background(), path, 0o755); err != nil && !errors.Is(err, errno.Exist) {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, system vmfs.System, path string) string {
	t.Helper()
	file, err := system.Open(context.Background(), path, vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReadFallsThroughToLower(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/etc/config": "from-lower"})
	if got := readFile(t, f.overlay, "/etc/config"); got != "from-lower" {
		t.Fatalf("内容=%q", got)
	}

	if _, err := f.upper.Lstat(ctx, "/etc/config"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("只读访问不得复制到 upper: %v", err)
	}
	info, err := f.overlay.Stat(ctx, "/etc/config")
	if err != nil || info.Size() != int64(len("from-lower")) {
		t.Fatalf("stat=%+v err=%v", info, err)
	}
}

func TestWriteCopiesUpAndLeavesLowerIntact(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/etc/config": "original"})
	file, err := f.overlay.Open(ctx, "/etc/config", vmfs.WriteOnly|vmfs.Truncate, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("modified")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.overlay, "/etc/config"); got != "modified" {
		t.Fatalf("合并视图应看到新内容: %q", got)
	}
	if got := readFile(t, f.lower, "/etc/config"); got != "original" {
		t.Fatalf("lower 必须保持不变: %q", got)
	}

	if got := readFile(t, f.upper, "/etc/config"); got != "modified" {
		t.Fatalf("upper 应有副本: %q", got)
	}
}

func TestOpenHandlesStayValidAcrossCopyUp(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/data": "0123456789"})

	handle, err := f.overlay.Open(ctx, "/data", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()

	file, err := f.overlay.Open(ctx, "/data", vmfs.WriteOnly|vmfs.Truncate, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("changed")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	buffer := make([]byte, 16)
	read, err := handle.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("旧句柄应仍可读: %v", err)
	}
	if read == 0 {
		t.Fatal("旧句柄应读到内容")
	}
	if got := readFile(t, f.overlay, "/data"); got != "changed" {
		t.Fatalf("新视图=%q", got)
	}
}

func TestRemoveShadowsLower(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/etc/config": "keep?", "/keep": "yes"})
	if err := f.overlay.Remove(ctx, "/etc/config"); err != nil {
		t.Fatal(err)
	}

	if _, err := f.overlay.Stat(ctx, "/etc/config"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("删除后应不可见: %v", err)
	}
	if _, err := f.overlay.Open(ctx, "/etc/config", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("删除后打开应报 ENOENT: %v", err)
	}

	if got := readFile(t, f.lower, "/etc/config"); got != "keep?" {
		t.Fatalf("lower 不得被删除: %q", got)
	}

	entries, err := f.overlay.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	names := entryNames(entries)
	if containsName(names, "etc") == false && containsName(names, "config") {
		t.Fatalf("被遮蔽项不得出现: %v", names)
	}
	if !containsName(names, "keep") {
		t.Fatalf("其它项应保留: %v", names)
	}

	for _, name := range names {
		if isWhiteoutName(name) {
			t.Fatalf("保留名不得可见: %v", names)
		}
	}

	if err := f.overlay.Remove(ctx, "/etc/config"); err != nil {
		t.Fatalf("重复删除应幂等: %v", err)
	}

	if err := f.overlay.Remove(ctx, "/nope"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("删除不存在应报 ENOENT: %v", err)
	}
}

func TestRecreateAfterRemoveClearsShadows(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/etc/config": "old"})
	if err := f.overlay.Remove(ctx, "/etc/config"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, f.overlay, "/etc/config", "new")
	if got := readFile(t, f.overlay, "/etc/config"); got != "new" {
		t.Fatalf("重建后应可见: %q", got)
	}

	if err := f.overlay.Remove(ctx, "/etc/config"); err != nil {
		t.Fatal(err)
	}
	file, err := f.overlay.Open(ctx, "/etc/config", vmfs.Create|vmfs.WriteOnly, 0o644)
	if err != nil {
		t.Fatalf("遮蔽后以 Create 打开应成功: %v", err)
	}
	if _, err := file.Write([]byte("created")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.overlay, "/etc/config"); got != "created" {
		t.Fatalf("Create 后内容=%q", got)
	}
}

func TestReadDirMergesLayers(t *testing.T) {
	ctx := context.Background()
	upper, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/shared", "/lower-only"} {
		writeFile(t, lower, path, "lower")
	}
	for _, path := range []string{"/shared", "/upper-only"} {
		writeFile(t, upper, path, "upper")
	}
	overlay, err := New(upper, lower)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := overlay.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	names := entryNames(entries)

	if !sort.StringsAreSorted(names) {
		t.Fatalf("目录项应按名排序: %v", names)
	}
	want := []string{"lower-only", "shared", "upper-only"}
	if len(names) != len(want) {
		t.Fatalf("目录项=%v want %v", names, want)
	}
	for index, name := range want {
		if names[index] != name {
			t.Fatalf("目录项=%v want %v", names, want)
		}
	}

	if got := readFile(t, overlay, "/shared"); got != "upper" {
		t.Fatalf("同名应取 upper: %q", got)
	}

	if _, err := overlay.ReadDir(ctx, "/shared"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("对文件读目录应报 EINVAL: %v", err)
	}
}

func TestRenameWithinView(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/from": "payload"})
	if err := f.overlay.Rename(ctx, "/from", "/to"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.overlay, "/to"); got != "payload" {
		t.Fatalf("目标内容=%q", got)
	}
	if _, err := f.overlay.Stat(ctx, "/from"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("来源应消失: %v", err)
	}

	if got := readFile(t, f.lower, "/from"); got != "payload" {
		t.Fatalf("lower 不得被改写: %q", got)
	}

	f2 := newFixture(t, map[string]string{"/src": "new", "/dst": "old"})
	if err := f2.overlay.Rename(ctx, "/src", "/dst"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f2.overlay, "/dst"); got != "new" {
		t.Fatalf("覆盖后内容=%q", got)
	}
}

func TestSymlinkAndLinkAcrossLayers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/real": "content"})
	if err := f.overlay.Symlink(ctx, "/real", "/alias"); err != nil {
		t.Fatal(err)
	}
	if target, err := f.overlay.Readlink(ctx, "/alias"); err != nil || target != "/real" {
		t.Fatalf("链接=%q err=%v", target, err)
	}

	if err := f.overlay.Link(ctx, "/real", "/hard"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.overlay, "/hard"); got != "content" {
		t.Fatalf("硬链接内容=%q", got)
	}
}

func TestMkdirAndParentsCopyUp(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/deep/inner/file": "deep", "/deep/other": "other"})

	writeFile(t, f.overlay, "/deep/inner/new", "fresh")
	if got := readFile(t, f.overlay, "/deep/inner/new"); got != "fresh" {
		t.Fatalf("内容=%q", got)
	}

	if _, err := f.upper.Lstat(ctx, "/deep/other"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("不应递归复制未访问的兄弟项: %v", err)
	}
	if got := readFile(t, f.overlay, "/deep/other"); got != "other" {
		t.Fatalf("兄弟内容应仍可读: %q", got)
	}

	if err := f.overlay.Mkdir(ctx, "/brand-new", 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := f.overlay.Stat(ctx, "/brand-new")
	if err != nil || !info.IsDir() {
		t.Fatalf("目录=%+v err=%v", info, err)
	}
}

func TestReservedNamesAreRejected(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/etc/config": "x"})
	if err := f.overlay.Remove(ctx, "/etc/config"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/.wh", "/.wh/etc_config"} {
		if _, err := f.overlay.Stat(ctx, path); !errors.Is(err, errno.Invalid) {
			t.Errorf("访问内部路径 %s 应报 EINVAL: %v", path, err)
		}
	}

	rootEntries, err := f.overlay.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if containsName(entryNames(rootEntries), whiteoutBase()) {
		t.Fatalf("内部命名空间不得可见: %v", entryNames(rootEntries))
	}

	entries, err := f.overlay.ReadDir(ctx, "/etc")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entryNames(entries) {
		if isWhiteoutName(name) {
			t.Fatalf("保留名不得可见: %v", entryNames(entries))
		}
	}
}

func TestReadOnlyLowerStaysReadOnly(t *testing.T) {
	ctx := context.Background()
	upper, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	image, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, image, "/etc/config", "image")
	lower, err := readonly.New(image)
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := New(upper, lower)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, overlay, "/etc/config"); got != "image" {
		t.Fatalf("读=%q", got)
	}

	file, err := overlay.Open(ctx, "/etc/config", vmfs.WriteOnly|vmfs.Truncate, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("session")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, overlay, "/etc/config"); got != "session" {
		t.Fatalf("写后=%q", got)
	}
	if got := readFile(t, image, "/etc/config"); got != "image" {
		t.Fatalf("镜像必须不变: %q", got)
	}

	if err := overlay.Remove(ctx, "/etc/config"); err != nil {
		t.Fatal(err)
	}
	if _, err := overlay.Stat(ctx, "/etc/config"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("删除后应不可见: %v", err)
	}
	if got := readFile(t, image, "/etc/config"); got != "image" {
		t.Fatalf("镜像必须不变: %q", got)
	}
}

func TestConstructionAndCancellation(t *testing.T) {
	if _, err := New(nil, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 层应报 EINVAL: %v", err)
	}
	f := newFixture(t, map[string]string{"/a": "1"})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.overlay.Open(cancelled, "/a", vmfs.ReadOnly, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := f.overlay.Stat(cancelled, "/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := f.overlay.Remove(cancelled, "/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := f.overlay.ReadDir(cancelled, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := f.overlay.Rename(cancelled, "/a", "/b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}

	if err := f.overlay.Remove(context.Background(), "/"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("删除根应报 EINVAL: %v", err)
	}

	if f.overlay.Upper() == nil || f.overlay.Lower() == nil {
		t.Fatal("Upper/Lower 应可见")
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

func TestLstatAndReadlinkRespectShadow(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/target": "body"})
	if err := f.overlay.Symlink(ctx, "/target", "/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.overlay.Lstat(ctx, "/link"); err != nil {
		t.Fatalf("Lstat 应成功: %v", err)
	}
	if err := f.overlay.Remove(ctx, "/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.overlay.Lstat(ctx, "/link"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("遮蔽后 Lstat 应报 ENOENT: %v", err)
	}
	if _, err := f.overlay.Readlink(ctx, "/link"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("遮蔽后 Readlink 应报 ENOENT: %v", err)
	}

	f2 := newFixture(t, map[string]string{"/target": "body"})
	if err := f2.overlay.Symlink(ctx, "/target", "/link"); err != nil {
		t.Fatal(err)
	}
	if target, err := f2.overlay.Readlink(ctx, "/link"); err != nil || target != "/target" {
		t.Fatalf("链接=%q err=%v", target, err)
	}
	_ = bytes.MinRead
}
