package taccmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"mvdan.cc/sh/v3/interp"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("a\nb\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
		code  int
	}{
		{"stdin", nil, "a\nb\n", "b\na\n", 0},
		{"file", []string{"data"}, "", "b\na\n", 0},
		{"dash", []string{"-"}, "x\ny\n", "y\nx\n", 0},
		{"separator", []string{"-s", ";"}, "a;b;", "b;a;", 0},
		{"before", []string{"-b", "-s", ";"}, "a;b;", "b;a", 0},
		{"regex", []string{"-r", "-s", "[0-9]"}, "a1b2", "b2a1", 0},
		{"empty", []string{}, "", "", 0},
		{"no-trailing", nil, "a\nb", "ba\n", 0},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("%s: args=%v 得到 %+v，want code=%d stdout=%q", test.name, test.args, result, test.code, test.want)
		}
	}
	for _, args := range [][]string{{"-s", ""}, {"-r", "-s", "["}, {"absent"}, {"--bad"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
	if result := commandtest.Run(t, Run, dir, []string{"--help"}, ""); result.Code != 0 {
		t.Fatalf("--help 得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, nil, ""); result.Code != 0 {
		t.Fatalf("空输入得到 %+v", result)
	}
}

func TestDirectPaths(t *testing.T) {
	var out bytes.Buffer
	rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{Out: &out, Err: io.Discard}}
	if code := cmd.Run(rc, []string{"-"}); code != 0 {
		t.Fatalf("nil stdin 应成功: %d", code)
	}
	if out.String() != "" {
		t.Fatalf("nil stdin 应无输出: %q", out.String())
	}
	failing := &toolcompat.RunContext{Stdio: toolcompat.Stdio{In: strings.NewReader("a\nb\n"), Out: failWriter{}, Err: io.Discard}}
	if code := cmd.Run(failing, nil); code != 1 {
		t.Fatalf("写失败应返回 1: %d", code)
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-r", "-b", "-s", "[0-9]"}, "a1b", "ba"},
		{[]string{"-b", "-s", ";"}, "a;b", ";a"},
		{[]string{"-r", "-s", "[0-9]"}, "a1", "a1"},
	} {
		out.Reset()
		rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{In: strings.NewReader(test.input), Out: &out, Err: io.Discard}}
		if code := cmd.Run(rc, test.args); code != 0 || out.String() != test.want {
			t.Fatalf("args=%v 得到 code=%d out=%q，want %q", test.args, code, out.String(), test.want)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestEnvironmentHelpers(t *testing.T) {
	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
