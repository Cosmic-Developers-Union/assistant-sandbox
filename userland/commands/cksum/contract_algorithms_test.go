package cksumcmd

import (
	"crypto/md5"
	"crypto/sha256"
	sha30 "crypto/sha3"
	"encoding/base64"
	"encoding/hex"
	"io"
	"regexp"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"golang.org/x/crypto/sha3"
)

func TestAlgorithmSurface(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello\n")
	write(t, dir, "data", data)
	sha224 := sha256.Sum224(data)
	sha512 := sha30.Sum512(data)
	shake := sha3.NewShake128()
	if _, err := shake.Write(data); err != nil {
		t.Fatal(err)
	}
	shakeOut := make([]byte, 32)
	if _, err := shake.Read(shakeOut); err != nil {
		t.Error(err)
	}

	for _, test := range []struct {
		args    []string
		pattern string
	}{
		{[]string{"-a", "crc32b", "data"}, `^\d+ 6 data\n$`},
		{[]string{"-a", "cksum", "data"}, `^\d+ 6 data\n$`},
		{[]string{"-a", "bsd", "data"}, `^\d{5} +\d+ data\n$`},
		{[]string{"-a", "sysv", "data"}, `^\d+ \d+ data\n$`},
		{[]string{"-a", "sha224", "data"}, `^SHA224 \(data\) = [0-9a-f]{56}\n$`},
		{[]string{"-a", "sha384", "data"}, `^SHA384 \(data\) = [0-9a-f]{96}\n$`},
		{[]string{"-a", "sha512", "data"}, `^SHA512 \(data\) = [0-9a-f]{128}\n$`},
		{[]string{"-a", "sha3-224", "data"}, `^SHA3-224 \(data\) = [0-9a-f]{56}\n$`},
		{[]string{"-a", "sha3-384", "data"}, `^SHA3-384 \(data\) = [0-9a-f]{96}\n$`},
		{[]string{"-a", "sha3-512", "data"}, `^SHA3-512 \(data\) = [0-9a-f]{128}\n$`},
		{[]string{"-a", "sha3_256", "data"}, `^SHA3-256 \(data\) = [0-9a-f]{64}\n$`},
		{[]string{"-a", "blake2b", "data"}, `^BLAKE2b \(data\) = [0-9a-f]{128}\n$`},
		{[]string{"-a", "blake2b", "-l", "128", "data"}, `^BLAKE2b-128 \(data\) = [0-9a-f]{32}\n$`},
		{[]string{"--algorithm=sha1", "data"}, `^SHA1 \(data\) = [0-9a-f]{40}\n$`},
		{[]string{"-a", "sha2", "--length", "224", "data"}, `^SHA224 \(data\) = [0-9a-f]{56}\n$`},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || !regexp.MustCompile(test.pattern).MatchString(result.Stdout) {
			t.Fatalf("args=%v 得到 %+v，want %s", test.args, result, test.pattern)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"-a", "sha2", "-l", "224", "data"}, "")
	if result.Stdout != "SHA224 (data) = "+hex.EncodeToString(sha224[:])+"\n" {
		t.Fatalf("sha224 值: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "sha3", "-l", "512", "data"}, "")
	if result.Stdout != "SHA3-512 (data) = "+hex.EncodeToString(sha512[:])+"\n" {
		t.Fatalf("sha3-512 值: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "shake128", "-l", "256", "data"}, "")
	if result.Code != 0 || !strings.HasPrefix(result.Stdout, "SHAKE128 (data) = ") {
		t.Fatalf("shake128: %+v", result)
	}
	_ = shakeOut
}

func TestCheckLineForms(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello\n")
	write(t, dir, "data", data)
	md5sum := md5.Sum(data)
	digest := hex.EncodeToString(md5sum[:])
	b64 := base64.StdEncoding.EncodeToString(md5sum[:])
	for _, test := range []struct {
		name  string
		lines string
		want  string
	}{
		{"untagged-two-spaces", digest + "  data\n", "data: OK\n"},
		{"untagged-binary-marker", digest + " *data\n", "data: OK\n"},
		{"tagged-spaced", "MD5 (data) = " + digest + "\n", "data: OK\n"},
		{"tagged-compact", "MD5(data)= " + digest + "\n", "data: OK\n"},
		{"base64", "MD5 (data) = " + b64 + "\n", "data: OK\n"},
		{"comment-and-blank", "# 注释\n\n" + digest + "  data\n", "data: OK\n"},
	} {
		write(t, dir, "sums", []byte(test.lines))
		result := commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "sums"}, "")
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("%s: 得到 %+v，want %q", test.name, result, test.want)
		}
	}

	write(t, dir, "auto", []byte("MD5 (data) = "+digest+"\n"))
	result := commandtest.Run(t, Run, dir, []string{"-c", "auto"}, "")
	if result.Code != 0 || result.Stdout != "data: OK\n" {
		t.Fatalf("自动识别得到 %+v", result)
	}

	write(t, dir, "esc", []byte("\\MD5 (data) = "+digest+"\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "esc"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "OK") {
		t.Fatalf("转义文件名得到 %+v", result)
	}

	write(t, dir, "short", []byte("00  data\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--warn", "-c", "short"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "improperly formatted") {
		t.Fatalf("长度不符得到 %+v", result)
	}
}

func TestLegacyShortFlagsAndRaw(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "data", []byte("hello\n"))
	for _, test := range []struct {
		args    []string
		pattern string
	}{
		{[]string{"-r", "data"}, `^\d{5} +\d+ data\n$`},
		{[]string{"-s", "data"}, `^\d+ \d+ data\n$`},
		{[]string{"-rs", "data"}, `^\d+ \d+ data\n$`},
		{[]string{"-z", "-a", "md5", "data"}, "^MD5 \\(data\\) = [0-9a-f]{32}\\x00$"},
		{[]string{"-a", "sha3", "-l", "224", "data"}, `^SHA3-224 \(data\) = [0-9a-f]{56}\n$`},
		{[]string{"-a", "sha3", "-l", "384", "data"}, `^SHA3-384 \(data\) = [0-9a-f]{96}\n$`},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || !regexp.MustCompile(test.pattern).MatchString(result.Stdout) {
			t.Fatalf("args=%v 得到 %+v，want %s", test.args, result, test.pattern)
		}
	}
	for _, test := range []struct {
		args []string
		size int
	}{
		{[]string{"--raw", "-a", "crc", "data"}, 4},
		{[]string{"--raw", "-a", "crc32b", "data"}, 4},
		{[]string{"--raw", "-a", "bsd", "data"}, 2},
		{[]string{"--raw", "-a", "sysv", "data"}, 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || len(result.Stdout) != test.size {
			t.Fatalf("args=%v 得到 %+v，want %d 字节", test.args, result, test.size)
		}
	}
	if result := commandtest.Run(t, Run, dir, []string{"--tag", "-t", "data"}, ""); result.Code != 2 {
		t.Fatalf("--tag 与 --text 互斥: %+v", result)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return 0, nil }

func TestShortWrite(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "data", []byte("hello\n"))
	release := bindForTest(t, dir)
	defer release()
	rc := &toolcompat.RunContext{Dir: dir, Stdio: toolcompat.Stdio{Out: shortWriter{}, Err: io.Discard}}
	if code := cmd.Run(rc, []string{"data"}); code != 1 {
		t.Fatalf("短写应返回 1: %d", code)
	}
}
