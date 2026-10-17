package pathchkcmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func runGuest(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	err := commandtest.Invoke(t.Context(), func(ctx context.Context, argv []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
		if err != nil {
			return err
		}
		release := vfs.Bind(system, "/", nil)
		defer release()
		if err := vfs.MkdirAll(path.Join(dir, "sub", "leaf"), 0700); err != nil {
			return err
		}
		if err := vfs.WriteFile(path.Join(dir, "file"), []byte("x"), 0600); err != nil {
			return err
		}
		clockSource, _ := p.Resources.Time()
		descriptors, _ := p.Resources.Descriptors()
		random, _ := p.Resources.Random()
		p.Resources = capability.Set{FS: system, FD: descriptors, Entropy: random, Clock: clockSource}
		return Run(proc.WithContext(ctx, p), argv)
	}, dir, args, strings.NewReader(""), &out, &diagnostic)
	code := 0
	if err != nil {
		status, ok := errors.AsType[interp.ExitStatus](err)
		if !ok {
			t.Fatal(err)
		}
		code = int(status)
	}
	return code, out.String(), diagnostic.String()
}

func TestDefaultChecks(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"sub"}, 0},
		{[]string{"sub/leaf"}, 0},
		{[]string{"sub/absent"}, 0},
		{[]string{"file"}, 0},
		{[]string{"sub/"}, 0},
		{[]string{"file/child"}, 1},
		{[]string{""}, 1},
	} {
		code, out, diagnostic := runGuest(t, dir, test.args...)
		if code != test.code {
			t.Fatalf("args=%v 得到 code=%d stderr=%q，want %d", test.args, code, diagnostic, test.code)
		}
		if test.code != 0 && diagnostic == "" {
			t.Fatalf("args=%v 报错却没有诊断 (out=%q)", test.args, out)
		}
	}
}

func TestPosixAndSpecial(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("a", posixPathMax)
	component := strings.Repeat("b", posixNameMax+1)
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"-p", "ok/name"}, 0},
		{[]string{"-p", "a.b_c-d"}, 0},
		{[]string{"-p", long}, 1},
		{[]string{"-p", "ok/" + component}, 1},
		{[]string{"-p", ""}, 0},
		{[]string{"-p", "bad" + string(rune(0x80))}, 1},
		{[]string{}, 2},
		{[]string{"--bad"}, 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != test.code {
			t.Fatalf("args=%v 得到 %+v，want code=%d", test.args, result, test.code)
		}
		if test.code != 0 && result.Stderr == "" {
			t.Fatalf("args=%v 报错却没有诊断: %+v", test.args, result)
		}
	}
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"-P", "sub"}, 0},
		{[]string{"-P", ""}, 1},
		{[]string{"-P", "--", "-leading"}, 1},
		{[]string{"--portability", "sub"}, 0},
		{[]string{"--portability", ""}, 1},
	} {
		code, _, diagnostic := runGuest(t, dir, test.args...)
		if code != test.code {
			t.Fatalf("args=%v 得到 code=%d stderr=%q，want %d", test.args, code, diagnostic, test.code)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help 得到 %+v", result)
	}
}

func TestLimitBranches(t *testing.T) {
	rc := &toolcompat.RunContext{Dir: "/work", Stdio: toolcompat.Stdio{Err: io.Discard}}
	limits := func(string) (int, int, error) { return 8, 3, nil }
	if checkDefaultWith(rc, "abcdefghij", limits) {
		t.Fatal("超过 PATH_MAX 应失败")
	}
	if checkDefaultWith(rc, "abcd", limits) {
		t.Fatal("超过 NAME_MAX 应失败")
	}
}

func TestPathPartsAndJoins(t *testing.T) {
	rc := &toolcompat.RunContext{Dir: "/work"}
	base, components, trailing := pathParts(rc, "a/b/")
	if base != "/work" || !trailing || strings.Join(components, "|") != "a|b|" {
		t.Fatalf("相对路径切分: %q %v %v", base, components, trailing)
	}
	base, components, trailing = pathParts(rc, "/abs/x")
	if base != "/" || strings.Join(components, "|") != "abs|x" || trailing {
		t.Fatalf("绝对路径切分: %q %v %v", base, components, trailing)
	}
	empty := &toolcompat.RunContext{}
	if base, _, _ = pathParts(empty, "rel"); base != "." {
		t.Fatalf("空工作目录的基: %q", base)
	}
	if got := rawPathJoin("/work", "sub"); got != "/work/sub" {
		t.Fatalf("拼接: %q", got)
	}
	if got := rawPathJoin("/work/", "sub"); got != "/work/sub" {
		t.Fatalf("带结尾分隔符拼接: %q", got)
	}
	if got := rawPathJoin("", "sub"); got != "/sub" {
		t.Fatalf("空基拼接: %q", got)
	}
	if pathMax, nameMax, err := filesystemLimits("/work"); err != nil || pathMax != 0 || nameMax != 0 {
		t.Fatalf("客体限制未知: %d %d %v", pathMax, nameMax, err)
	}
}

func TestLimitFailures(t *testing.T) {
	rc := &toolcompat.RunContext{Dir: "/work", Stdio: toolcompat.Stdio{Err: io.Discard}}
	if checkDefaultWith(rc, "name", func(string) (int, int, error) {
		return 0, 0, io.ErrUnexpectedEOF
	}) {
		t.Fatal("限制查询失败应报告失败")
	}
	if checkDefaultWith(rc, "", func(string) (int, int, error) { return 0, 0, nil }) {
		t.Fatal("空路径应报告失败")
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
