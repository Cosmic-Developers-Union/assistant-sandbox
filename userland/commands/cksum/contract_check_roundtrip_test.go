package cksumcmd

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

func bindForTest(t *testing.T, dir string) func() {
	t.Helper()
	return vfs.Bind(filesystem.NewFilesystem(dir), dir, nil)
}

func TestTaggedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello\n")
	write(t, dir, "data", data)
	for _, test := range []struct {
		name  string
		args  []string
		check string
	}{
		{"sha256", []string{"-a", "sha2", "-l", "256", "data"}, "-a sha2 -l 256 -c sums"},
		{"sha384", []string{"-a", "sha2", "-l", "384", "data"}, "-a sha2 -l 384 -c sums"},
		{"sha512", []string{"-a", "sha2", "-l", "512", "data"}, "-a sha2 -l 512 -c sums"},
		{"sha3-224", []string{"-a", "sha3-224", "data"}, "-a sha3-224 -c sums"},
		{"sha3-384", []string{"-a", "sha3-384", "data"}, "-a sha3-384 -c sums"},
		{"sha3-512", []string{"-a", "sha3-512", "data"}, "-a sha3-512 -c sums"},
		{"blake2b", []string{"-a", "blake2b", "data"}, "-a blake2b -c sums"},
		{"shake128", []string{"-a", "shake128", "-l", "256", "data"}, "-a shake128 -l 256 -c sums"},
		{"md5", []string{"-a", "md5", "data"}, "-a md5 -c sums"},
		{"sha1", []string{"-a", "sha1", "data"}, "-a sha1 -c sums"},
	} {
		produced := commandtest.Run(t, Run, dir, test.args, "")
		if produced.Code != 0 || produced.Stdout == "" {
			t.Fatalf("%s 生成失败: %+v", test.name, produced)
		}
		write(t, dir, "sums", []byte(produced.Stdout))
		result := commandtest.Run(t, Run, dir, strings.Fields(test.check), "")
		if result.Code != 0 || result.Stdout != "data: OK\n" {
			t.Fatalf("%s 校验失败: %+v", test.name, result)
		}
	}

	produced := commandtest.Run(t, Run, dir, []string{"-a", "sha256", "data"}, "")
	write(t, dir, "auto", []byte(produced.Stdout))
	if result := commandtest.Run(t, Run, dir, []string{"-c", "auto"}, ""); result.Code != 0 || result.Stdout != "data: OK\n" {
		t.Fatalf("自动识别 sha256 得到 %+v", result)
	}
	sum := sha256.Sum256(data)
	if !strings.Contains(produced.Stdout, hex.EncodeToString(sum[:])) {
		t.Fatalf("摘要不符: %+v", produced)
	}
}

func TestRawAndUntaggedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "data", []byte("hello\n"))
	produced := commandtest.Run(t, Run, dir, []string{"-a", "sha1", "--untagged", "data"}, "")
	if produced.Code != 0 {
		t.Fatalf("生成失败: %+v", produced)
	}
	write(t, dir, "sums", []byte(produced.Stdout))
	if result := commandtest.Run(t, Run, dir, []string{"-a", "sha1", "-c", "sums"}, ""); result.Code != 0 {
		t.Fatalf("无标签校验失败: %+v", result)
	}

	for _, mode := range []string{"-b", "-t"} {
		reference := commandtest.Run(t, Run, dir, []string{"-a", "sha1", "data"}, "")
		result := commandtest.Run(t, Run, dir, []string{mode, "-a", "sha1", "data"}, "")
		if result.Code != 0 || result.Stdout != reference.Stdout {
			t.Fatalf("%s 得到 %+v，want %q", mode, result, reference.Stdout)
		}
	}

	write(t, dir, "binary", []byte(strings.Replace(produced.Stdout, "  ", " *", 1)))
	if result := commandtest.Run(t, Run, dir, []string{"-a", "sha1", "-c", "binary"}, ""); result.Code != 0 {
		t.Fatalf("二进制标记得到 %+v", result)
	}
}

func TestFaultedOperands(t *testing.T) {
	for _, test := range []struct {
		operation string
		args      []string
	}{
		{"Read", []string{"-a", "md5", "data"}},
		{"Read", []string{"data"}},
		{"Read", []string{"-a", "shake128", "-l", "256", "data"}},
		{"OpenRead", []string{"-a", "md5", "data"}},
	} {
		dir := t.TempDir()
		write(t, dir, "data", []byte("hello\n"))
		result := commandtest.RunFault(t, Run, dir, test.args, "", test.operation)
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("%s args=%v 得到 %+v", test.operation, test.args, result)
		}
	}
}

func TestWriteFailures(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "data", []byte("hello\n"))
	release := bindForTest(t, dir)
	defer release()
	for _, args := range [][]string{
		{"data"},
		{"--raw", "data"},
		{"-a", "md5", "data"},
		{"-a", "md5", "--raw", "--untagged", "data"},
		{"-a", "blake2b", "-l", "128", "data"},
		{"-a", "shake128", "-l", "128", "data"},
	} {
		rc := &toolcompat.RunContext{Dir: dir, Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: failWriter{}, Err: io.Discard}}
		code := cmd.Run(rc, append([]string{"-a", "crc"}, args...))
		_ = code
		if code := cmd.Run(rc, args); code != 1 {
			t.Fatalf("args=%v 写失败应返回 1: %d", args, code)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
