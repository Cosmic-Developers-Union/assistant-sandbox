package uuencodecmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"mvdan.cc/sh/v3/interp"
)

func classicReference(data []byte) string {
	var out strings.Builder
	for offset := 0; offset < len(data); offset += 45 {
		end := offset + 45
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		out.WriteByte(byte(len(chunk)&0x3f) + 0x20)
		for index := 0; index < len(chunk); index += 3 {
			var value uint32
			for offset := 0; offset < 3; offset++ {
				value <<= 8
				if index+offset < len(chunk) {
					value |= uint32(chunk[index+offset])
				}
			}
			out.WriteByte(byte(value>>18)&0x3f + 0x20)
			out.WriteByte(byte(value>>12)&0x3f + 0x20)
			out.WriteByte(byte(value>>6)&0x3f + 0x20)
			out.WriteByte(byte(value)&0x3f + 0x20)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
		code  int
	}{
		{[]string{"remote"}, "hello", "begin 666 remote\n" + classicReference([]byte("hello")) + " \nend\n", 0},
		{[]string{"remote"}, "", "begin 666 remote\n \nend\n", 0},
		{[]string{"-m", "remote"}, "hello", "begin-base64 666 remote\naGVsbG8=\n====\n", 0},
		{[]string{"data", "remote"}, "", "begin 644 remote\n" + classicReference([]byte("hello")) + " \nend\n", 0},
		{[]string{"-m", "data", "remote"}, "", "begin-base64 644 remote\naGVsbG8=\n====\n", 0},
		{[]string{}, "x", "", 2},
		{[]string{"a", "b", "c"}, "x", "", 2},
		{[]string{"absent", "remote"}, "", "", 1},
		{[]string{"--bad", "remote"}, "x", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want code=%d stdout=%q", test.args, result, test.code, test.want)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help 得到 %+v", result)
	}
}

func TestPosixModeAndFailures(t *testing.T) {
	cmdEnv := func(env []string, in io.Reader, out, diag io.Writer, args ...string) int {
		return cmd.Run(&toolcompat.RunContext{Env: env, Stdio: toolcompat.Stdio{In: in, Out: out, Err: diag}}, args)
	}
	var out bytes.Buffer
	if code := cmdEnv([]string{"POSIXLY_CORRECT=1"}, strings.NewReader("hi"), &out, io.Discard, "remote"); code != 0 {
		t.Fatalf("POSIX 模式得到 %d", code)
	}
	if !strings.HasPrefix(out.String(), "begin 666 remote\n") {
		t.Fatalf("POSIX 模式输出: %q", out.String())
	}
	out.Reset()
	if code := cmdEnv(nil, failReader{}, &out, io.Discard, "remote"); code != 1 {
		t.Fatalf("读失败应返回 1: %d", code)
	}
	out.Reset()
	out.Reset()
	if code := cmdEnv([]string{"POSIXLY_CORRECT=1"}, strings.NewReader("hi"), &out, io.Discard, "-m", "remote"); code != 0 || !strings.HasPrefix(out.String(), "begin-base64 666 remote\n") {
		t.Fatalf("POSIX 模式 -m 得到 code=%d out=%q", code, out.String())
	}
}

func TestWriteFailures(t *testing.T) {
	for _, args := range [][]string{{"remote"}, {"-m", "remote"}} {
		rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{In: strings.NewReader("hello"), Out: failWriter{}, Err: io.Discard}}
		if code := cmd.Run(rc, args); code != 1 {
			t.Fatalf("args=%v 写失败应返回 1: %d", args, code)
		}
	}
}

var errReadFailed = errors.New("读取失败")

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errReadFailed }

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type onceWriter struct{ writes int }

func (w *onceWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, io.ErrClosedPipe
	}
	return len(data), nil
}

func TestEncodingFailures(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		input io.Reader
		out   io.Writer
	}{
		{"classic-data-write", []string{"remote"}, strings.NewReader("hello"), &onceWriter{}},
		{"classic-terminator-write", []string{"remote"}, strings.NewReader(""), &onceWriter{}},
		{"base64-read", []string{"-m", "remote"}, failReader{}, io.Discard},
		{"base64-empty", []string{"-m", "remote"}, strings.NewReader(""), &onceWriter{}},
		{"base64-data-write", []string{"-m", "remote"}, strings.NewReader("hello"), &onceWriter{}},
	} {
		rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{In: test.input, Out: test.out, Err: io.Discard}}
		if code := cmd.Run(rc, test.args); code != 1 {
			t.Fatalf("%s: 应返回 1，得到 %d", test.name, code)
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
