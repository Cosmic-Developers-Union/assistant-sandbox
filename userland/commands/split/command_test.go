package splitcmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	"mvdan.cc/sh/v3/interp"
)

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "<缺失:" + name + ">"
	}
	return string(data)
}

func TestLineAndByteSplit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("1\n2\n3\n4\n5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bytes"), []byte("abcdefghij"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "records"), []byte("a,b,c"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args   []string
		input  string
		files  map[string]string
		stdout string
	}{
		{args: []string{"-l", "2", "data", "out"}, files: map[string]string{"outaa": "1\n2\n", "outab": "3\n4\n", "outac": "5\n"}},
		{args: []string{"-l", "2", "data"}, files: map[string]string{"xaa": "1\n2\n", "xab": "3\n4\n", "xac": "5\n"}},
		{args: []string{"-b", "4", "bytes", "byte-"}, files: map[string]string{"byte-aa": "abcd", "byte-ab": "efgh", "byte-ac": "ij"}},
		{args: []string{"-l", "2", "-a", "3", "data", "num"}, files: map[string]string{"numaaa": "1\n2\n", "numaab": "3\n4\n", "numaac": "5\n"}},
		{args: []string{"-l", "2", "-d", "data", "d"}, files: map[string]string{"d00": "1\n2\n", "d01": "3\n4\n", "d02": "5\n"}},
		{args: []string{"-l", "2", "-x", "data", "h"}, files: map[string]string{"h0": "1\n2\n", "h1": "3\n4\n", "h2": "5\n"}},
		{args: []string{"-l", "2", "--additional-suffix=.txt", "data", "s"}, files: map[string]string{"saa.txt": "1\n2\n", "sab.txt": "3\n4\n", "sac.txt": "5\n"}},
		{args: []string{"-l", "1", "-t", ",", "records", "r"}, files: map[string]string{"raa": "a,", "rab": "b,c"}},
		{args: []string{"-l", "3", "-", "csplit"}, files: map[string]string{"csplitaa": "1\n2\n3\n", "csplitab": "5\n"}, input: "1\n2\n3\n5\n"},
		{args: []string{"-l", "5", "-", "stdio-"}, files: map[string]string{"stdio-aa": "1\n2\n3\n4\n5\n"}, input: "1\n2\n3\n4\n5\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 {
			t.Fatalf("args=%v 得到 %+v", test.args, result)
		}
		if result.Stdout != test.stdout {
			t.Fatalf("args=%v stdout=%q，want %q", test.args, result.Stdout, test.stdout)
		}
		for name, want := range test.files {
			entries, _ := os.ReadDir(dir)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if got := read(t, dir, name); got != want {
				_ = names
				t.Fatalf("args=%v 文件 %s=%q，want %q；目录=%v", test.args, name, got, want, names)
			}
		}
	}
}

func TestChunksAndVerbose(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("1\n2\n3\n4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-n", "2", "data", "chunk"}, ""); result.Code != 0 {
		t.Fatalf("-n 得到 %+v", result)
	}
	if got := read(t, dir, "chunkaa") + read(t, dir, "chunkab"); got != "1\n2\n3\n4\n" {
		t.Fatalf("-n 内容 %q", got)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-n", "l/2", "-e", "data", "elide"}, ""); result.Code != 0 {
		t.Fatalf("-n l/2 得到 %+v", result)
	}
	if got := read(t, dir, "elideaa"); got != "1\n2\n" {
		t.Fatalf("-n l/2 内容 %q", got)
	}
	result := commandtest.Run(t, Run, dir, []string{"-l", "2", "--verbose", "data", "loud"}, "")
	if result.Code != 0 || !strings.Contains(result.Stderr, "loudaa") {
		t.Fatalf("--verbose 得到 %+v", result)
	}
}

func TestCommandErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"-l", "2", "data", "o", "extra"}, 2},
		{[]string{"--bad"}, 2},
		{[]string{"-x", "-d", "data", "o"}, 2},
		{[]string{"--filter", "cat", "data", "o"}, 2},
		{[]string{"-l", "2", "-b", "2", "data", "o"}, 2},
		{[]string{"-a", "0", "data", "o"}, 2},
		{[]string{"-l", "2", "absent", "o"}, 1},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != test.code || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v，want code=%d", test.args, result, test.code)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-n", "2", "folder", "o"}, ""); result.Code != 1 || !strings.Contains(result.Stderr, "cannot determine file size") {
		t.Fatalf("-n 需要大小已知的具名输入: %+v", result)
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help 得到 %+v", result)
	}
}

func TestDirectFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("1\n2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-l", "1", "data", "absent/out"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("输出目录缺失应失败: %+v", result)
	}
	release := vfs.Bind(filesystem.NewFilesystem(dir), dir, nil)
	defer release()
	var out strings.Builder
	posix := &toolcompat.RunContext{
		Dir:   dir,
		Env:   []string{"POSIXLY_CORRECT=1"},
		Stdio: toolcompat.Stdio{In: strings.NewReader("1\n2\n"), Out: &out, Err: io.Discard},
	}
	var diagnostic strings.Builder
	posix.Err = &diagnostic
	if code := cmd.Run(posix, []string{"-l", "2", "-"}); code != 0 {
		t.Fatalf("POSIX 模式得到 code=%d stderr=%q", code, diagnostic.String())
	}
	if got := read(t, dir, "xaa"); got != "1\n2\n" {
		t.Fatalf("POSIX 模式产物 %q", got)
	}
}

func TestEnvironmentHelpers(t *testing.T) {
	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
