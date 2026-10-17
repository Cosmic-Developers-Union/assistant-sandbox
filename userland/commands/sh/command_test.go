package sh

import (
	"io"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

func testRunner(dir string, env []string, in io.Reader, out, diagnostic io.Writer) (*interp.Runner, error) {
	return interp.New(interp.Dir(dir), interp.Env(expand.ListEnviron(env...)), interp.StdIO(in, out, diagnostic))
}

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	command := Command{NewRunner: testRunner}
	result := commandtest.Run(t, command.Run, dir, []string{"-c", "printf 子解释器"}, "")
	if result.Code != 0 || result.Stdout != "子解释器" {
		t.Fatalf("-c: %+v", result)
	}
	result = commandtest.Run(t, command.Run, dir, []string{"-c", "exit 7"}, "")
	if result.Code != 7 {
		t.Fatalf("退出码: %+v", result)
	}
	result = commandtest.Run(t, command.Run, dir, []string{"-s"}, "printf %s \"$1\"")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("-s: %+v", result)
	}
	result = commandtest.Run(t, command.Run, dir, []string{"--help"}, "")
	if result.Code != 0 {
		t.Fatalf("--help: %+v", result)
	}
	result = commandtest.Run(t, command.Run, dir, []string{"--bad"}, "")
	if result.Code != 2 {
		t.Fatalf("参数错误: %+v", result)
	}
}
