package tsortcmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "edges"), []byte("a b\nb c\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
		code  int
	}{
		{[]string{"edges"}, "", "a\nb\nc\n", 0},
		{nil, "a b\nb c\n", "a\nb\nc\n", 0},
		{[]string{"-"}, "b c\n", "b\nc\n", 0},
		{nil, "a b b c a c\n", "a\nb\nc\n", 0},
		{nil, "a a\n", "a\n", 0},
		{nil, "a b\nc d\n", "a\nb\nc\nd\n", 0},
		{nil, "a\tb\n", "a\nb\n", 0},
		{nil, "a b", "a\nb\n", 0},
		{nil, "  a  b  \n", "a\nb\n", 0},
		{nil, "a\n", "", 1},
		{nil, "a b\nb a\n", "", 1},
		{[]string{"absent"}, "", "", 1},
		{[]string{"a", "b"}, "", "", 2},
		{[]string{"--bad"}, "", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code {
			t.Fatalf("args=%v 得到 %+v，want code=%d", test.args, result, test.code)
		}
		if test.code == 0 && result.Stdout != test.want {
			t.Fatalf("args=%v 得到 stdout=%q，want %q", test.args, result.Stdout, test.want)
		}
		if test.code != 0 && result.Stderr == "" {
			t.Fatalf("args=%v 报错却没有诊断: %+v", test.args, result)
		}
	}
	result := commandtest.Run(t, Run, dir, nil, "a\n")
	if !strings.Contains(result.Stderr, "odd number of tokens") {
		t.Fatalf("奇数记号诊断: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, nil, "a b\nb a\n")
	if !strings.Contains(result.Stderr, "loop") || !strings.Contains(result.Stdout, "a") {
		t.Fatalf("环诊断: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-w"}, "a b\nb a\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "loop") {
		t.Fatalf("-w 环应以环数为退出码: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help 得到 %+v", result)
	}
}

func TestCycleShapes(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		input string
		want  string
	}{
		{"a b\nb c\nc a\n", "a\nb\nc\n"},
		{"a b\nb c\nc b\n", "a\nb\nc\n"},
		{"a b\nb a\nc d\n", "c\nd\na\nb\n"},
	} {
		result := commandtest.Run(t, Run, dir, nil, test.input)
		if result.Code != 1 || !strings.Contains(result.Stderr, "loop") {
			t.Fatalf("input=%q 得到 %+v", test.input, result)
		}
		if result.Stdout != test.want {
			t.Fatalf("input=%q 得到 stdout=%q，want %q", test.input, result.Stdout, test.want)
		}
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

func TestWriteFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), nil, strings.NewReader("a b\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}

func TestCycleExitCap(t *testing.T) {
	var input strings.Builder
	for index := 0; index < 125; index++ {
		fmt.Fprintf(&input, "n%d m%d\nm%d n%d\n", index, index, index, index)
	}
	result := commandtest.Run(t, Run, t.TempDir(), []string{"-w"}, input.String())
	if result.Code != 124 {
		t.Fatalf("-w 超过上限应返回 124: %+v", result)
	}
}
