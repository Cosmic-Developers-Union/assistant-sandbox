package exprcmd

import (
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"1", "+", "1"}, "")
	if result.Code != 0 || result.Stdout != "2\n" {
		t.Fatalf("端到端得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 {
		t.Fatalf("--help 得到 %+v", result)
	}
	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
