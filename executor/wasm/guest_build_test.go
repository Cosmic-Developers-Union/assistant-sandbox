package wasm

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGuestBuildDoesNotPullInHostRuntime(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("缺少 go 命令: %v", err)
	}
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./")
	command.Env = append(command.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("查询 wasip1 依赖失败: %v", err)
	}
	dependencies := strings.Fields(string(output))
	if len(dependencies) == 0 {
		t.Fatal("依赖列表为空，判据失效")
	}
	for _, dependency := range dependencies {

		if strings.Contains(dependency, "wazero") {
			t.Fatalf("wasip1 构建依赖了宿主 WASM 运行时 %s：\n"+
				"这会排除「客体校验 + 宿主执行」选项，并使发布资产增大约三分之一。\n"+
				"若要改为客体执行，请先更新 docs/sandbox/wasm-execution-decision.md 与能力矩阵。",
				dependency)
		}
	}

	hostCommand := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./")
	hostCommand.Env = append(hostCommand.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	hostOutput, err := hostCommand.Output()
	if err != nil {
		t.Fatalf("查询宿主依赖失败: %v", err)
	}
	if !strings.Contains(string(hostOutput), "wazero") {
		t.Fatal("宿主构建应包含 WASM 运行时；若已移除，请同步本测试与决策文档")
	}
}
