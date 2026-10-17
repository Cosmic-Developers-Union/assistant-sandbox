package cksumcmd

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCheckLineVariants(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello\n")
	write(t, dir, "data", data)
	md5sum := md5.Sum(data)
	md5hex := hex.EncodeToString(md5sum[:])
	md5b64 := base64.StdEncoding.EncodeToString(md5sum[:])
	sha1sum := sha1.Sum(data)
	sha1hex := hex.EncodeToString(sha1sum[:])
	for _, test := range []struct {
		name  string
		args  []string
		lines string
		code  int
		ok    bool
	}{
		{"untagged-base64", []string{"-a", "md5"}, md5b64 + "  data\n", 0, true},
		{"untagged-short-digest", []string{"-a", "md5", "--warn"}, "00  data\n", 1, false},
		{"untagged-nonhex", []string{"-a", "md5", "--warn"}, strings.Repeat("z", 32) + "  data\n", 1, false},
		{"tagged-other-algorithm", []string{"-a", "md5"}, "SHA1 (data) = " + sha1hex + "\n", 1, false},
		{"tagged-missing-equals", []string{"-a", "md5", "--warn"}, "MD5 (data) " + md5hex + "\n", 1, false},
		{"tagged-no-close-paren", []string{"-a", "md5", "--warn"}, "MD5 (data = " + md5hex + "\n", 1, false},
		{"tagged-wrong-digest", []string{"-a", "md5", "--warn"}, "MD5 (data) = " + strings.Repeat("0", 32) + "\n", 1, false},
	} {
		write(t, dir, "sums", []byte(test.lines))
		result := commandtest.Run(t, Run, dir, append(test.args, "-c", "sums"), "")
		if result.Code != test.code {
			t.Fatalf("%s: 得到 %+v，want code=%d", test.name, result, test.code)
		}
		if test.ok && result.Stdout != "data: OK\n" {
			t.Fatalf("%s: 得到 %+v", test.name, result)
		}
	}

	write(t, dir, "multi", []byte("junk one\njunk two\n"+md5hex+"  data\n"))
	result := commandtest.Run(t, Run, dir, []string{"-a", "md5", "--warn", "-c", "multi"}, "")
	if result.Code != 0 || !strings.Contains(result.Stderr, "lines are improperly formatted") {
		t.Fatalf("复数诊断得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"--algor", "md5", "data"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "MD5 (data)") {
		t.Fatalf("长选项缩写得到 %+v", result)
	}

	produced := commandtest.Run(t, Run, dir, []string{"-a", "blake2b", "-l", "128", "data"}, "")
	write(t, dir, "blake", []byte(produced.Stdout))
	if result := commandtest.Run(t, Run, dir, []string{"-a", "blake2b", "-l", "128", "-c", "blake"}, ""); result.Code != 0 {
		t.Fatalf("BLAKE2b 标签得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "crc32b"}, "hello\n")
	if result.Code != 0 || !strings.HasSuffix(result.Stdout, " 6\n") {
		t.Fatalf("crc32b 标准输入得到 %+v", result)
	}
}

func TestCheckEdgeCases(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello\n")
	write(t, dir, "data", data)
	sum := md5.Sum(data)
	digest := hex.EncodeToString(sum[:])

	result := commandtest.Run(t, Run, dir, []string{"-a", "md5", "--warn", "-c", "-"}, digest+"  -\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "improperly formatted") {
		t.Fatalf("标准输入里的 - 路径得到 %+v", result)
	}

	write(t, dir, "bad", []byte(strings.Repeat("0", 32)+"  data\n"))
	result = commandtest.Run(t, Run, dir, []string{"-a", "md5", "--status", "-c", "bad"}, "")
	if result.Code != 1 || result.Stdout != "" {
		t.Fatalf("--status 失败得到 %+v", result)
	}

	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"folder"}, {"-a", "md5", "folder"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 1 || result.Stderr == "" {
			t.Fatalf("目录输入 args=%v 得到 %+v", args, result)
		}
	}

	write(t, dir, "esc", []byte("\\"+digest+"  data\n"))
	if result := commandtest.Run(t, Run, dir, []string{"-a", "md5", "-c", "esc"}, ""); result.Code != 0 && result.Stdout != "data: OK\n" {
		t.Fatalf("转义无标签行得到 %+v", result)
	}
}

func TestFilenameHelpers(t *testing.T) {
	for _, test := range []struct {
		name    string
		escaped string
		prefix  string
	}{
		{"plain", "plain", ""},
		{`a\b`, `a\\b`, `\`},
		{"a\nb", `a\nb`, `\`},
		{"a\rb", `a\rb`, `\`},
	} {
		escaped, prefix := escapeFilename(test.name)
		if escaped != test.escaped || prefix != test.prefix {
			t.Fatalf("%q 得到 %q %q", test.name, escaped, prefix)
		}
		if got := unescapeIf(prefix != "", escaped); got != test.name {
			t.Fatalf("%q 反解得到 %q", test.name, got)
		}
	}
	if got := unescapeIf(false, `a\nb`); got != `a\nb` {
		t.Fatalf("未转义不应反解: %q", got)
	}
	if got := unescapeIf(true, `x\\y\qz`); got != "x\\y\\qz" {
		t.Fatalf("未知转义应原样保留: %q", got)
	}
	for _, test := range []struct {
		err  error
		want string
	}{
		{errIsDirectory, "Is a directory"},
		{fs.ErrNotExist, "No such file or directory"},
		{fs.ErrPermission, "Permission denied"},
		{&fs.PathError{Op: "open", Path: "x", Err: fs.ErrClosed}, "file already closed"},
		{errors.New("其他"), "其他"},
	} {
		if got := gnuErrMsg(test.err); got != test.want {
			t.Fatalf("gnuErrMsg(%v)=%q，want %q", test.err, got, test.want)
		}
	}
}
