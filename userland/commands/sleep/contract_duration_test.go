package sleep

import (
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestDurationErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"s"}, {".5"}, {"1.x"}, {"1.."}, {"1e3"}, {"1S"},
	} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestFractions(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"0.5"}, {"1.25s"}, {"0.5m"}, {"0.5h"}, {"0.5d"}} {
		if result := commandtest.Run(t, Run, dir, args, ""); result.Code != 0 {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}
