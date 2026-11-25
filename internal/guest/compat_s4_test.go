package guest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/guest/devroot"
	nativeshell "github.com/Cosmic-Developers-Union/assistant-sandbox/guest/native"
)

// TestCompatibilityS4DevelopmentWorkflow 验证内置 S4 开发合同:
// 发行版入口、包工具、Git、编译链、Python/Node 以及 /proc、/sys。
func TestCompatibilityS4DevelopmentWorkflow(t *testing.T) {
	machine, connection := compatDevelopmentGuest(t)
	if err := nativeshell.Install(t.Context(), machine.Filesystem()); err != nil {
		t.Fatal(err)
	}
	if err := devroot.Install(t.Context(), machine.Filesystem()); err != nil {
		t.Fatal(err)
	}
	script := `set -eu
test -x /sbin/init
test -r /proc/self/status
test -r /sys/kernel/osrelease
test -r /sys/devices/system/cpu/online
test -r /sys/class/net/lo/address
command -v apk >/dev/null
command -v git >/dev/null
command -v cc >/dev/null
command -v python >/dev/null
command -v python3 >/dev/null
command -v node >/dev/null
apk --version >/dev/null
git --version >/dev/null
cc --version >/dev/null
node --version >/dev/null
python --version >/dev/null
printf 's4-ok\n'`
	output, diagnostic, code := runGuestProgram(t, connection,
		"/bin/bash", []string{"--noprofile", "--norc", "-c", script},
		map[string]string{
			"HOME": "/work", "PATH": "/usr/local/bin:/usr/bin:/bin",
			"LANG": "C.UTF-8", "PYTHONHOME": "/usr",
			"NODE_ICU_DATA": "/usr/share/icu/76.1",
		}, 300*time.Second)
	if code != 0 || output != "s4-ok\n" {
		t.Fatalf("S4 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
}

// TestSandboxInSandboxDevelopment 先构建 riscv64 CLI,再把它作为客体程序
// 放入外层 VM,由内层 VM 运行 BusyBox sh。该测试默认跳过,使用
// SANDBOX_NESTED_TEST=1 或 make sandbox-in-sandbox 显式运行。
func TestSandboxInSandboxDevelopment(t *testing.T) {
	if os.Getenv("SANDBOX_NESTED_TEST") != "1" {
		t.Skip("设置 SANDBOX_NESTED_TEST=1 后运行嵌套 sandbox 验证")
	}
	if testing.Short() {
		t.Skip("快速模式跳过嵌套 sandbox")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "assistant-sandbox")
	build := exec.CommandContext(t.Context(), "go", "build", "-trimpath",
		"-ldflags=-s -w", "-o", binary, "./cmd/assistant-sandbox")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=riscv64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 riscv64 CLI: %v\n%s", err, output)
	}
	image, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	machine, connection := compatDevelopmentGuest(t)
	if err := nativeshell.Install(t.Context(), machine.Filesystem()); err != nil {
		t.Fatal(err)
	}
	writeGuestFile(t, machine, "/work/assistant-sandbox", image, 0o755)
	script := `/work/assistant-sandbox --tools=minimal --shell sh --cpus 1 --memory 300m -c 'printf sandbox-in-sandbox-ok'`
	output, diagnostic, code := runGuestProgram(t, connection,
		"/bin/sh", []string{"-c", script},
		map[string]string{
			"HOME": "/work", "PATH": "/usr/local/bin:/usr/bin:/bin",
			"LANG": "C.UTF-8", "TMPDIR": "/tmp",
		}, 180*time.Second)
	if code != 0 || strings.TrimSpace(output) != "sandbox-in-sandbox-ok" {
		t.Fatalf("嵌套 sandbox exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
}
