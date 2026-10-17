package overlay

import (
	"context"
	"errors"
	stdfs "io/fs"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func TestCopyUpSymlinkAndDirectory(t *testing.T) {
	ctx := context.Background()
	upper, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, lower, "/real", "body")
	if err := lower.Symlink(ctx, "/real", "/alias"); err != nil {
		t.Fatal(err)
	}
	if err := lower.Mkdir(ctx, "/restricted", 0o700); err != nil {
		t.Fatal(err)
	}
	overlay, err := New(upper, lower)
	if err != nil {
		t.Fatal(err)
	}

	if err := overlay.copyUpParents(ctx, "/restricted/file"); err != nil {
		t.Fatal(err)
	}
	if err := overlay.copyUp(ctx, "/restricted", vmfs.ReadWrite, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := upper.Lstat(ctx, "/restricted")
	if err != nil || !info.IsDir() {
		t.Fatalf("目录应被复制: %+v err=%v", info, err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("目录权限应保留: %v", info.Mode().Perm())
	}

	if err := overlay.copyUp(ctx, "/alias", vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	target, err := upper.Readlink(ctx, "/alias")
	if err != nil || target != "/real" {
		t.Fatalf("链接应被复制且保留目标: %q err=%v", target, err)
	}
}

func TestCopyUpAcrossReadOnlyLower(t *testing.T) {
	ctx := context.Background()
	upper, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lower, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}

	payload := strings.Repeat("abcd", 32*1024)
	writeFile(t, lower, "/big", payload)
	overlay, err := New(upper, lower)
	if err != nil {
		t.Fatal(err)
	}
	if err := overlay.copyUp(ctx, "/big", vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, overlay, "/big"); got != payload {
		t.Fatalf("大文件复制内容长度=%d want %d", len(got), len(payload))
	}
}

func TestWritePathRejectsFileParent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/blocker": "i am a file"})
	_, err := f.overlay.Open(ctx, "/blocker/child", vmfs.Create|vmfs.WriteOnly, 0o644)
	if !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("父级是文件应报 ENOTDIR: %v", err)
	}

	if got := readFile(t, f.lower, "/blocker"); got != "i am a file" {
		t.Fatalf("镜像不得被改写: %q", got)
	}
}

func TestCopyUpAbsentPathIsNoop(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	if err := f.overlay.copyUp(ctx, "/nothing", vmfs.ReadWrite, 0o644); err != nil {
		t.Fatalf("不存在的路径复制应为空操作: %v", err)
	}
	if _, err := f.upper.Lstat(ctx, "/nothing"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("空操作不得创建条目: %v", err)
	}

	if err := f.overlay.copyUp(ctx, "/", vmfs.ReadWrite, 0o644); err != nil {
		t.Fatalf("根路径复制应为空操作: %v", err)
	}
}

func TestWhiteoutPlacementIsIdempotentAndClearable(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/gone": "x"})
	if err := f.overlay.placeWhiteout(ctx, "/gone"); err != nil {
		t.Fatal(err)
	}
	marker := whiteoutPath("/gone")
	if _, err := f.upper.Lstat(ctx, marker); err != nil {
		t.Fatalf("标记应存在: %v", err)
	}

	if err := f.overlay.placeWhiteout(ctx, "/gone"); err != nil {
		t.Fatalf("重复放置应幂等: %v", err)
	}

	if err := f.overlay.clearWhiteout(ctx, "/gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.upper.Lstat(ctx, marker); !errors.Is(err, errno.NotFound) {
		t.Fatalf("标记应被清除: %v", err)
	}
	if err := f.overlay.clearWhiteout(ctx, "/gone"); err != nil {
		t.Fatalf("重复清除应幂等: %v", err)
	}

	if err := f.overlay.placeWhiteout(ctx, "/"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("遮蔽根应报 EINVAL: %v", err)
	}
}

func TestPathEncodingAndReservedDetection(t *testing.T) {
	for path, want := range map[string]string{
		"/a":               "a",
		"/a/b":             "a_b",
		"/etc/config":      "etc_config",
		"/deep/inner/file": "deep_inner_file",
	} {
		if got := encodePath(path); got != want {
			t.Errorf("encodePath(%q)=%q want %q", path, got, want)
		}
	}

	if encodePath("/a_b") == encodePath("/a/b") {
		t.Fatal("编码存在歧义：/a_b 与 /a/b 同名")
	}

	if encodePath("/a/b_c") == encodePath("/a_b/c") {
		t.Fatal("编码存在歧义：/a/b_c 与 /a_b/c 同名")
	}

	if !pathReserved("/.wh") || !pathReserved("/.wh/x") {
		t.Fatal("内部命名空间应被判为保留")
	}
	if pathReserved("/white") || pathReserved("/.why") {
		t.Fatal("普通路径不得被判为保留")
	}
	if !pathReserved("/file" + renamedSuffix) {
		t.Fatal("改名临时名应被判为保留")
	}
	if !isRenamedTemp("x"+renamedSuffix) || isRenamedTemp("x") {
		t.Fatal("改名临时名判定不符")
	}
	if !hasPrefix("abc", "ab") || hasPrefix("a", "ab") {
		t.Fatal("hasPrefix 判定不符")
	}
	if whiteoutBase() != ".wh" {
		t.Fatalf("遮蔽目录基名=%q", whiteoutBase())
	}
}

