package joincmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one"), []byte("a 1\nb 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two"), []byte("a x\nb y\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"one", "two"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "a 1 x") {
		t.Fatalf("端到端得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "1", "one", "two"}, "")
	if result.Code != 0 {
		t.Fatalf("-a 1 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"one"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("缺操作数得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"one", "absent"}, "")
	if result.Code == 0 || result.Stderr == "" {
		t.Fatalf("缺失输入得到 %+v", result)
	}
	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
