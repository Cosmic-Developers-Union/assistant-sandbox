package pwdcmd

import (
	"bytes"
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

func TestDefaultPhysical(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, nil, "")
	if result.Code != 0 || result.Stdout != dir+"\n" {
		t.Fatalf("得到 %+v，want %q", result, dir+"\n")
	}
	if result := commandtest.Run(t, Run, dir, []string{"-P"}, ""); result.Code != 0 || result.Stdout != dir+"\n" {
		t.Fatalf("-P 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"operand"}, "")
	if result.Code != 0 || result.Stdout != dir+"\n" || !strings.Contains(result.Stderr, "ignoring non-option arguments") {
		t.Fatalf("多余操作数得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"--help"}, ""); result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help 得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"--bad"}, ""); result.Code != 2 {
		t.Fatalf("未知参数得到 %+v", result)
	}
}

func runDir(t *testing.T, base, dir string, env []string, args ...string) (int, string, string) {
	t.Helper()
	release := vfs.Bind(filesystem.NewFilesystem(base), base, nil)
	defer release()
	var out, diagnostic bytes.Buffer
	rc := &toolcompat.RunContext{Dir: dir, Env: env, Stdio: toolcompat.Stdio{Out: &out, Err: &diagnostic}}
	code := cmd.Run(rc, args)
	return code, out.String(), diagnostic.String()
}

func TestLogicalMode(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := runDir(t, dir, dir, []string{"PWD=" + dir}, "-L")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("-L 采用 PWD 得到 code=%d out=%q", code, out)
	}
	code, out, _ = runDir(t, dir, dir, []string{"PWD=/elsewhere"}, "-L")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("-L 忽略不匹配的 PWD 得到 code=%d out=%q", code, out)
	}
	code, out, _ = runDir(t, dir, dir, []string{"PWD=" + dir + "/dot/.."}, "-L")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("-L 拒绝带点组件得到 code=%d out=%q", code, out)
	}
	code, out, _ = runDir(t, dir, dir, []string{"PWD=relative"}, "-L")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("-L 拒绝相对 PWD 得到 code=%d out=%q", code, out)
	}
	code, out, _ = runDir(t, dir, dir, []string{"POSIXLY_CORRECT=1", "PWD=" + dir}, "-L")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("POSIX 模式得到 code=%d out=%q", code, out)
	}
}

func TestPhysicalResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real", "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(root, "real", "child")
	code, out, diagnostic := runDir(t, root, filepath.Join(root, "link", "child"), nil, "-P")
	if code != 0 || out != physical+"\n" {
		t.Fatalf("符号链接解析得到 code=%d out=%q stderr=%q", code, out, diagnostic)
	}

	code, out, _ = runDir(t, root, filepath.Join(root, "absent", "child"), nil, "-P")
	if code != 0 || out != filepath.Join(root, "absent", "child")+"\n" {
		t.Fatalf("缺失组件得到 code=%d out=%q", code, out)
	}
}

func TestLastModeWins(t *testing.T) {
	dir := t.TempDir()
	env := []string{"PWD=" + dir}
	code, out, _ := runDir(t, dir, dir, env, "-L", "-P")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("-L -P 得到 code=%d out=%q", code, out)
	}
	code, out, _ = runDir(t, dir, dir, env, "--physical", "--logical")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("--physical --logical 得到 code=%d out=%q", code, out)
	}
	code, out, _ = runDir(t, dir, dir, env, "-LP")
	if code != 0 || out != dir+"\n" {
		t.Fatalf("-LP 得到 code=%d out=%q", code, out)
	}
}

func TestDirectFailures(t *testing.T) {
	rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{Out: failWriter{}, Err: io.Discard}}
	if code := cmd.Run(rc, nil); code != 1 {
		t.Fatalf("空工作目录应返回 1: %d", code)
	}
	rc = &toolcompat.RunContext{Dir: "/work", Stdio: toolcompat.Stdio{Out: failWriter{}, Err: io.Discard}}
	if code := cmd.Run(rc, nil); code != 1 {
		t.Fatalf("写失败应返回 1: %d", code)
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

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestResolveLimits(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "b"), filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	if code, _, diagnostic := runDir(t, root, filepath.Join(root, "a"), nil, "-P"); code != 1 || !strings.Contains(diagnostic, "symbolic links") {
		t.Fatalf("符号链接环得到 code=%d stderr=%q", code, diagnostic)
	}
	relative := &toolcompat.RunContext{Dir: "relative", Stdio: toolcompat.Stdio{Out: io.Discard, Err: io.Discard}}
	if code := cmd.Run(relative, []string{"-P"}); code != 1 {
		t.Fatalf("相对工作目录应返回 1: %d", code)
	}
	dir := t.TempDir()
	if code, out, _ := runDir(t, dir, dir, []string{"PWD=" + dir}, "--logical=false"); code != 0 || out != dir+"\n" {
		t.Fatalf("--logical=false 得到 code=%d out=%q", code, out)
	}
	if code, _, _ := runDir(t, dir, dir, []string{"PWD=" + dir}, "--logical="); code != 2 {
		t.Fatalf("--logical= 空取值应报 2，得到 code=%d", code)
	}
	if code, _, _ := runDir(t, dir, dir, nil, "---x"); code != 2 {
		t.Fatalf("---x 应报 2，得到 code=%d", code)
	}
	if mode := lastLP([]string{"--", "-L"}, 'P'); mode != 'P' {
		t.Fatalf("-- 之后不再扫描: %c", mode)
	}
}