func TestShadowYieldsToUpperEntity(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/path": "lower"})
	if err := f.overlay.placeWhiteout(ctx, "/path"); err != nil {
		t.Fatal(err)
	}
	if masked, err := f.overlay.isWhiteout(ctx, "/path"); err != nil || !masked {
		t.Fatalf("仅有遮蔽时应对 lower 生效: %v %v", masked, err)
	}

	writeFile(t, f.upper, "/path", "upper")
	if masked, err := f.overlay.isWhiteout(ctx, "/path"); err != nil || masked {
		t.Fatalf("upper 有实体时遮蔽应让位: %v %v", masked, err)
	}
	if got := readFile(t, f.overlay, "/path"); got != "upper" {
		t.Fatalf("应看到 upper 内容: %q", got)
	}
}

func TestStatAndLstatFollowSemantics(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/real": "body"})
	if err := f.overlay.Symlink(ctx, "/real", "/alias"); err != nil {
		t.Fatal(err)
	}
	stat, err := f.overlay.Stat(ctx, "/alias")
	if err != nil {
		t.Fatal(err)
	}
	if stat.IsDir() {
		t.Fatal("Stat 应跟随链接到常规文件")
	}
	lstat, err := f.overlay.Lstat(ctx, "/alias")
	if err != nil {
		t.Fatal(err)
	}
	if lstat.Mode()&stdfs.ModeSymlink == 0 {
		t.Fatalf("Lstat 应报告符号链接: %v", lstat.Mode())
	}
}

func TestCrossLayerSymlinkResolution(t *testing.T) {
	ctx := context.Background()

	f := newFixture(t, map[string]string{"/target": "body"})
	if err := f.overlay.Symlink(ctx, "/target", "/up"); err != nil {
		t.Fatal(err)
	}
	info, err := f.overlay.Stat(ctx, "/up")
	if err != nil {
		t.Fatalf("跨层链接应可解析: %v", err)
	}
	if info.IsDir() || info.Size() != int64(len("body")) {
		t.Fatalf("应解析到目标文件: %+v", info)
	}

	f2 := newFixture(t, map[string]string{"/real": "lower-body"})
	if err := f2.lower.Symlink(ctx, "/real", "/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.overlay.Stat(ctx, "/link"); err != nil {
		t.Fatalf("lower 链接应可解析: %v", err)
	}

	f3 := newFixture(t, map[string]string{"/dir/target": "rel"})
	if err := f3.overlay.Symlink(ctx, "target", "/dir/rel"); err != nil {
		t.Fatal(err)
	}
	relInfo, err := f3.overlay.Stat(ctx, "/dir/rel")
	if err != nil {
		t.Fatalf("相对链接应可解析: %v", err)
	}
	if relInfo.Size() != int64(len("rel")) {
		t.Fatalf("相对链接目标=%+v", relInfo)
	}
}

func TestSymlinkLoopIsReported(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)

	if err := f.overlay.Symlink(ctx, "/self", "/self"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.overlay.Stat(ctx, "/self"); !errors.Is(err, errno.LinkLoop) {
		t.Fatalf("自引用应报 ELOOP: %v", err)
	}

	f2 := newFixture(t, nil)
	if err := f2.overlay.Symlink(ctx, "/b", "/a"); err != nil {
		t.Fatal(err)
	}
	if err := f2.overlay.Symlink(ctx, "/a", "/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.overlay.Stat(ctx, "/a"); !errors.Is(err, errno.LinkLoop) {
		t.Fatalf("互引用应报 ELOOP: %v", err)
	}
}

func TestWritePathOnUpperOnlyFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	writeFile(t, f.upper, "/only-upper", "content")

	file, err := f.overlay.Open(ctx, "/only-upper", vmfs.WriteOnly|vmfs.Append, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("!")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.overlay, "/only-upper"); got != "content!" {
		t.Fatalf("追加内容=%q", got)
	}
}

func TestLinkIntoSameLayer(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/source": "data"})
	if err := f.overlay.Link(ctx, "/source", "/hard"); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, f.overlay, "/source"); got != "data" {
		t.Fatalf("来源=%q", got)
	}
	if got := readFile(t, f.overlay, "/hard"); got != "data" {
		t.Fatalf("链接=%q", got)
	}

	if _, err := f.upper.Lstat(ctx, "/hard"); err != nil {
		t.Fatalf("链接应存在于 upper: %v", err)
	}
}

func TestDirReadDirOnShadowedDirectory(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/dir/a": "1", "/dir/b": "2"})
	if err := f.overlay.Remove(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.overlay.ReadDir(ctx, "/dir"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("被遮蔽目录应报 ENOENT: %v", err)
	}
	if got := readFile(t, f.lower, "/dir/a"); got != "1" {
		t.Fatalf("镜像内容不得被删除: %q", got)
	}
}

func TestMkdirClearsShadowOnSamePath(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, map[string]string{"/thing": "was a file"})
	if err := f.overlay.Remove(ctx, "/thing"); err != nil {
		t.Fatal(err)
	}
	if err := f.overlay.Mkdir(ctx, "/thing", 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := f.overlay.Stat(ctx, "/thing")
	if err != nil || !info.IsDir() {
		t.Fatalf("应建出目录: %+v err=%v", info, err)
	}

	if _, err := f.upper.Lstat(ctx, whiteoutPath("/thing")); !errors.Is(err, errno.NotFound) {
		t.Fatalf("遮蔽应被清除: %v", err)
	}
}
