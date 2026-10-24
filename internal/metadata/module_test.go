package metadata_test

import (
	"os"
	"strings"
	"testing"
)

// TestModuleResolvesWithoutLocalReplacements(B01):go.mod 不得携带
// replace 指令或宿主绝对路径,依赖全部来自发布版本;配合 CI 的全新
// 检出构建,保证任何环境都能解析与构建本仓库。
func TestModuleResolvesWithoutLocalReplacements(t *testing.T) {
	raw, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "replace ") {
			t.Fatalf("go.mod 存在 replace 指令: %q", trimmed)
		}
		if strings.Contains(trimmed, "/home/") || strings.Contains(trimmed, "/Users/") {
			t.Fatalf("go.mod 含宿主绝对路径: %q", trimmed)
		}
	}
	sum, err := os.ReadFile("../../go.sum")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sum), "/home/") {
		t.Fatal("go.sum 含宿主绝对路径")
	}
}
