package batch

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestBatchReusesAtImplementation(t *testing.T) {

	r := commandtest.Run(t, Run, t.TempDir(), nil, "echo\n")
	if r.Code != 2 || !strings.Contains(r.Stderr, "batch: 需要时间操作数") {
		t.Fatalf("batch 入口 = %+v", r)
	}
}
