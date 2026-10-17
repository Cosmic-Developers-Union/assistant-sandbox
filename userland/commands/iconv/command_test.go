package iconv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func run(t *testing.T, args []string, input string) commandtest.Result {
	t.Helper()
	return commandtest.Run(t, Run, t.TempDir(), args, input)
}

func TestIconvToSingleByte(t *testing.T) {

	if r := run(t, []string{"-f", "UTF-8", "-t", "ASCII"}, "hello\n"); r.Code != 0 || r.Stdout != "hello\n" {
		t.Fatalf("ascii 范围 = %+v", r)
	}

	if r := run(t, []string{"-f", "utf-8", "-t", "iso-8859-1"}, "café"); r.Code != 0 || r.Stdout != "caf\xe9" {
		t.Fatalf("latin1 落码 = %+v", r)
	}

	r := run(t, []string{"-f", "UTF-8", "-t", "ASCII"}, "aé")
	if r.Code != 1 || r.Stderr != "iconv: 位置 1: 无法转换\n" || r.Stdout != "" {
		t.Fatalf("目标集外 = %+v", r)
	}

	if r := run(t, []string{"-f", "UTF-8", "-t", "ISO-8859-1"}, "Ā"); r.Code != 1 || r.Stderr != "iconv: 位置 0: 无法转换\n" {
		t.Fatalf("latin1 越界 = %+v", r)
	}

	if r := run(t, []string{"-f", "UTF-8", "-t", "ASCII", "-c"}, "aéĀ\xffz"); r.Code != 0 || r.Stdout != "az" {
		t.Fatalf("-c 丢弃 = %+v", r)
	}

	if r := run(t, []string{"-f", "UTF-8", "-t", "ASCII"}, "ab\xff"); r.Code != 1 || r.Stderr != "iconv: 位置 2: 无法转换\n" {
		t.Fatalf("非法序列 = %+v", r)
	}
}

func TestIconvFromSingleByte(t *testing.T) {
	if r := run(t, []string{"-f", "ASCII", "-t", "UTF-8"}, "plain"); r.Code != 0 || r.Stdout != "plain" {
		t.Fatalf("ascii→utf8 = %+v", r)
	}

	if r := run(t, []string{"-f", "ISO-8859-1", "-t", "UTF-8"}, "caf\xe9"); r.Code != 0 || r.Stdout != "café" {
		t.Fatalf("latin1→utf8 = %+v", r)
	}

	if r := run(t, []string{"-f", "ISO-8859-1", "-t", "ASCII"}, "\xe9"); r.Code != 1 || r.Stderr != "iconv: 位置 0: 无法转换\n" {
		t.Fatalf("latin1→ascii = %+v", r)
	}
	if r := run(t, []string{"-f", "ISO-8859-1", "-t", "ASCII", "-c"}, "a\xe9b"); r.Code != 0 || r.Stdout != "ab" {
		t.Fatalf("latin1→ascii -c = %+v", r)
	}

	if r := run(t, []string{"-f", "ASCII", "-t", "ISO-8859-1"}, "same"); r.Code != 0 || r.Stdout != "same" {
		t.Fatalf("ascii→latin1 = %+v", r)
	}
}

func TestIconvIdentity(t *testing.T) {
	if r := run(t, []string{"-f", "ASCII", "-t", "ASCII"}, "a\xffb"); r.Code != 0 || r.Stdout != "a\xffb" {
		t.Fatalf("ascii 恒等 = %+v", r)
	}
	if r := run(t, []string{"-f", "UTF-8", "-t", "UTF-8"}, "文本"); r.Code != 0 || r.Stdout != "文本" {
		t.Fatalf("utf8 恒等 = %+v", r)
	}
	if r := run(t, []string{"-f", "ISO-8859-1", "-t", "LATIN1"}, "\xff\xfe"); r.Code != 0 || r.Stdout != "\xff\xfe" {
		t.Fatalf("latin1 恒等 = %+v", r)
	}
}

func TestIconvInputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := commandtest.Run(t, Run, dir, []string{"-f", "UTF-8", "-t", "ASCII", "a.txt"}, "")
	if r.Code != 0 || r.Stdout != "hi" {
		t.Fatalf("文件输入 = %+v", r)
	}

	r = commandtest.Run(t, Run, dir, []string{"-f", "UTF-8", "-t", "ASCII", "-"}, "pipe")
	if r.Code != 0 || r.Stdout != "pipe" {
		t.Fatalf("stdin = %+v", r)
	}

	r = commandtest.Run(t, Run, dir, []string{"-f", "UTF-8", "-t", "ASCII"}, "")
	if r.Code != 0 || r.Stdout != "" {
		t.Fatalf("空输入 = %+v", r)
	}

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = commandtest.Run(t, Run, dir, []string{"-f", "UTF-8", "-t", "ASCII", "a.txt", "b.txt"}, "")
	if r.Code != 0 || r.Stdout != "hiok" {
		t.Fatalf("多文件 = %+v", r)
	}

	r = commandtest.Run(t, Run, dir, []string{"-f", "UTF-8", "-t", "ASCII", "missing.txt"}, "")
	if r.Code == 0 {
		t.Fatalf("缺失文件 = %+v", r)
	}
}

func TestIconvList(t *testing.T) {
	r := run(t, []string{"-l"}, "")
	if r.Code != 0 {
		t.Fatalf("-l = %+v", r)
	}
	names := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	want := []string{"8859-1", "ANSI_X3.4-1968", "ASCII", "ISO-8859-1", "US-ASCII", "UTF-8", "latin1"}
	if len(names) != len(want) {
		t.Fatalf("-l 输出 %d 行: %+v", len(names), r)
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("-l 第 %d 行 = %s, 期望 %s", i, names[i], name)
		}
	}
}

func TestIconvUnsupportedNames(t *testing.T) {
	for _, args := range [][]string{
		{"-f", "KOI8-R", "-t", "UTF-8"},
		{"-f", "UTF-8", "-t", "UTF-16"},
		{"-f", "UTF-8", "-t", "ASCII//TRANSLIT"},
		{"-f", "UTF-8//IGNORE", "-t", "ASCII"},
	} {
		r := run(t, args, "x")
		if r.Code != 1 || !strings.HasSuffix(r.Stderr, ": 不支持\n") {
			t.Fatalf("%v = %+v", args, r)
		}
	}
}

func TestIconvUsage(t *testing.T) {
	if r := run(t, []string{}, "x"); r.Code != 2 {
		t.Fatalf("无选项 = %+v", r)
	}
	if r := run(t, []string{"-f", "UTF-8"}, "x"); r.Code != 2 {
		t.Fatalf("缺 -t = %+v", r)
	}
	if r := run(t, []string{"-t", "ASCII"}, "x"); r.Code != 2 {
		t.Fatalf("缺 -f = %+v", r)
	}
	if r := run(t, []string{"--bogus"}, "x"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}
