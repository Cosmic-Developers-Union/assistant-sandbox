package sleep

import (
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"0"}, {"0.5"}, {"1s"}, {"1m"}, {"1h"}, {"1d"}, {"0", "1s"}} {
		if result := commandtest.Run(t, Run, dir, args, ""); result.Code != 0 {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
	for _, args := range [][]string{{}, {"abc"}, {"-1"}, {"--bad"}, {"--help"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}
