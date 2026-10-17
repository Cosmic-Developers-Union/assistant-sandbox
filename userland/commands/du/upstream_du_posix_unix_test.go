//go:build linux || darwin || freebsd || netbsd || openbsd

package ducmd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPOSIXDefaultUsesApparentSizeIn512ByteUnits(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	const size = 5000
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runToolAt(t, dir, "f")
	if want := strconv.Itoa((size + 511) / 512); code != 0 || errb != "" || out != want+" f\n" {
		t.Fatalf("du f = (%q, %q, %d), want %s f（按大小折算 512 字节单位）", out, errb, code, want)
	}
	out, errb, code = runToolAt(t, dir, "-k", "f")
	if want := strconv.Itoa((size + 1023) / 1024); code != 0 || errb != "" || out != want+" f\n" {
		t.Fatalf("du -k f = (%q, %q, %d), want %s f（1024 字节单位）", out, errb, code, want)
	}
	out, errb, code = runToolAt(t, dir, "-b", "f")
	if code != 0 || errb != "" || out != strconv.Itoa(size)+" f\n" {
		t.Fatalf("du -b f = (%q, %q, %d), want %d f（原始字节）", out, errb, code, size)
	}
}

func TestPOSIXOneFileSystemExcludesCrossDeviceNonDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	foreignInfo, err := os.Stat("/dev/null")
	if err != nil {
		t.Skipf("/dev/null unavailable: %v", err)
	}
	rootDev, rootOK := fileDev(rootInfo)
	foreignDev, foreignOK := fileDev(foreignInfo)
	if !rootOK || !foreignOK || rootDev == foreignDev {
		t.Skip("test needs /dev/null on a different device")
	}
	if err := os.Symlink("/dev/null", filepath.Join(root, "foreign")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	out, errb, code := runToolAt(t, dir, "-aLx", "root")
	if code != 0 || errb != "" {
		t.Fatalf("du -aLx root = (%q, %q, %d)", out, errb, code)
	}
	if strings.Contains(out, "root/foreign") {
		t.Fatalf("cross-device non-directory was evaluated: %q", out)
	}
}

func TestPOSIXHardLinksAreNotDeduplicated(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "first")
	if err := os.WriteFile(first, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, filepath.Join(root, "second")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	out, errb, code := runToolAt(t, dir, "-ab", "root")
	if code != 0 || errb != "" {
		t.Fatalf("du -ab root = (%q, %q, %d)", out, errb, code)
	}
	if got := strings.Count(out, "root/first") + strings.Count(out, "root/second"); got != 2 {
		t.Fatalf("硬链接应各算一次（客体不去重），得到 %d 次: %q", got, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("du -ab root lines=%v, want 两个文件与 root 汇总", lines)
	}
}
