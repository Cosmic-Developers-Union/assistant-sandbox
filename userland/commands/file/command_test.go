package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func writeFixture(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileClassifiesDeclaredSignatures(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		want  string
	}{
		{"empty.bin", nil, "empty"},
		{"elf.so", append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, make([]byte, 16)...), "ELF 64-bit LSB"},
		{"elf32.o", append([]byte{0x7f, 'E', 'L', 'F', 1, 1, 1, 0}, make([]byte, 16)...), "ELF 32-bit LSB"},
		{"script.sh", []byte("#!/bin/bash\necho hi\n"), "a /bin/bash script text executable"},
		{"data.gz", []byte{0x1f, 0x8b, 0x08, 0}, "gzip compressed data"},
		{"data.bz2", []byte{'B', 'Z', 'h', '9'}, "bzip2 compressed data"},
		{"data.zst", []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00}, "zstd compressed data"},
		{"data.xz", []byte{0xfd, '7', 'z', 'X', 'Z', 0x00, 0x00}, "xz compressed data"},
		{"plain.txt", []byte("hello world\n"), "ASCII text"},
		{"utf8.txt", []byte("héllo wörld\n"), "UTF-8 Unicode text"},
		{"binary.bin", []byte{0x00, 0x01, 0x02}, "data"},
		{"weird.bin", []byte{0x07, 0x08, 0x09}, "data"},
	}
	for _, tc := range cases {
		dir := t.TempDir()

		writeFixture(t, dir, tc.name, tc.bytes)
		r := commandtest.Run(t, Run, dir, []string{tc.name}, "")
		if r.Code != 0 || !strings.Contains(r.Stdout, tc.name+": ") || !strings.Contains(r.Stdout, tc.want) {
			t.Fatalf("%s = %+v（期望含 %q）", tc.name, r, tc.want)
		}
	}
}

func TestFileTarAndAr(t *testing.T) {
	dir := t.TempDir()
	tar := make([]byte, 262)
	copy(tar[257:], "ustar")
	writeFixture(t, dir, "bundle.tar", tar)
	if r := commandtest.Run(t, Run, dir, []string{"bundle.tar"}, ""); r.Code != 0 || !strings.Contains(r.Stdout, "POSIX tar archive") {
		t.Fatalf("tar = %+v", r)
	}
	writeFixture(t, dir, "lib.a", []byte("!<arch>\n"))
	if r := commandtest.Run(t, Run, dir, []string{"lib.a"}, ""); r.Code != 0 || !strings.Contains(r.Stdout, "current ar archive") {
		t.Fatalf("ar = %+v", r)
	}
}

func TestFileSymlinkPolicies(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "real.txt", []byte("content\n"))

	if r := commandtest.Run(t, Run, dir, []string{"real.txt"}, ""); r.Code != 0 || !strings.Contains(r.Stdout, "ASCII text") {
		t.Fatalf("普通文件 = %+v", r)
	}
	if r := commandtest.Run(t, Run, dir, []string{"-L", "real.txt"}, ""); r.Code != 0 {
		t.Fatalf("-L = %+v", r)
	}
	if r := commandtest.Run(t, Run, dir, []string{"missing"}, ""); r.Code != 1 || !strings.Contains(r.Stderr, "missing") {
		t.Fatalf("缺失 = %+v", r)
	}
	if r := commandtest.Run(t, Run, dir, nil, ""); r.Code != 2 {
		t.Fatalf("缺操作数 = %+v", r)
	}
	if r := commandtest.Run(t, Run, dir, []string{"--bogus"}, ""); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func TestFileSymlinkAndDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "real.txt", []byte("content\n"))
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if r := commandtest.Run(t, Run, dir, []string{"link.txt"}, ""); r.Code != 0 ||
		!strings.Contains(r.Stdout, "symbolic link to") {
		t.Fatalf("-h 链接 = %+v", r)
	}

	if r := commandtest.Run(t, Run, dir, []string{"-L", "link.txt"}, ""); r.Code != 0 ||
		!strings.Contains(r.Stdout, "ASCII text") {
		t.Fatalf("-L 链接 = %+v", r)
	}

	if r := commandtest.Run(t, Run, dir, []string{"sub"}, ""); r.Code != 0 || !strings.Contains(r.Stdout, "directory") {
		t.Fatalf("目录 = %+v", r)
	}
}

func TestFileELFEndianAndScriptEdge(t *testing.T) {
	dir := t.TempDir()
	msb := append([]byte{0x7f, 'E', 'L', 'F', 1, 2, 1, 0}, make([]byte, 16)...)
	msb[16], msb[17] = 0, 3
	writeFixture(t, dir, "msb.so", msb)
	if r := commandtest.Run(t, Run, dir, []string{"msb.so"}, ""); !strings.Contains(r.Stdout, "ELF 32-bit MSB") {
		t.Fatalf("MSB = %+v", r)
	}

	writeFixture(t, dir, "noeol", []byte("#!/bin/sh"))
	if r := commandtest.Run(t, Run, dir, []string{"noeol"}, ""); !strings.Contains(r.Stdout, "a /bin/sh script") {
		t.Fatalf("无换行 shebang = %+v", r)
	}

	unknown := append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, make([]byte, 16)...)
	writeFixture(t, dir, "elfx", unknown)
	if r := commandtest.Run(t, Run, dir, []string{"elfx"}, ""); !strings.Contains(r.Stdout, "unknown type") {
		t.Fatalf("未知类型 = %+v", r)
	}
}
