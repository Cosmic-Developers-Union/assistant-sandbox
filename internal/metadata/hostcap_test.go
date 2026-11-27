// hostcap_test.go 审计宿主能力入口(O07):生产源码的宿主文件/进程/
// 网络/随机/时间导入必须落在白名单内,新增入口先登记到
// docs/architecture/host-capabilities.md 并更新本表。
package metadata

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var hostImportAllowlist = map[string]map[string]bool{
	"os": {
		"cmd/assistant-sandbox/main.go":              true,
		"cmd/assistant-sandbox/pty.go":               true,
		"cmd/assistant-sandbox/signals_unix.go":      true,
		"cmd/assistant-sandbox/signals_windows.go":   true,
		"filesystem/persistent/store.go":             true,
		"internal/diff/diff.go":                      true,
		"instance/native_probe_linux.go":             true,
		"platform/filesystem/filesystem_native.go":   true,
		"platform/filesystem/filesystem_wasi.go":     true,
		"platform/filesystem/modes_native.go":        true,
		"platform/filesystem/timestamps_wasi.go":     true,
		"testdata/extension.go":                      true,
		"userland/toolcompat/goawk/interp/guest.go":  true,
		"userland/toolcompat/goawk/interp/interp.go": true,
		"userland/toolcompat/goawk/interp/io.go":     true,
	},
	"os/exec": {
		"internal/diff/diff.go":                        true,
		"userland/toolcompat/goawk/interp/io.go":       true,
		"userland/toolcompat/goawk/interp/iostream.go": true,
	},
	"syscall": {
		"cmd/assistant-sandbox/signals_unix.go":        true,
		"instance/native_probe_linux.go":               true,
		"platform/filesystem/accesstime_linux.go":      true,
		"platform/filesystem/accesstime_wasi.go":       true,
		"platform/filesystem/fifo_linux.go":            true,
		"platform/filesystem/space_linux.go":           true,
		"platform/filesystem/timestamps_wasi.go":       true,
		"userland/toolcompat/goawk/interp/iostream.go": true,
	},
	"crypto/rand": {
		"platform/entropy/entropy_native.go": true,
		"platform/entropy/entropy_wasi.go":   true,
	},
	"unsafe": {
		"platform/modules/modules_wasi.go": true,
		// 原生批量执行:只传递持锁页/译码切片地址,不是宿主资源入口。
		"executor/cpu/riscv64/batch.go": true, "executor/cpu/riscv64/hot_cgo.go": true,
		"vm/memory/table.go": true,
	},
	"net": {
		"platform/network/network.go": true,
	},
}

var processLaunchAllowlist = map[string]bool{
	"internal/diff/diff.go":                  true,
	"instance/native_probe_linux.go":         true,
	"userland/toolcompat/goawk/interp/io.go": true, "userland/toolcompat/goawk/interp/iostream.go": true,
}

// TestHostCapabilitiesAreAllowlisted 验证 O07:宿主能力入口经过集中
// 授权,客体执行路径不能直接启动宿主程序或读取宿主资源。
func TestHostCapabilitiesAreAllowlisted(t *testing.T) {
	root := "../.."
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "licenses", "docs", "testdata", "var", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		source, err := os.ReadFile(path) //nolint:gosec // 仓库内只读审计,路径来自 WalkDir
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			allowed, audited := hostImportAllowlist[name]
			if !audited {
				continue
			}
			if !allowed[relative] {
				t.Errorf("未登记的宿主能力入口: %s import %q(先在 docs/architecture/host-capabilities.md 登记)", relative, name)
			}
		}
		text := string(source)
		for _, marker := range []string{"exec.Command(", "exec.CommandContext(", "os.StartProcess(", "syscall.Exec(", "syscall.ForkExec("} {
			if strings.Contains(text, marker) && !processLaunchAllowlist[relative] {
				t.Errorf("未登记的宿主进程启动: %s 使用 %s", relative, marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 客体 AWK 必须保持 NoExec,禁止 system()/管道落到宿主 shell。
	awk, err := os.ReadFile("../../userland/commands/awk/awk.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(awk), "NoExec:       true") && !strings.Contains(string(awk), "NoExec: true") {
		t.Fatal("客体 awk 必须启用 NoExec")
	}
}
