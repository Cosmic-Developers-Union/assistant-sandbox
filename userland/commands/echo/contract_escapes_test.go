package echocmd

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

func runEcho(env []string, out io.Writer, args ...string) (int, string, string) {
	var diagnostic bytes.Buffer
	rc := &toolcompat.RunContext{Env: env, Stdio: toolcompat.Stdio{Out: out, Err: &diagnostic}}
	code := cmd.Run(rc, args)
	return code, diagnostic.String(), ""
}

func TestEscapeSet(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"-e", `a\ab`}, "a\ab\n"},
		{[]string{"-e", `a\bb`}, "a\bb\n"},
		{[]string{"-e", `a\eb`}, "a\x1bb\n"},
		{[]string{"-e", `a\fb`}, "a\fb\n"},
		{[]string{"-e", `a\nb`}, "a\nb\n"},
		{[]string{"-e", `a\rb`}, "a\rb\n"},
		{[]string{"-e", `a\vb`}, "a\vb\n"},
		{[]string{"-e", `a\\b`}, "a\\b\n"},
		{[]string{"-e", `\0101`}, "A\n"},
		{[]string{"-e", `\0`}, "\x00\n"},
		{[]string{"-e", `\x41`}, "A\n"},
		{[]string{"-e", `\x4`}, "\x04\n"},
		{[]string{"-e", `\x`}, "\\x\n"},
		{[]string{"-e", `a\qb`}, "a\\qb\n"},
		{[]string{"-e", `tail\`}, "tail\\\n"},
		{[]string{"-e", "one", "two\\c", "three"}, "one two"},
		{[]string{"-e", "\\c"}, ""},
		{[]string{"-E", `a\nb`}, "a\\nb\n"},
	} {
		var out bytes.Buffer
		code, diagnostic, _ := runEcho(nil, &out, test.args...)
		if code != 0 || diagnostic != "" {
			t.Fatalf("args=%v code=%d stderr=%q", test.args, code, diagnostic)
		}
		if out.String() != test.want {
			t.Fatalf("args=%v 得到 %q，want %q", test.args, out.String(), test.want)
		}
	}
}

func TestOptionScanAndPosixMode(t *testing.T) {
	for _, test := range []struct {
		env  []string
		args []string
		want string
	}{
		{nil, []string{"-n", "-e", "x"}, "x"},
		{nil, []string{"--", "x"}, "-- x\n"},
		{nil, []string{"-na", "x"}, "-na x\n"},
		{nil, []string{"-nne", "x"}, "x"},
		{[]string{"POSIXLY_CORRECT=1"}, []string{"-n", "-e", `a\tb`}, "a\tb"},
		{[]string{"POSIXLY_CORRECT=1"}, []string{"-e", `a\tb`}, "-e a\tb\n"},
	} {
		var out bytes.Buffer
		code, _, _ := runEcho(test.env, &out, test.args...)
		if code != 0 || out.String() != test.want {
			t.Fatalf("env=%v args=%v 得到 %q code=%d，want %q", test.env, test.args, out.String(), code, test.want)
		}
	}
}

func TestVersionSoleArgument(t *testing.T) {
	var out bytes.Buffer
	if code, _, _ := runEcho(nil, &out, "--version"); code != 0 || !strings.Contains(out.String(), "echo") {
		t.Fatalf("--version: %q code=%d", out.String(), code)
	}
	out.Reset()
	if code, _, _ := runEcho(nil, &out, "--version", "x"); code != 0 || out.String() != "--version x\n" {
		t.Fatalf("--version x: %q code=%d", out.String(), code)
	}
}

func TestWriteFailure(t *testing.T) {
	code, diagnostic, _ := runEcho(nil, io.Discard, "-e", "x")
	if code != 0 || diagnostic != "" {
		t.Fatalf("io.Discard 正常路径: code=%d stderr=%q", code, diagnostic)
	}
	rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{Out: failWriter{}, Err: io.Discard}}
	if code := cmd.Run(rc, []string{"x"}); code != 1 {
		t.Fatalf("写失败应返回 1，得到 %d", code)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
