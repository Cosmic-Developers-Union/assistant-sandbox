package bccmd

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, nil, "2+3\n")
	if result.Code != 0 || result.Stdout != "5\n" || result.Stderr != "" {
		t.Fatalf("标准输入求值得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-l"}, "scale\ns(0)\n")
	if result.Code != 0 || result.Stdout != "20\n0\n" {
		t.Fatalf("-l 数学库得到 %+v", result)
	}

	if err := os.WriteFile(filepath.Join(dir, "program.bc"), []byte("x=41\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"program.bc"}, "x+1\n")
	if result.Code != 0 || result.Stdout != "42\n" {
		t.Fatalf("程序文件操作数得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"absent.bc"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("缺失程序文件得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"--not-a-flag"}, ""); result.Code != 2 {
		t.Fatalf("未知参数得到 %+v", result)
	}

	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}

func TestBCFileOperandStatuses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "quit.bc"), []byte("quit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"quit.bc"}, "1+1\n")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("quit 程序文件得到 %+v", result)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.bc"), []byte("1+\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"bad.bc"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("语法错误程序文件得到 %+v", result)
	}
}

func TestBCEmptyStandardInput(t *testing.T) {
	rc := &toolcompat.RunContext{
		Ctx: t.Context(), Dir: t.TempDir(),
		Stdio: toolcompat.Stdio{In: nil, Out: io.Discard, Err: io.Discard},
	}
	if code := run(rc, nil); code != 0 {
		t.Fatalf("无标准输入得到退出码 %d", code)
	}
}
