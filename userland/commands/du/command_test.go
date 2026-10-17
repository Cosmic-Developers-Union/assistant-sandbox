package ducmd

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
	root := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"f1": 10, "sub/f2": 30} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat("a", size)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		args []string
		want string
	}{

		{"summary", []string{"-s", "tree"}, "1 tree\n"},

		{"bytes", []string{"-a", "-b", "tree"}, "10 tree/f1\n30 tree/sub/f2\n30 tree/sub\n40 tree\n"},
		{"all-blocks", []string{"-a", "tree"}, "1 tree/f1\n1 tree/sub/f2\n1 tree/sub\n1 tree\n"},
		{"max-depth", []string{"-b", "--max-depth=1", "tree"}, "30 tree/sub\n40 tree\n"},
		{"grand-total", []string{"-b", "-c", "tree/tree-does-not-exist"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, dir, tc.args, "")
			if tc.want == "" {
				if result.Code != 1 || result.Stderr == "" {
					t.Fatalf("缺失路径得到 %+v", result)
				}
				return
			}
			if result.Code != 0 || result.Stderr != "" || result.Stdout != tc.want {
				t.Fatalf("du %q = (%q, %q, %d), want %q", tc.args, result.Stdout, result.Stderr, result.Code, tc.want)
			}
		})
	}

	result := commandtest.Run(t, Run, dir, []string{"-x", "tree"}, "")
	if result.Code != 2 || !strings.Contains(result.Stderr, "one-file-system") {
		t.Fatalf("-x 得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"--time=atime", "tree"}, "")
	if result.Code != 2 || !strings.Contains(result.Stderr, "--time=atime") {
		t.Fatalf("--time=atime 得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"--not-a-flag"}, ""); result.Code != 2 {
		t.Fatalf("未知参数得到 %+v", result)
	}

	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
