package tput

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestTPutAnswersSizeFacts(t *testing.T) {
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, commandtest.WithTerminal(t, Run), t.TempDir(), args, "")
	}
	if r := run("cols"); r.Code != 0 || r.Stdout != "80\n" {
		t.Fatalf("cols = %+v", r)
	}
	if r := run("lines"); r.Code != 0 || r.Stdout != "24\n" {
		t.Fatalf("lines = %+v", r)
	}
	if r := run("rows"); r.Code != 0 || r.Stdout != "24\n" {
		t.Fatalf("rows = %+v", r)
	}
}

func TestTPutRejectsMissingTerminfo(t *testing.T) {
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, commandtest.WithTerminal(t, Run), t.TempDir(), args, "")
	}
	if r := run("cup", "10", "20"); r.Code != 1 || !strings.Contains(r.Stderr, "terminfo") {
		t.Fatalf("cup = %+v", r)
	}
	if r := run("-T", "xterm", "cols"); r.Code != 1 {
		t.Fatalf("-T = %+v", r)
	}

	if r := commandtest.Run(t, Run, t.TempDir(), []string{"cols"}, ""); r.Code != 1 || !strings.Contains(r.Stderr, "不是终端") {
		t.Fatalf("无终端 = %+v", r)
	}
	if r := run(); r.Code != 2 {
		t.Fatalf("缺操作数 = %+v", r)
	}
	if r := run("--bogus"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}
