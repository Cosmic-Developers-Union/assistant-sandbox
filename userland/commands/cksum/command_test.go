package cksumcmd

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"golang.org/x/crypto/blake2b"
)

func write(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCRCMode(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "data", []byte("hello\n"))

	result := commandtest.Run(t, Run, dir, nil, "")
	if result.Code != 0 || result.Stdout != "4294967295 0\n" {
		t.Fatalf("空输入得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"data"}, "")
	if result.Code != 0 || !regexp.MustCompile(`^\d+ 6 data\n$`).MatchString(result.Stdout) {
		t.Fatalf("文件得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-z", "data"}, "")
	if result.Code != 0 || !strings.HasSuffix(result.Stdout, "data\x00") {
		t.Fatalf("-z 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"data", "data"}, "")
	if result.Code != 0 || len(strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")) != 2 {
		t.Fatalf("多文件得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"absent"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("缺失输入得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--raw", "data"}, "")
	if result.Code != 0 || len(result.Stdout) != 4 {
		t.Fatalf("--raw 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--debug", "data"}, "")
	if result.Code != 0 || !strings.Contains(result.Stderr, "hardware acceleration") {
		t.Fatalf("--debug 得到 %+v", result)
	}
}

func TestDigestModes(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello\n")
	write(t, dir, "data", data)
	md5sum := md5.Sum(data)
	sha1sum := sha1.Sum(data)
	sha224 := sha256.Sum224(data)
	sha256sum := sha256.Sum256(data)
	sha384 := sha512.Sum384(data)
	sha512sum := sha512.Sum512(data)
	sha3sum := sha3.Sum256(data)
	blake := blake2b.Sum256(data)

	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"-a", "md5", "data"}, "MD5 (data) = " + hex.EncodeToString(md5sum[:]) + "\n"},
		{[]string{"-a", "md5", "--untagged", "data"}, hex.EncodeToString(md5sum[:]) + "  data\n"},
		{[]string{"-a", "md5", "--tag", "data"}, "MD5 (data) = " + hex.EncodeToString(md5sum[:]) + "\n"},
		{[]string{"-a", "md5", "--base64", "--untagged", "data"}, base64.StdEncoding.EncodeToString(md5sum[:]) + "  data\n"},
		{[]string{"-a", "sha1", "data"}, "SHA1 (data) = " + hex.EncodeToString(sha1sum[:]) + "\n"},
		{[]string{"-a", "sha2", "-l", "224", "data"}, "SHA224 (data) = " + hex.EncodeToString(sha224[:]) + "\n"},
		{[]string{"-a", "sha2", "-l", "256", "data"}, "SHA256 (data) = " + hex.EncodeToString(sha256sum[:]) + "\n"},
		{[]string{"-a", "sha2", "-l", "384", "data"}, "SHA384 (data) = " + hex.EncodeToString(sha384[:]) + "\n"},
		{[]string{"-a", "sha2", "-l", "512", "data"}, "SHA512 (data) = " + hex.EncodeToString(sha512sum[:]) + "\n"},
		{[]string{"-a", "sha256", "data"}, "SHA256 (data) = " + hex.EncodeToString(sha256sum[:]) + "\n"},
		{[]string{"-a", "sha3", "-l", "256", "data"}, "SHA3-256 (data) = " + hex.EncodeToString(sha3sum[:]) + "\n"},
		{[]string{"-a", "blake2b", "-l", "256", "data"}, "BLAKE2b-256 (data) = " + hex.EncodeToString(blake[:]) + "\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}

	result := commandtest.Run(t, Run, dir, []string{"-a", "md5", "--raw", "--untagged", "data"}, "")
	if result.Code != 0 || result.Stdout != string(md5sum[:]) {
		t.Fatalf("--raw 得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-a", "bsd", "data"}, "")
	if result.Code != 0 || !regexp.MustCompile(`^\d{5} +\d+ data\n$`).MatchString(result.Stdout) {
		t.Fatalf("bsd 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "sysv", "data"}, "")
	if result.Code != 0 || !regexp.MustCompile(`^\d+ \d+ data\n$`).MatchString(result.Stdout) {
		t.Fatalf("sysv 得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-a", "md5"}, "hello\n")
	if result.Code != 0 || result.Stdout != "MD5 (-) = "+hex.EncodeToString(md5sum[:])+"\n" {
		t.Fatalf("标准输入得到 %+v", result)
	}
}

func TestCheckMode(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello\n")
	write(t, dir, "data", data)
	sum := md5.Sum(data)
	digest := hex.EncodeToString(sum[:])
	write(t, dir, "sums", []byte(digest+"  data\n"))
	result := commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "sums"}, "")
	if result.Code != 0 || result.Stdout != "data: OK\n" {
		t.Fatalf("校验通过得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--quiet", "-c", "sums"}, "")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("--quiet 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--status", "-c", "sums"}, "")
	if result.Code != 0 || result.Stdout != "" || result.Stderr != "" {
		t.Fatalf("--status 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "-"}, digest+"  data\n")
	if result.Code != 0 || result.Stdout != "data: OK\n" {
		t.Fatalf("标准输入校验得到 %+v", result)
	}

	write(t, dir, "bad", []byte(strings.Repeat("0", 32)+"  data\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "bad"}, "")
	if result.Code != 1 || !strings.Contains(result.Stdout, "data: FAILED\n") || !strings.Contains(result.Stderr, "WARNING") {
		t.Fatalf("校验失败得到 %+v", result)
	}
	write(t, dir, "missing", []byte(digest+"  absent\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "missing"}, "")
	if result.Code != 1 || !strings.Contains(result.Stdout, "absent: FAILED open or read\n") {
		t.Fatalf("缺失文件得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--ignore-missing", "-c", "missing"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "no file was verified") {
		t.Fatalf("--ignore-missing 全部缺失得到 %+v", result)
	}
	write(t, dir, "partial", []byte(digest+"  data\n"+digest+"  absent\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--ignore-missing", "-c", "partial"}, "")
	if result.Code != 0 || result.Stdout != "data: OK\n" {
		t.Fatalf("--ignore-missing 部分缺失得到 %+v", result)
	}
	write(t, dir, "junk", []byte("not a checksum line\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--warn", "-c", "junk"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "improperly formatted") {
		t.Fatalf("--warn 得到 %+v", result)
	}
	write(t, dir, "mixed", []byte(digest+"  data\nnot a checksum line\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--warn", "-c", "mixed"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "data: OK") || !strings.Contains(result.Stderr, "improperly formatted") {
		t.Fatalf("混合校验文件得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--strict", "-c", "junk"}, "")
	if result.Code != 1 {
		t.Fatalf("--strict 得到 %+v", result)
	}
	write(t, dir, "empty", []byte("# 只有注释\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "empty"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "no properly formatted checksum lines found") {
		t.Fatalf("空校验文件得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-c", "absent"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("校验文件缺失得到 %+v", result)
	}

	write(t, dir, "tagged", []byte("MD5 (data) = "+digest+"\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "tagged"}, "")
	if result.Code != 0 || result.Stdout != "data: OK\n" {
		t.Fatalf("带标签校验得到 %+v", result)
	}
}

func TestOptionErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"--base64", "--raw", "x"},
		{"--tag", "-c", "x"},
		{"--untagged", "-c", "x"},
		{"--zero", "-c", "x"},
		{"--status", "x"},
		{"--ignore-missing", "x"},
		{"--warn", "x"},
		{"--quiet", "x"},
		{"--strict", "x"},
		{"-a", "bogus"},
		{"-a", "md5", "-l", "128", "x"},
		{"-a", "blake2b", "-l", "100", "x"},
		{"-a", "blake2b", "-l", "520", "x"},
		{"-a", "sha2", "-l", "100", "x"},
		{"-a", "blake2b", "-l", "-8", "x"},
		{"--bad"},
	} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help 得到 %+v", result)
	}
}
