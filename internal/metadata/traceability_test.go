package metadata_test

import (
	"os"
	"strings"
	"testing"
)

// TestTODOCheckoffDiscipline 强制 A10 追踪纪律:TODO.md 中每个已勾选
// 任务必须同时携带 证据:/测试:/CI: 三要素;未勾选任务不得携带证据。
// 这使需求追踪清单可执行:缺证据的勾选在 CI 直接失败。
func TestTODOCheckoffDiscipline(t *testing.T) {
	content, err := os.ReadFile("../../TODO.md")
	if err != nil {
		t.Fatalf("读取 TODO.md: %v", err)
	}
	checked, uncheckedWithEvidence := 0, 0
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- [") {
			continue
		}
		id := taskID(trimmed)
		if id == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- [x]") {
			checked++
			// 全角标点规范化后再校验(仓库文档使用全角冒号)。
			normalized := strings.ReplaceAll(trimmed, "：", ":")
			for _, field := range []string{"证据", "测试", "CI"} {
				if !strings.Contains(normalized, field+":") {
					t.Errorf("%s 已勾选但缺少 %q 字段", id, field)
				}
			}
			continue
		}
		if strings.Contains(trimmed, "证据:") {
			uncheckedWithEvidence++
			t.Errorf("%s 未勾选却携带证据(半成品不得伪装完成)", id)
		}
	}
	if checked == 0 {
		t.Fatal("未解析到任何已勾选任务,解析逻辑可能失效")
	}
	t.Logf("已勾选 %d 项,全部具备证据/测试/CI 三要素;未勾选带证据 %d 项", checked, uncheckedWithEvidence)
}

func taskID(line string) string {
	rest := strings.TrimPrefix(line, "- [x]")
	rest = strings.TrimPrefix(rest, "- [ ]")
	rest = strings.TrimSpace(rest)
	field := strings.Fields(rest)
	if len(field) == 0 {
		return ""
	}
	return field[0]
}
