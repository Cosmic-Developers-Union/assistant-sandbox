package csplitcmd

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("a\nb\nc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"data", "1"}, "")
	if result.Code != 0 {
		t.Fatalf("端到端得到 %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "xx00")); err != nil {
		t.Fatalf("应生成 xx00: %v", err)
	}
	result = commandtest.Run(t, Run, dir, []string{"absent", "1"}, "")
	if result.Code == 0 || result.Stderr == "" {
		t.Fatalf("缺失输入得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--bad"}, "")
	if result.Code == 0 {
		t.Fatalf("未知参数得到 %+v", result)
	}
	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
