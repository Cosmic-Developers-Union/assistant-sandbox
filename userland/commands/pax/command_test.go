package paxcmd

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func makeArchive(t *testing.T, extra ...*tar.Header) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	entries := []struct {
		header  tar.Header
		content string
	}{
		{tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o755}, ""},
		{tar.Header{Name: "dir/hello.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 6}, "hello\n"},
		{tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "dir/hello.txt"}, ""},
		{tar.Header{Name: "hard", Typeflag: tar.TypeLink, Mode: 0o644, Linkname: "dir/hello.txt"}, ""},
	}
	for _, entry := range entries {
		if err := writer.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if entry.content != "" {
			if _, err := writer.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, header := range extra {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := writer.Write(bytes.Repeat([]byte("x"), int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func copyArchive(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(from, "archive.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "archive.tar"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeArchiveFile(t *testing.T, dir string, data []byte) string {
	t.Helper()
	name := filepath.Join(dir, "archive.tar")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFile(t, dir, makeArchive(t))

	result := commandtest.Run(t, Run, dir, []string{"-f", "archive.tar"}, "")
	want := "dir/\ndir/hello.txt\nlink\nhard\n"
	if result.Code != 0 || result.Stderr != "" || result.Stdout != want {
		t.Fatalf("列出 = (%q, %q, %d), want %q", result.Stdout, result.Stderr, result.Code, want)
	}

	result = commandtest.Run(t, Run, dir, []string{"-v", "-f", "archive.tar"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "drwxr-xr-x") ||
		!strings.Contains(result.Stdout, "hello.txt") || !strings.Contains(result.Stdout, "link -> dir/hello.txt") ||
		!strings.Contains(result.Stdout, "hard == dir/hello.txt") {
		t.Fatalf("详细列出 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}

	dest := t.TempDir()
	copyArchive(t, dir, dest)
	result = commandtest.Run(t, Run, dest, []string{"-r", "-f", "archive.tar"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("解包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "dir/hello.txt")); err != nil || string(data) != "hello\n" {
		t.Fatalf("文件内容 = (%q, %v)", data, err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "link")); err != nil || target != "dir/hello.txt" {
		t.Fatalf("符号链接 = (%q, %v)", target, err)
	}
	info, err := os.Stat(filepath.Join(dest, "hard"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(filepath.Join(dest, "dir/hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, original) {
		t.Fatal("硬链接没有指向同一个文件")
	}
	if info, err := os.Stat(filepath.Join(dest, "dir")); err != nil || !info.IsDir() {
		t.Fatalf("目录 = (%v, %v)", info, err)
	}

	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}

func TestExtractFromStdinAndPatterns(t *testing.T) {
	dir := t.TempDir()
	archive := string(makeArchive(t))
	result := commandtest.Run(t, Run, dir, []string{"-r", "dir/hello.txt"}, archive)
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("标准输入解包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "dir/hello.txt")); err != nil {
		t.Fatalf("选中的成员应落地: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "link")); err == nil {
		t.Fatal("未选中的成员不应落地")
	}

	if _, err := os.Stat(filepath.Join(dir, ".pax-0")); err == nil {
		t.Fatal("标准输入归档的临时文件应被清理")
	}
}

func TestKeepAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFile(t, dir, makeArchive(t))
	target := filepath.Join(dir, "dir/hello.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := commandtest.Run(t, Run, dir, []string{"-r", "-f", "archive.tar"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("默认覆盖 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "hello\n" {
		t.Fatalf("覆盖后 = (%q, %v)", data, err)
	}

	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"-r", "-k", "-f", "archive.tar"}, "")
	if result.Code != 0 {
		t.Fatalf("-k 应成功 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep\n" {
		t.Fatalf("-k 后 = (%q, %v)", data, err)
	}
}

func TestUnsafeArchivesAreRefusedWhole(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header *tar.Header
	}{
		{"穿越", &tar.Header{Name: "../escape.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4}},
		{"绝对路径", &tar.Header{Name: "/abs.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4}},
		{"FIFO", &tar.Header{Name: "fifo", Typeflag: tar.TypeFifo, Mode: 0o644}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeArchiveFile(t, dir, makeArchive(t, tc.header))
			result := commandtest.Run(t, Run, dir, []string{"-r", "-f", "archive.tar"}, "")
			if result.Code != 1 || result.Stderr == "" {
				t.Fatalf("不安全归档 = (%q, %q, %d), want 拒绝", result.Stdout, result.Stderr, result.Code)
			}

			if _, err := os.Stat(filepath.Join(dir, "dir/hello.txt")); err == nil {
				t.Fatal("不安全归档不应落地任何成员")
			}
			if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
				t.Fatal("穿越成员不应落地")
			}
		})
	}
}

func TestUpdateOnlyNewerMembers(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFile(t, dir, makeArchive(t))
	target := filepath.Join(dir, "dir/hello.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("newer\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := commandtest.Run(t, Run, dir, []string{"-r", "-u", "-f", "archive.tar"}, "")
	if result.Code != 0 {
		t.Fatalf("-u = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "newer\n" {
		t.Fatalf("-u 不该覆盖更新的目标 = (%q, %v)", data, err)
	}
}

func TestUnsupportedOptionsFailLoudly(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFile(t, dir, makeArchive(t))
	for _, args := range [][]string{
		{"-w", "-f", "archive.tar"},
		{"-r", "-a", "-f", "archive.tar"},
		{"-r", "-i", "-f", "archive.tar"},
		{"-r", "-s", "/a/b/", "-f", "archive.tar"},
		{"-r", "-p", "e", "-f", "archive.tar"},
		{"-r", "-o", "delete=pattern", "-f", "archive.tar"},
		{"-r", "-b", "5120", "-f", "archive.tar"},
		{"-r", "-l", "-f", "archive.tar"},
		{"-r", "-t", "-f", "archive.tar"},
		{"-r", "-X", "-f", "archive.tar"},
		{"-x", "bogus", "-f", "archive.tar"},
		{"--not-a-flag"},
	} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Errorf("pax %v = (%q, %q, %d), want 明确拒绝", args, result.Stdout, result.Stderr, result.Code)
		}
	}

	if result := commandtest.Run(t, Run, dir, []string{"-f", "absent.tar"}, ""); result.Code != 1 || result.Stderr == "" {
		t.Errorf("缺失归档 = (%+v), want 退出码 1 与诊断", result)
	}
}

func TestWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tree/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tree/a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tree/sub/b.txt"), []byte("beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "tree/link")); err != nil {
		t.Fatal(err)
	}

	result := commandtest.Run(t, Run, dir, []string{"-w", "-f", "out.tar", "tree"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("打包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if info, err := os.Stat(filepath.Join(dir, "out.tar")); err != nil || info.Size() == 0 {
		t.Fatalf("归档 = (%v, %v)", info, err)
	}

	result = commandtest.Run(t, Run, dir, []string{"-f", "out.tar"}, "")
	want := "tree/\ntree/a.txt\ntree/link\ntree/sub/\ntree/sub/b.txt\n"
	if result.Code != 0 || result.Stdout != want {
		t.Fatalf("列出打包结果 = (%q, %q, %d), want %q", result.Stdout, result.Stderr, result.Code, want)
	}

	dest := t.TempDir()
	archive, err := os.ReadFile(filepath.Join(dir, "out.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "out.tar"), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dest, []string{"-r", "-f", "out.tar"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("解包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "tree/sub/b.txt")); err != nil || string(data) != "beta\n" {
		t.Fatalf("子目录文件 = (%q, %v)", data, err)
	}
	if target, err := os.Readlink(filepath.Join(dest, "tree/link")); err != nil || target != "a.txt" {
		t.Fatalf("符号链接 = (%q, %v)", target, err)
	}
}

func TestWriteAppendAndNoDescend(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"one.txt": "1\n", "two.txt": "2\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"-w", "-f", "two.tar", "one.txt"}, "")
	if result.Code != 0 {
		t.Fatalf("首次打包 = %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-w", "-a", "-f", "two.tar", "two.txt"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("追加 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	result = commandtest.Run(t, Run, dir, []string{"-f", "two.tar"}, "")
	if result.Code != 0 || result.Stdout != "one.txt\ntwo.txt\n" {
		t.Fatalf("追加后列出 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}

	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tree/child.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"-w", "-d", "-f", "dirs.tar", "tree"}, "")
	if result.Code != 0 {
		t.Fatalf("-d 打包 = %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-f", "dirs.tar"}, "")
	if result.Code != 0 || result.Stdout != "tree/\n" {
		t.Fatalf("-d 只写目录自身 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
}

func TestWriteFollowSymlinkOperand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	result := commandtest.Run(t, Run, dir, []string{"-w", "-f", "link.tar", "link"}, "")
	if result.Code != 0 {
		t.Fatalf("默认打包 = %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-v", "-f", "link.tar"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "link -> target.txt") {
		t.Fatalf("默认应存符号链接 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}

	result = commandtest.Run(t, Run, dir, []string{"-w", "-H", "-f", "follow.tar", "link"}, "")
	if result.Code != 0 {
		t.Fatalf("-H 打包 = %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-v", "-f", "follow.tar"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "link\n") || strings.Contains(result.Stdout, "->") {
		t.Fatalf("-H 应存常规文件 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
}

func TestWriteToStdoutAndUstarLimits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := commandtest.Run(t, Run, dir, []string{"-w", "a.txt"}, "")
	if result.Code != 0 || !strings.HasPrefix(result.Stdout, "a.txt") {
		t.Fatalf("标准输出归档 = (%q, %q, %d)", result.Stdout[:min(20, len(result.Stdout))], result.Stderr, result.Code)
	}

	long := strings.Repeat("n", 120) + ".txt"
	if err := os.WriteFile(filepath.Join(dir, long), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"-w", "-x", "ustar", "-f", "long.tar", long}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("-x ustar 超长名 = (%q, %q, %d), want 如实报错", result.Stdout, result.Stderr, result.Code)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-w", "-x", "cpio", "-f", "x.cpio", "a.txt"}, ""); result.Code != 2 {
		t.Fatalf("-x cpio = %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-r", "-w", "a.txt"}, ""); result.Code != 2 {
		t.Fatalf("-r -w = %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-w", "-f", "empty.tar"}, ""); result.Code != 2 {
		t.Fatalf("写面无操作数 = %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-a", "-f", "x.tar", "a.txt"}, ""); result.Code != 2 {
		t.Fatalf("-a 不带 -w = %+v", result)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestWriteVerboseAndUstarFormat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-w", "-v", "-x", "ustar", "-f", "out.tar", "a.txt"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("打包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if !strings.Contains(result.Stdout, "a.txt") || !strings.Contains(result.Stdout, "-rw-r--r--") {
		t.Fatalf("-v 写面输出 = %q", result.Stdout)
	}

	result = commandtest.Run(t, Run, dir, []string{"-f", "out.tar"}, "")
	if result.Code != 0 || result.Stdout != "a.txt\n" {
		t.Fatalf("ustar 列出 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	dest := t.TempDir()
	data, err := os.ReadFile(filepath.Join(dir, "out.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "out.tar"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dest, []string{"-r", "-f", "out.tar"}, "")
	if result.Code != 0 {
		t.Fatalf("ustar 解包 = %+v", result)
	}
	if content, err := os.ReadFile(filepath.Join(dest, "a.txt")); err != nil || string(content) != "alpha\n" {
		t.Fatalf("ustar 解包内容 = (%q, %v)", content, err)
	}
}

func TestSelectionComplementAndFirst(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFile(t, dir, makeArchive(t))
	result := commandtest.Run(t, Run, dir, []string{"-f", "archive.tar", "*.txt"}, "")
	if result.Code != 0 || result.Stdout != "dir/hello.txt\n" {
		t.Fatalf("模式匹配 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	result = commandtest.Run(t, Run, dir, []string{"-c", "-f", "archive.tar", "*.txt"}, "")
	if result.Code != 0 || result.Stdout != "dir/\nlink\nhard\n" {
		t.Fatalf("-c 取反 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}

	result = commandtest.Run(t, Run, dir, []string{"-n", "-f", "archive.tar", "dir/*", "*.txt"}, "")
	if result.Code != 0 || result.Stdout != "dir/\ndir/hello.txt\n" {
		t.Fatalf("-n = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
}

func TestFollowAllSymlinksInTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tree/real.txt"), []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "tree/link.txt")); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-w", "-L", "-f", "follow.tar", "tree"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("-L 打包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	result = commandtest.Run(t, Run, dir, []string{"-v", "-f", "follow.tar"}, "")
	if result.Code != 0 || strings.Contains(result.Stdout, "->") || !strings.Contains(result.Stdout, "link.txt") {
		t.Fatalf("-L 应把符号链接存成常规文件 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
}

func TestExtractRestoresArchivedModes(t *testing.T) {
	dir := t.TempDir()
	writeArchiveFile(t, dir, makeArchive(t))
	target := filepath.Join(dir, "dir/hello.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-r", "-f", "archive.tar"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("解包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("覆盖后模式 = %v, %v（应为归档里的 0644）", info.Mode().Perm(), err)
	}

	if info, err := os.Lstat(filepath.Join(dir, "link")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("符号链接 = %v, %v", info.Mode(), err)
	}
}

func TestExtractRestoresReadOnlyDirectoryMode(t *testing.T) {
	dir := t.TempDir()
	archive := string(makeArchive(t,
		&tar.Header{Name: "ro/", Typeflag: tar.TypeDir, Mode: 0o555},
		&tar.Header{Name: "ro/inner.txt", Typeflag: tar.TypeReg, Mode: 0o400, Size: 3},
	))
	result := commandtest.Run(t, Run, dir, []string{"-r", "ro/*"}, archive)
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("解包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	inner := filepath.Join(dir, "ro/inner.txt")
	if data, err := os.ReadFile(inner); err != nil || string(data) != "xxx" {
		t.Fatalf("只读目录里的成员 = (%q, %v)，说明目录模式过早生效", data, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "ro")); err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("目录模式 = %v, %v（应为 0555）", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(inner); err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("成员模式 = %v, %v（应为 0400）", info.Mode().Perm(), err)
	}

	if err := os.Chmod(filepath.Join(dir, "ro"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRestoresTimestamps(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	archive := string(makeArchive(t, &tar.Header{
		Name: "timed.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 3, ModTime: stamp,
	}))
	result := commandtest.Run(t, Run, dir, []string{"-r", "timed.txt"}, archive)
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("解包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	info, err := os.Stat(filepath.Join(dir, "timed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Unix() != stamp.Unix() {
		t.Fatalf("mtime = %v，期望 %v", info.ModTime().UTC(), stamp)
	}

	result = commandtest.Run(t, Run, dir, []string{"-r", "dir/hello.txt"}, string(makeArchive(t)))
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("解包 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	info, err = os.Stat(filepath.Join(dir, "dir/hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Before(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("无时间戳成员被设成了 %v", info.ModTime().UTC())
	}
}
