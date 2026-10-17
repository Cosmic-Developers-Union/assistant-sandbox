package testcmd

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"file-exists", []string{"-f", "f"}, 0},
		{"file-missing", []string{"-f", "absent"}, 1},
		{"string-equal", []string{"x", "=", "x"}, 0},
		{"integer-equal", []string{"7", "-eq", "7"}, 0},
		{"no-operands", nil, 1},
		{"syntax-error", []string{"x", "y"}, 2},

		{"bracket-is-an-operand", []string{"["}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, dir, tc.args, "")
			if result.Code != tc.want {
				t.Fatalf("test %q = (%q, %q, %d), want %d", tc.args, result.Stdout, result.Stderr, result.Code, tc.want)
			}
		})
	}

	if got := commandtest.Run(t, Run, dir, []string{"x", "=", "x", "]"}, ""); got.Code != 2 {
		t.Fatalf("注册名 test 不应接受闭括号，得到 %+v", got)
	}

	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
