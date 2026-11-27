package vm

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestVMProductionCodeHasNoAmbientHostImports(t *testing.T) {
	boundary := map[string]map[string]bool{
		"platform/filesystem/filesystem_native.go": {"os": true, "path/filepath": true, "time": true},
		"platform/filesystem/modes_native.go":      {"os": true},
		"platform/filesystem/filesystem_wasi.go":   {"os": true},
		"platform/filesystem/timestamps_wasi.go":   {"os": true, "syscall": true},

		"platform/filesystem/accesstime_linux.go": {"syscall": true},
		"platform/filesystem/accesstime_wasi.go":  {"syscall": true},

		"platform/filesystem/space_linux.go": {"syscall": true},
		"platform/filesystem/fifo_linux.go":  {"syscall": true},
		"platform/modules/modules_wasi.go":   {"unsafe": true},

		"platform/entropy/entropy_native.go": {"crypto/rand": true},
		"platform/entropy/entropy_wasi.go":   {"crypto/rand": true},

		// M09 宿主出口适配器:仅由部署层注入,策略在 vm/net 裁决。
		"platform/network/network.go": {"net": true, "time": true},

		"filesystem/memfs/file.go": {"time": true},
		// 时钟读数只用于测量耗时(系统调用轨迹),不作为客体可见时间。
		"vm/clock/monotonic.go": {"time": true},

		"filesystem/persistent/store.go": {"os": true},

		"filesystem/procfs/procfs.go": {"time": true},

		"filesystem/devfs/devfs.go": {"time": true},

		// PTY 是宿主侧设备视图;time 仅用于 FileInfo.ModTime 的零值。
		"vm/pty/pty.go": {"time": true},
		// 标准流的 fstat 视图;time 仅用于 FileInfo.ModTime 的零值。
		"vm/fd/table.go": {"time": true},
		// memfd 的 FileInfo 只返回零值 ModTime;内容与时钟均由 VM 管理。
		"vm/memfd/memfd.go": {"time": true},
		// 套接字 fstat 视图只返回零值 ModTime。
		"vm/net/stat.go": {"time": true},
		// 管道 fstat 视图只返回零值 ModTime。
		"vm/pipe/pipe.go": {"time": true},
		// cgroupfs 的 FileInfo 只返回零值 ModTime;内容来自 VM 进程表。
		"instance/cgroupfs.go": {"time": true},

		// 原生批量执行把持锁期间的页/译码切片地址传给同进程 C++ 解释器;
		// 仅内存寻址,不访问宿主文件、网络、时钟或进程能力。
		"executor/cpu/riscv64/batch.go":   {"unsafe": true},
		"executor/cpu/riscv64/hot_cgo.go": {"unsafe": true},
		"vm/memory/table.go":              {"unsafe": true},

		"filesystem/packagefs/packagefs.go": {"time": true},

		"userland/toolcompat/goawk/interp/interp.go":   {"os": true, "math/rand": true},
		"userland/toolcompat/goawk/interp/io.go":       {"os": true, "os/exec": true},
		"userland/toolcompat/goawk/interp/iostream.go": {"os/exec": true, "syscall": true},
		"userland/toolcompat/goawk/interp/guest.go":    {"os": true},
	}
	forbidden := map[string]bool{"os": true, "os/exec": true, "time": true, "math/rand": true, "math/rand/v2": true, "crypto/rand": true, "net": true, "syscall": true, "unsafe": true, "path/filepath": true}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// model/ 是 A06/A07 的契约套件与参考实现宿主侧替身
			// (入口 Suite(t *testing.T,…)),只在宿主测试进程运行,
			// 永不进入客体执行;与 internal/、test/ 同属测试基础设施。
			if d.Name() == "testdata" || d.Name() == "build" || d.Name() == "generate" || d.Name() == "internal" || d.Name() == "cmd" || d.Name() == "test" || d.Name() == "model" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Dir(path) == ".." || filepath.Ext(path) != ".go" || len(path) >= 8 && path[len(path)-8:] == "_test.go" {
			return nil
		}
		source, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range source.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			relative := strings.TrimPrefix(filepath.ToSlash(path), "../")
			prefix := "github.com/Cosmic-Developers-Union/assistant-sandbox/"
			if strings.HasPrefix(relative, "vm/") && strings.HasPrefix(name, prefix) && !strings.HasPrefix(name, prefix+"vm") {
				t.Errorf("内核反向依赖外层: %s import %s", path, name)
			}
			if strings.HasPrefix(relative, "userland/") && (strings.HasPrefix(name, prefix+"platform/") || strings.HasPrefix(name, prefix+"guest")) {
				t.Errorf("用户程序依赖启动或平台层: %s import %s", path, name)
			}
			if forbidden[name] && !boundary[relative][name] {
				t.Errorf("客体核心存在资源旁路: %s import %s", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
