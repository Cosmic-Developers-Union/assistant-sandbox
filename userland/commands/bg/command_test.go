package bg

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestBGRefuses(t *testing.T) {
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, Run, t.TempDir(), args, "")
	}
	if r := run("%1"); r.Code != 1 || !strings.Contains(r.Stderr, "作业控制") {
		t.Fatalf("bg %%1 = %+v", r)
	}
	if r := run(); r.Code != 1 || !strings.Contains(r.Stderr, "作业控制") {
		t.Fatalf("bg = %+v", r)
	}
}

func TestBgRejectsBadFlag(t *testing.T) {
	r := commandtest.Run(t, Run, t.TempDir(), []string{"--bogus"}, "")
	if r.Code != 2 || !strings.Contains(r.Stderr, "参数错误") {
		t.Fatalf("未知选项 = %+v", r)
	}
}
