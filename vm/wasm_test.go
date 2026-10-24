package vm

import (
	"bytes"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

func TestKernelActuallyRunsInsideWASM(t *testing.T) {
	file := filepath.Join(t.TempDir(), "vm.wasm")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", file, "./testdata/guest.go")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译客体: %v\n%s", err, out)
	}
	binary, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	runtime := wazero.NewRuntime(t.Context())
	defer func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if _, err := wasi_snapshot_preview1.Instantiate(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if _, err := runtime.InstantiateWithConfig(t.Context(), binary, wazero.NewModuleConfig().WithStdout(&stdout).WithStderr(&stderr)); err != nil {
		t.Fatal(err, stderr.String())
	}
	if stdout.String() != "vm: pid=2 parent=1 exit=7 net=ENETUNREACH ipc=ENOSYS\n" || stderr.Len() != 0 {
		t.Fatal(stdout.String(), stderr.String())
	}
}
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

		"filesystem/memfs/file.go": {"time": true},
		"guest/cmd/main.go":        {"os": true},

		"filesystem/persistent/store.go": {"os": true},

		"filesystem/procfs/procfs.go": {"time": true},

		"filesystem/devfs/devfs.go": {"time": true},

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
			if d.Name() == "testdata" || d.Name() == "build" || d.Name() == "generate" || d.Name() == "internal" || d.Name() == "cmd" || d.Name() == "test" || d.Name() == ".git" {
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
			if forbidden[name] && !boundary[strings.TrimPrefix(filepath.ToSlash(path), "../")][name] {
				t.Errorf("客体核心存在资源旁路: %s import %s", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
