package linkcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"a", "b"}, ""); result.Code != 0 {
		t.Fatalf("建立硬链接: %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(dir, "b"))
	if err != nil || string(content) != "value" {
		t.Fatalf("硬链接内容: %q %v", content, err)
	}
	result := commandtest.Run(t, Run, dir, []string{"a", "b"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("目标已存在应失败: %+v", result)
	}
	for _, args := range [][]string{{}, {"a"}, {"a", "b", "c"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
	result = commandtest.Run(t, Run, dir, []string{"absent", "c"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("源缺失应失败: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--bad", "a", "c"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("未知参数得到 %+v", result)
	}
}
