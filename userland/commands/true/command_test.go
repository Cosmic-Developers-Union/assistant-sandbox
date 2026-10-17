package truecmd

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	if result := commandtest.Run(t, Run, dir, nil, ""); result.Code != 0 || result.Stdout != "" {
		t.Fatalf("无参数: %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"ignored"}, ""); result.Code != 0 {
		t.Fatalf("忽略多余操作数: %+v", result)
	}
	for _, flag := range []string{"--help", "-h"} {
		result := commandtest.Run(t, Run, dir, []string{flag}, "")
		if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
			t.Fatalf("%s: %+v", flag, result)
		}
	}
	if result := commandtest.Run(t, Run, dir, []string{"--version"}, ""); result.Code != 0 {
		t.Fatalf("--version: %+v", result)
	}
}
