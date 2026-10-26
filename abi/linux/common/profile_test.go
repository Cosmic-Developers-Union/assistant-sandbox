package common

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// profileSections 从兼容配置文档提取三张系统调用表的名字集合。
// 表行格式为 "| name | ... |";分隔标题以 "### " 开头。
func profileSections(t *testing.T) (delivered, unsupported, declared map[string]bool) {
	t.Helper()
	content, err := os.ReadFile("../../../docs/compat/linux-profile.md")
	if err != nil {
		t.Fatalf("读取兼容配置文档: %v", err)
	}
	section := ""
	delivered = map[string]bool{}
	unsupported = map[string]bool{}
	declared = map[string]bool{}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "### ") {
			section = strings.TrimSpace(line[4:])
			continue
		}
		if !strings.HasPrefix(line, "|") {
			continue
		}
		fields := strings.Split(strings.Trim(line, "|"), "|")
		// 单元格内以 " / " 分组的多名(如 "setuid / setgid")逐名展开。
		names := strings.Split(strings.TrimSpace(fields[0]), "/")
		if strings.TrimSpace(fields[0]) == "" || strings.HasPrefix(strings.TrimSpace(fields[0]), "调用") {
			continue
		}
		for _, raw := range names {
			name := strings.TrimSpace(raw)
			if name == "" || strings.HasPrefix(name, "-") || name == "调用" {
				continue
			}
			switch section {
			case "已交付语义(77 项)":
				delivered[name] = true
			case "明确 ENOTSUP(31 项:在册、语义未交付)":
				unsupported[name] = true
			case "声明语义(4 项:返回值即配置,不是缺陷)":
				declared[name] = true
			}
		}
	}
	return delivered, unsupported, declared
}

// TestProfileSyscallTablesMatchRegistry 锁定 A03:兼容配置文档的三张
// 系统调用表与登记表(Registered)逐名一致——并集恰好覆盖全部登记项,
// 无遗漏、无多余;ENOTSUP 表中每一项实际返回 ENOTSUP。
func TestProfileSyscallTablesMatchRegistry(t *testing.T) {
	delivered, unsupported, declared := profileSections(t)
	registered := map[string]Syscall{}
	for _, call := range Registered() {
		registered[call.String()] = call
	}
	for _, table := range []struct {
		name string
		set  map[string]bool
	}{{"已交付", delivered}, {"ENOTSUP", unsupported}, {"声明", declared}} {
		for name := range table.set {
			if _, ok := registered[name]; !ok {
				t.Errorf("%s 表中的 %q 未登记(先登记再入配置)", table.name, name)
			}
		}
	}
	covered := map[string]bool{}
	for name := range delivered {
		if unsupported[name] || declared[name] {
			t.Errorf("%q 同时出现在多张表", name)
		}
		covered[name] = true
	}
	for name := range unsupported {
		if declared[name] {
			t.Errorf("%q 同时出现在多张表", name)
		}
		covered[name] = true
	}
	for name := range declared {
		covered[name] = true
	}
	for name := range registered {
		if !covered[name] {
			t.Errorf("登记项 %q 未出现在兼容配置任何一张表", name)
		}
	}
	for name := range covered {
		if _, ok := registered[name]; !ok {
			t.Errorf("配置表中的 %q 不在登记表", name)
		}
	}

	// ENOTSUP 表与真实行为一致:逐项断言 Handle 返回 ENOTSUP。
	handler := Syscalls{Memory: newFakeMemory(), Identity: &Identity{PID: 1}}
	names := make([]string, 0, len(unsupported))
	for name := range unsupported {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if reply := handler.Handle(context.Background(), Call{Syscall: registered[name]}); reply.Errno != errno.NotSupported {
			t.Errorf("%s: reply=%+v want ENOTSUP(文档声明与行为不一致)", name, reply)
		}
	}
}
