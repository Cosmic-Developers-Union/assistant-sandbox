package rootfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/image"
)

func readFile(t *testing.T, root *Root, name string) string {
	t.Helper()
	file, err := root.System.Open(context.Background(), name, 0, 0)
	if err != nil {
		t.Fatalf("打开 %s: %v", name, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取 %s: %v", name, err)
	}
	return string(data)
}

// TestBuildCreatesStandardLayout 验证 N02:标准目录、权限、基础文件与
// 工作区可重复生成,清单摘要稳定。
func TestBuildCreatesStandardLayout(t *testing.T) {
	ctx := context.Background()
	first, err := Build(ctx, Options{Clock: &clock.Virtual{}, Workdir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(ctx, Options{Clock: &clock.Virtual{}, Workdir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() != second.Digest() {
		t.Fatalf("两次构建摘要不同: %s vs %s", first.Digest(), second.Digest())
	}
	for _, dir := range []string{"/bin", "/lib", "/usr", "/usr/bin", "/etc", "/tmp", "/proc", "/dev", "/work"} {
		info, err := first.System.Stat(ctx, dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("目录 %s: %v", dir, err)
		}
	}
	tmp, err := first.System.Stat(ctx, "/tmp")
	if err != nil || tmp.Mode().Perm() != 0o777 || tmp.Mode()&fs.ModeSticky == 0 {
		t.Fatalf("/tmp 权限=%v err=%v want 01777", tmp.Mode(), err)
	}
	if got := readFile(t, first, "/etc/os-release"); got == "" {
		t.Fatal("/etc/os-release 为空")
	}
	if first.Manifest.Architecture != image.ArchitectureRV64 || first.Manifest.ABI != image.ABILinuxRV64 {
		t.Fatalf("清单 arch/abi=%s/%s", first.Manifest.Architecture, first.Manifest.ABI)
	}
	paths := make([]string, 0, len(first.Manifest.Files))
	for _, file := range first.Manifest.Files {
		paths = append(paths, file.Path)
	}
	if !slices.Contains(paths, "/work") || !slices.Contains(paths, "/etc/os-release") {
		t.Fatalf("清单缺少布局项: %v", paths)
	}
}

// TestBuildMaterializesImageWithDeclaredModes 验证 N02:镜像内容按清单
// 权限物化,可执行入口保留执行位。
func TestBuildMaterializesImageWithDeclaredModes(t *testing.T) {
	ctx := context.Background()
	content, err := image.New(image.ABILinuxRV64, "/bin/tool", map[string][]byte{
		"/bin/tool":      []byte("tool-bytes"),
		"/usr/share/doc": []byte("doc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := Build(ctx, Options{Clock: &clock.Virtual{}, Image: content, Entry: "/bin/tool"})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, root, "/bin/tool"); got != "tool-bytes" {
		t.Fatalf("镜像文件内容=%q", got)
	}
	info, err := root.System.Stat(ctx, "/bin/tool")
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("/bin/tool 权限=%v err=%v", info.Mode(), err)
	}
	doc, err := root.System.Stat(ctx, "/usr/share/doc")
	if err != nil || doc.Mode().Perm() != 0o644 {
		t.Fatalf("/usr/share/doc 权限=%v err=%v", doc.Mode(), err)
	}
	if root.Manifest.Entry != "/bin/tool" {
		t.Fatalf("入口=%q", root.Manifest.Entry)
	}
}

// TestBuildRejectsInvalidWorkdir 验证 N02:非法工作目录明确失败。
func TestBuildRejectsInvalidWorkdir(t *testing.T) {
	if _, err := Build(context.Background(), Options{Workdir: "relative"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("相对工作目录 err=%v", err)
	}
}

// TestBuildWritesIdentityEnvironmentAndLocale 验证 N04:passwd/group、
// HOME、PATH 与 UTF-8 locale 由根文件系统与默认环境提供,用户目录存在。
func TestBuildWritesIdentityEnvironmentAndLocale(t *testing.T) {
	root, err := Build(context.Background(), Options{
		Clock:   &clock.Virtual{},
		Workdir: "/work",
		User:    User{Name: "bob", UID: 2000, GID: 2001, Home: "/home/bob", Shell: "/bin/sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	passwd := readFile(t, root, "/etc/passwd")
	if !strings.Contains(passwd, "bob:x:2000:2001:bob:/home/bob:/bin/sh\n") {
		t.Fatalf("passwd=%q", passwd)
	}
	group := readFile(t, root, "/etc/group")
	if !strings.Contains(group, "bob:x:2001:\n") {
		t.Fatalf("group=%q", group)
	}
	if info, err := root.System.Stat(context.Background(), "/home/bob"); err != nil || !info.IsDir() {
		t.Fatalf("用户目录: %v", err)
	}
	environment := root.Environment()
	if environment["HOME"] != "/home/bob" || environment["USER"] != "bob" ||
		environment["PATH"] != DefaultPath || environment["LANG"] != "C.UTF-8" {
		t.Fatalf("环境=%v", environment)
	}
	if !strings.Contains(readFile(t, root, "/etc/profile"), "export LANG=C.UTF-8") {
		t.Fatal("/etc/profile 缺少 LANG")
	}
}
