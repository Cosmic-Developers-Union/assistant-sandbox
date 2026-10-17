package layer

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/readonly"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/image"
)

func newImage(t *testing.T) *image.Memory {
	t.Helper()
	img, err := image.New(image.ABIBuiltin, "/bin/init", map[string][]byte{
		"/bin/init":     []byte("init-bytes"),
		"/etc/rules":    []byte("rules"),
		"/usr/lib/a.so": []byte("shared"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func readAll(t *testing.T, system vmfs.System, path string) string {
	t.Helper()
	file, err := system.Open(context.Background(), path, vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatalf("打开 %s: %v", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	return string(data)
}

func TestNewLayerMaterializesNestedImagePaths(t *testing.T) {
	ctx := context.Background()
	layer, err := NewLayer(ctx, newImage(t), nil)
	if err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		"/bin/init":     "init-bytes",
		"/etc/rules":    "rules",
		"/usr/lib/a.so": "shared",
	} {
		if got := readAll(t, layer.Base(), path); got != want {
			t.Fatalf("%s=%q want %q", path, got, want)
		}
	}

	entries, err := layer.Base().ReadDir(ctx, "/usr/lib")
	if err != nil || len(entries) != 1 || entries[0].Name() != "a.so" {
		t.Fatalf("entries=%v err=%v", entries, err)
	}

	manifest, err := layer.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	imageManifest, err := newImage(t).Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Digest != imageManifest.Digest || manifest.Entry != "/bin/init" || manifest.ABI != image.ABIBuiltin {
		t.Fatalf("manifest=%+v", manifest)
	}
}

func TestBaseLayerRejectsEveryWriteOperation(t *testing.T) {
	ctx := context.Background()
	layer, err := NewLayer(ctx, newImage(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	base := layer.Base()

	if _, err := base.Open(ctx, "/bin/init", vmfs.ReadWrite, 0); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("写打开应报 EROFS: %v", err)
	}
	if _, err := base.Open(ctx, "/new", vmfs.Create|vmfs.WriteOnly, 0o644); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("创建应报 EROFS: %v", err)
	}
	if _, err := base.Open(ctx, "/bin/init", vmfs.Truncate|vmfs.WriteOnly, 0); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("截断应报 EROFS: %v", err)
	}
	if err := base.Mkdir(ctx, "/d", 0o755); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("Mkdir 应报 EROFS: %v", err)
	}
	if err := base.Remove(ctx, "/etc/rules"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("Remove 应报 EROFS: %v", err)
	}
	if err := base.Rename(ctx, "/etc/rules", "/moved"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("Rename 应报 EROFS: %v", err)
	}
	if err := base.Link(ctx, "/etc/rules", "/hard"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("Link 应报 EROFS: %v", err)
	}
	if err := base.Symlink(ctx, "/etc/rules", "/soft"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("Symlink 应报 EROFS: %v", err)
	}
	timestamps, ok := base.(vmfs.Timestamps)
	if !ok {
		t.Fatal("只读层必须保留时间戳能力接口")
	}
	if err := timestamps.SetTimes(ctx, "/etc/rules", 1, 2); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("SetTimes 应报 EROFS: %v", err)
	}

	if _, err := base.Stat(ctx, "/etc/rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Lstat(ctx, "/etc/rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Readlink(ctx, "/etc/rules"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非常规链接应报 EINVAL: %v", err)
	}
}

func TestSessionWritesDoNotChangeBaseOrImage(t *testing.T) {
	ctx := context.Background()
	image := newImage(t)
	layer, err := NewLayer(ctx, image, nil)
	if err != nil {
		t.Fatal(err)
	}
	file, err := layer.Session().Open(ctx, "/etc/rules", vmfs.Truncate|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("rewritten")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, layer.Base(), "/etc/rules"); got != "rules" {
		t.Fatalf("会话写不应改变基础层: %q", got)
	}
	manifest, err := image.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := newImage(t).Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Digest != again.Digest {
		t.Fatal("镜像摘要不应被会话写改变")
	}

	if err := layer.Session().Mkdir(ctx, "/work", 0o755); err != nil {
		t.Fatal(err)
	}
	extra, err := layer.Session().Open(ctx, "/work/note", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extra.Write([]byte("note")); err != nil {
		t.Fatal(err)
	}
	if err := extra.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, layer.Session(), "/work/note"); got != "note" {
		t.Fatalf("会话新增文件: %q", got)
	}
}

func TestLayerSnapshotAndRestoreResetSessionOnly(t *testing.T) {
	ctx := context.Background()
	layer, err := NewLayer(ctx, newImage(t), &clock.Virtual{})
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, layer, "/etc/rules", "first")
	snapshot, err := layer.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, layer, "/etc/rules", "second")
	writeSession(t, layer, "/extra", "temp")
	if err := layer.Restore(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, layer.Session(), "/etc/rules"); got != "first" {
		t.Fatalf("会话未回滚: %q", got)
	}
	if _, err := layer.Session().Stat(ctx, "/extra"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("回滚后新增文件应消失: %v", err)
	}

	if got := readAll(t, layer.Base(), "/etc/rules"); got != "rules" {
		t.Fatalf("基础层不应随会话回滚: %q", got)
	}

	if err := layer.Restore(ctx, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 快照应报 EINVAL: %v", err)
	}
}

func TestNewLayerRejectsInvalidInputs(t *testing.T) {
	ctx := context.Background()
	if _, err := NewLayer(ctx, nil, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 镜像应报 EINVAL: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := NewLayer(cancelled, newImage(t), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}

	cancelling, cancelDuring := context.WithCancel(ctx)
	cancelDuring()
	if _, err := NewLayer(cancelling, newImage(t), &clock.Virtual{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("物化中取消应中止: %v", err)
	}

	layer, err := NewLayer(ctx, newImage(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := layer.Base().Stat(ctx, "/bin/init")
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().UnixNano() != 0 {
		t.Fatalf("逻辑时钟应从零开始: %v", info.ModTime())
	}
}

func writeSession(t *testing.T, layer *Layer, path, content string) {
	t.Helper()
	file, err := layer.Session().Open(context.Background(), path, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatalf("会话写入 %s: %v", path, err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

type stubImage struct {
	paths   []string
	content map[string]string
	failOn  string
}

func (s *stubImage) Paths() []string { return s.paths }

func (s *stubImage) Open(_ context.Context, path string) (io.ReadCloser, error) {
	if path == s.failOn {
		return nil, errno.NotFound
	}
	content, ok := s.content[path]
	if !ok {
		return nil, errno.NotFound
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (s *stubImage) Manifest(context.Context) (image.Manifest, error) {
	return image.Manifest{Digest: "sha256:stub", ABI: image.ABIBuiltin, Entry: "/bin/init"}, nil
}

func TestMaterializeAbortsOnImageReadFailure(t *testing.T) {
	stub := &stubImage{
		paths:   []string{"/a.txt", "/b.txt"},
		content: map[string]string{"/a.txt": "a"},
		failOn:  "/b.txt",
	}
	if _, err := NewLayer(context.Background(), stub, &clock.Virtual{}); !errors.Is(err, errno.NotFound) {
		t.Fatalf("镜像读取失败应中止装配: %v", err)
	}
}

func TestLayerRejectsImagePathsTheSessionLayerCannotHold(t *testing.T) {
	tooLong := "/" + strings.Repeat("d", 3000) + "/" + strings.Repeat("n", 3000)
	stub := &stubImage{paths: []string{tooLong}, content: map[string]string{tooLong: "x"}}
	if _, err := NewLayer(context.Background(), stub, &clock.Virtual{}); err == nil {
		t.Fatal("超长镜像路径必须报错")
	}
}

func TestMaterializeStopsWhenContextIsCancelledDuringCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stub := &stubImage{
		paths:   []string{"/a.txt", "/b.txt"},
		content: map[string]string{"/a.txt": "a", "/b.txt": "b"},
	}
	if _, err := NewLayer(ctx, stub, &clock.Virtual{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应中止物化: %v", err)
	}
}

func TestMemoryRejectsCancelledContext(t *testing.T) {
	image := newImage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := image.Manifest(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("清单应响应取消: %v", err)
	}
	if _, err := image.Open(ctx, "/bin/init"); !errors.Is(err, context.Canceled) {
		t.Fatalf("读取应响应取消: %v", err)
	}
}

type stubTarget struct {
	openErr   error
	mkdirErr  error
	openCalls int
}

func (s *stubTarget) Open(context.Context, string, uint32, fs.FileMode) (vmfs.File, error) {
	s.openCalls++
	return nil, s.openErr
}
func (s *stubTarget) Stat(context.Context, string) (fs.FileInfo, error) {
	return nil, errno.NotFound
}
func (s *stubTarget) Lstat(context.Context, string) (fs.FileInfo, error) {
	return nil, errno.NotFound
}
func (s *stubTarget) ReadDir(context.Context, string) ([]fs.DirEntry, error) { return nil, nil }
func (s *stubTarget) Mkdir(context.Context, string, fs.FileMode) error {
	return s.mkdirErr
}
func (s *stubTarget) Remove(context.Context, string) error          { return nil }
func (s *stubTarget) Rename(context.Context, string, string) error  { return nil }
func (s *stubTarget) Link(context.Context, string, string) error    { return nil }
func (s *stubTarget) Symlink(context.Context, string, string) error { return nil }
func (s *stubTarget) Readlink(context.Context, string) (string, error) {
	return "", errno.Invalid
}

func TestMaterializeReportsTargetFailures(t *testing.T) {
	stub := &stubImage{paths: []string{"/etc/note"}, content: map[string]string{"/etc/note": "x"}}

	if err := materialize(context.Background(), stub, &stubTarget{mkdirErr: errno.Permission}); !errors.Is(err, errno.Permission) {
		t.Fatalf("目录建立失败应上报: %v", err)
	}

	target := &stubTarget{openErr: errno.NoSpace}
	if err := materialize(context.Background(), stub, target); !errors.Is(err, errno.NoSpace) {
		t.Fatalf("文件写入失败应上报: %v", err)
	}
	if target.openCalls == 0 {
		t.Fatal("应在建立目录后尝试打开目标文件")
	}
}

type stagedContext struct {
	context.Context
	remaining int
}

func (c *stagedContext) Err() error {
	if c.remaining <= 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

func TestMaterializeChecksCancellationInsideLoop(t *testing.T) {
	stub := &stubImage{
		paths:   []string{"/a.txt", "/b.txt"},
		content: map[string]string{"/a.txt": "a", "/b.txt": "b"},
	}
	ctx := &stagedContext{Context: context.Background(), remaining: 2}
	if _, err := NewLayer(ctx, stub, &clock.Virtual{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("循环内取消应中止物化: %v", err)
	}
}

func TestNewLayerAbortsWhenStorageConstructionFails(t *testing.T) {
	originalStore, originalView := newMemoryStore, newReadOnlyView
	t.Cleanup(func() { newMemoryStore, newReadOnlyView = originalStore, originalView })

	newMemoryStore = func(clock.Source) (*memfs.FS, error) { return nil, errno.NoSpace }
	if _, err := NewLayer(context.Background(), newImage(t), nil); !errors.Is(err, errno.NoSpace) {
		t.Fatalf("基础层构造失败应中止: %v", err)
	}

	calls := 0
	newMemoryStore = func(clk clock.Source) (*memfs.FS, error) {
		calls++
		if calls == 2 {
			return nil, errno.NoSpace
		}
		return originalStore(clk)
	}
	if _, err := NewLayer(context.Background(), newImage(t), nil); !errors.Is(err, errno.NoSpace) {
		t.Fatalf("会话层构造失败应中止: %v", err)
	}

	newMemoryStore = originalStore
	newReadOnlyView = func(*memfs.FS) (*readonly.FS, error) { return nil, errno.Invalid }
	if _, err := NewLayer(context.Background(), newImage(t), nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("只读视图构造失败应中止: %v", err)
	}
}

func TestLayerSurfacesImageBuilderProvenance(t *testing.T) {
	ctx := context.Background()
	img := newImage(t)
	img.DeclareBuilder("registry-v1", "bash")
	layer, err := NewLayer(ctx, img, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := layer.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Registry != "registry-v1" || manifest.Language != "bash" {
		t.Fatalf("装配层未透出构建来源: %+v", manifest)
	}

	direct, err := img.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Digest != direct.Digest {
		t.Fatalf("装配不得改变内容摘要: %s vs %s", manifest.Digest, direct.Digest)
	}
}
