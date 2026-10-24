package metadata_test

import (
	"os"
	"strings"
	"testing"
)

// TestThirdPartyCoversModuleDependencies(B10):go.mod 的每个第三方依赖
// 都必须在 THIRD_PARTY.md 登记(模块路径可检索),防止发布物携带
// 无来源记录的依赖。
func TestThirdPartyCoversModuleDependencies(t *testing.T) {
	raw, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := os.ReadFile("../../THIRD_PARTY.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := string(inventory)
	var missing []string
	inRequire := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "require (" {
			inRequire = true
			continue
		}
		if inRequire && trimmed == ")" {
			inRequire = false
			continue
		}
		if !inRequire || trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		module := strings.Fields(trimmed)[0]
		if !strings.Contains(module, ".") || strings.HasPrefix(module, "github.com/Cosmic-Developers-Union") {
			continue // 标准库与本模块自身
		}
		if !strings.Contains(documented, module) {
			missing = append(missing, module)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("以下 go.mod 依赖未在 THIRD_PARTY.md 登记: %v", missing)
	}
}
