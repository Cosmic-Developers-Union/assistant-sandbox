package guest_test

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/guest/devroot"
	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestGoGuestRuntime 验证 Go 交叉编译出的静态 RISC-V ELF 可在 VM 中
// 启动、使用 RDTIME/时钟相关运行时路径并正常退出。
func TestGoGuestRuntime(t *testing.T) {
	machine, connection := languagePerformanceGuest(t, false)
	source := `package main

import "os"

func main() {
	os.Stdout.WriteString("go-ok\n")
}
`
	binary, err := os.ReadFile(buildGoProgram(t, source, "linux", "riscv64"))
	if err != nil {
		t.Fatal(err)
	}
	writeGuestFile(t, machine, "/work/go-runtime", binary, 0o755)
	output, diagnostic, code := runGuestProgram(t, connection,
		"/work/go-runtime", nil,
		map[string]string{"HOME": "/work", "PATH": "/usr/bin:/bin"},
		time.Minute)
	if code != 0 || output != "go-ok\n" {
		t.Fatalf("Go runtime exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
}

const pythonPerformanceScript = `x = 0
for i in range(1000000):
    x += i
print(x)
`

const nodePerformanceScript = `let x = 0;
for (let i = 0; i < 100000; i++) x += i;
process.stdout.write(String(x) + "\n");
`

const goPerformanceSource = `package main

import "fmt"

func main() {
	var x uint64
	for i := uint64(0); i < 10000000; i++ {
		x += i
	}
	fmt.Println(x)
}
`

// 以下性能测试必须分别在独立 go test 进程中运行，避免 Linux 上全局
// JIT 代码缓存被前一运行时填满后污染后续运行时。
func requirePerformanceRun(t *testing.T) {
	t.Helper()
	if os.Getenv("SANDBOX_PERF") == "" {
		t.Skip("设置 SANDBOX_PERF=1 后运行语言性能基线")
	}
}

func TestPerformancePython(t *testing.T) {
	requirePerformanceRun(t)
	_, connection := languagePerformanceGuest(t, false)
	started := time.Now()
	output, diagnostic, code := runGuestProgram(t, connection,
		"/usr/bin/python3.12", []string{"-S", "-c", pythonPerformanceScript},
		map[string]string{
			"HOME": "/work", "PATH": "/usr/bin:/bin",
			"LANG": "C.UTF-8", "PYTHONHOME": "/usr",
		}, 5*time.Minute)
	elapsed := time.Since(started)
	if code != 0 {
		t.Fatalf("Python exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	logLanguageTiming(t, "python", elapsed,
		hostCommandDuration(t, "python3", "-S", "-c", pythonPerformanceScript))
}

func TestPerformanceNodeJitless(t *testing.T) {
	requirePerformanceRun(t)
	_, connection := languagePerformanceGuest(t, true)
	runNodePerformance(t, connection, "node-jitless",
		[]string{"--jitless", "-e", nodePerformanceScript})
}

func TestPerformanceNodeStandard(t *testing.T) {
	requirePerformanceRun(t)
	_, connection := languagePerformanceGuest(t, true)
	runNodePerformance(t, connection, "node-standard",
		[]string{"-e", nodePerformanceScript})
}

func runNodePerformance(t *testing.T, connection *instance.Connection, name string, args []string) {
	t.Helper()
	started := time.Now()
	output, diagnostic, code := runGuestProgram(t, connection,
		"/usr/bin/node-bin", args,
		map[string]string{"HOME": "/work", "PATH": "/usr/bin:/bin"},
		5*time.Minute)
	elapsed := time.Since(started)
	if code != 0 {
		t.Fatalf("Node exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	logLanguageTiming(t, name, elapsed,
		hostCommandDuration(t, "node", "-e", nodePerformanceScript))
}

func TestPerformanceGo(t *testing.T) {
	requirePerformanceRun(t)
	machine, connection := languagePerformanceGuest(t, false)
	assertAndWriteGoProgram(t, machine, "/work/go-perf", goPerformanceSource)
	started := time.Now()
	output, diagnostic, code := runGuestProgram(t, connection,
		"/work/go-perf", nil,
		map[string]string{"HOME": "/work", "PATH": "/usr/bin:/bin"},
		5*time.Minute)
	elapsed := time.Since(started)
	if code != 0 {
		t.Fatalf("Go exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
	hostBinary := buildGoProgram(t, goPerformanceSource, "linux", "amd64")
	logLanguageTiming(t, "go", elapsed, hostCommandDuration(t, hostBinary))
}

func assertAndWriteGoProgram(t *testing.T, machine *instance.VM, name, source string) {
	t.Helper()
	guestBinary, err := os.ReadFile(buildGoProgram(t, source, "linux", "riscv64"))
	if err != nil {
		t.Fatal(err)
	}
	image, err := elf.NewFile(bytes.NewReader(guestBinary))
	if err != nil {
		t.Fatalf("Go 客体产物不是 ELF: %v", err)
	}
	defer func() { _ = image.Close() }()
	if image.Machine != elf.EM_RISCV || image.Class != elf.ELFCLASS64 {
		t.Fatalf("Go 客体产物架构=%s/%s want RISC-V/ELF64", image.Machine, image.Class)
	}
	writeGuestFile(t, machine, name, guestBinary, 0o755)
}

func languagePerformanceGuest(t *testing.T, allowGuestRWX bool) (*instance.VM, *instance.Connection) {
	t.Helper()
	machine, err := instance.New(t.Context(), instance.Options{
		Deterministic: false,
		Seed:          1,
		Session:       resources.Limits{Memory: 4 << 30, Work: 1 << 38},
		Process:       resources.Limits{Memory: 2 << 30, Work: 1 << 38},
		AllowGuestRWX: allowGuestRWX,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	if err := nativeshell.Install(t.Context(), machine.Filesystem()); err != nil {
		t.Fatal(err)
	}
	if err := devroot.Install(t.Context(), machine.Filesystem()); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return machine, connection
}

func logLanguageTiming(t *testing.T, name string, guest, host time.Duration) {
	t.Helper()
	if host <= 0 {
		t.Logf("%s guest=%s host=unavailable", name, guest)
		return
	}
	t.Logf("%s guest=%s host=%s gap=%.1fx", name, guest, host,
		float64(guest)/float64(host))
}

func hostCommandDuration(t *testing.T, name string, args ...string) time.Duration {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		return 0
	}
	started := time.Now()
	command := exec.Command(path, args...)
	command.Stdout = &bytes.Buffer{}
	command.Stderr = &bytes.Buffer{}
	if err := command.Run(); err != nil {
		t.Logf("宿主基线 %s 失败: %v", name, err)
		return 0
	}
	return time.Since(started)
}

func buildGoProgram(t *testing.T, source, goos, goarch string) string {
	t.Helper()
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "main.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, fmt.Sprintf("program-%s-%s", goos, goarch))
	command := exec.CommandContext(t.Context(), "go", "build",
		"-trimpath", "-ldflags=-s -w", "-o", output, sourcePath)
	command.Env = append(os.Environ(),
		"GOOS="+goos,
		"GOARCH="+goarch,
		"CGO_ENABLED=0",
		"GOTOOLCHAIN=local",
	)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("构建 %s/%s Go 程序: %v\n%s", goos, goarch, err, result)
	}
	return output
}

// TestRuntimeHostEnvelope compares complete process executions in an installed
// VM. Run each language in a fresh test process to isolate the global JIT cache.
func TestRuntimeHostEnvelope(t *testing.T) {
	requirePerformanceRun(t)
	for _, runtime := range []struct {
		host, guest, eval, startup, cpu, io string
		wantCPU                             string
	}{
		{"python3", "/usr/bin/python3.12", "-c", "print(42)", pythonPerformanceScript,
			`from pathlib import Path
path = Path("perf.bin")
data = b"x" * (1 << 20)
path.write_bytes(data)
assert path.read_bytes() == data
print(len(data))`, "499999500000\n"},
		{"node", "/usr/bin/node-bin", "-e", "console.log(42)", nodePerformanceScript,
			`const fs = require("node:fs");
const data = Buffer.alloc(1 << 20, 120);
fs.writeFileSync("perf.bin", data);
if (!fs.readFileSync("perf.bin").equals(data)) throw Error("data");
console.log(data.length);`, "4999950000\n"},
	} {
		t.Run(runtime.host, func(t *testing.T) {
			_, connection := languagePerformanceGuest(t, runtime.host == "node")
			for _, scene := range []struct{ name, script, want string }{
				{"startup", runtime.startup, "42\n"},
				{"cpu", runtime.cpu, runtime.wantCPU},
				{"file", runtime.io, "1048576\n"},
			} {
				t.Run(scene.name, func(t *testing.T) {
					args := []string{runtime.eval, scene.script}
					cmd := exec.CommandContext(t.Context(), runtime.host, args...)
					cmd.Dir = t.TempDir()
					start := time.Now()
					out, err := cmd.Output()
					host := time.Since(start)
					if err != nil || string(out) != scene.want {
						t.Fatalf("host output %q, error %v", out, err)
					}
					start = time.Now()
					output, diagnostic, code := runGuestProgram(t, connection, runtime.guest, args,
						map[string]string{"HOME": "/work", "PATH": "/usr/bin:/bin"}, 5*time.Minute)
					guest := time.Since(start)
					if code != 0 || output != scene.want {
						t.Fatalf("guest exit=%d stdout=%q stderr=%q", code, output, diagnostic)
					}
					logLanguageTiming(t, scene.name, guest, host)
					if guest > 3*host {
						t.Errorf("guest %s exceeds 3 × host %s", guest, host)
					}
				})
			}
		})
	}
}
