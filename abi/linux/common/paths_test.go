package common

import (
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestResolverResolvesGuestPaths 验证 I01:客体路径解析统一——绝对路径
// 直接使用,相对路径以工作目录为基准,"." 与 ".." 按词法消解并在客体
// 根处收敛,不逃逸授权根。
func TestResolverResolvesGuestPaths(t *testing.T) {
	resolver := NewResolver("/work/sub")
	cases := []struct {
		label string
		path  string
		want  string
	}{
		{"绝对路径", "/etc/passwd", "/etc/passwd"},
		{"绝对路径含 .", "/etc/./passwd", "/etc/passwd"},
		{"绝对路径含 ..", "/etc/../passwd", "/passwd"},
		{"相对路径", "file", "/work/sub/file"},
		{"当前目录", ".", "/work/sub"},
		{"点目录", "./", "/work/sub"},
		{"上跳一级", "..", "/work"},
		{"上跳两级", "../..", "/"},
		{"相对上跳", "sub/../..", "/work"},
		{"混合", "sub/../other", "/work/sub/other"},
		{"尾部斜杠", "sub/", "/work/sub/sub"},
		{"多次斜杠", "sub//file", "/work/sub/sub/file"},
	}
	for _, testCase := range cases {
		got, err := resolver.Resolve(testCase.path)
		if err != nil {
			t.Fatalf("%s: %v", testCase.label, err)
		}
		if got != testCase.want {
			t.Fatalf("%s: 解析=%q want %q", testCase.label, got, testCase.want)
		}
	}
}

// TestResolverRejectsInvalidPaths 验证 I01:空路径与含 NUL 的路径报
// EINVAL,超长路径报 ENAMETOOLONG。
func TestResolverRejectsInvalidPaths(t *testing.T) {
	resolver := NewResolver("/work")
	if _, err := resolver.Resolve(""); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空路径 err=%v want EINVAL", err)
	}
	if _, err := resolver.Resolve("a\x00b"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("含 NUL 的路径 err=%v want EINVAL", err)
	}
	if _, err := resolver.Resolve("/" + strings.Repeat("a", PathLimit)); !errors.Is(err, errno.NameTooLong) {
		t.Fatalf("超长路径 err=%v want ENAMETOOLONG", err)
	}
	// 根目录之上收敛,不逃逸客体根。
	got, err := resolver.Resolve("/../../..")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/" {
		t.Fatalf("越界路径=%q want \"/\"", got)
	}
}

// TestResolverFallsBackToRootForInvalidWorkdir 验证工作目录非法时以根
// 为基准,不产生非法路径。
func TestResolverFallsBackToRootForInvalidWorkdir(t *testing.T) {
	resolver := NewResolver("relative")
	got, err := resolver.Resolve("file")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/file" {
		t.Fatalf("解析=%q want \"/file\"", got)
	}
}

// TestResolverResolvesPairs 验证成对路径(改名、硬链)按同一规则解析。
func TestResolverResolvesPairs(t *testing.T) {
	resolver := NewResolver("/work")
	left, right, err := resolver.ResolvePair("a/../b", "/c/./d")
	if err != nil {
		t.Fatal(err)
	}
	if left != "/work/b" || right != "/c/d" {
		t.Fatalf("解析=%q/%q want /work/b 与 /c/d", left, right)
	}
	if _, _, err := resolver.ResolvePair("", "/c"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空路径 err=%v want EINVAL", err)
	}
}
