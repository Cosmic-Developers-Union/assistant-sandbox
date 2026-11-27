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
test -r /proc/self/cgroup
test -r /sys/kernel/osrelease
test -r /sys/devices/system/cpu/online
test -r /sys/class/net/lo/address
test -r /sys/fs/cgroup/cgroup.controllers
test -L /proc/self/ns/mnt
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
for name in cgroup ipc mnt net pid time user uts; do
  target=$(readlink /proc/self/ns/$name)
  case "$target" in "$name:"\[[0-9]*\]) ;; *) echo "bad ns $name=$target" >&2; exit 1;; esac
done
case "$(readlink /proc/self/ns/pid_for_children)" in pid:\[[0-9]*\]) ;; *) exit 1;; esac
test "$(readlink /proc/self/ns/mnt)" = "$(readlink /proc/1/ns/mnt)"
grep -qx '0::/assistant-sandbox' /proc/self/cgroup
grep -q '^cpu memory pids$' /sys/fs/cgroup/cgroup.controllers
grep -q '^cpu memory pids$' /sys/fs/cgroup/assistant-sandbox/cgroup.controllers
awk -v pid=$$ '$1 == pid { found=1 } END { exit !found }' /sys/fs/cgroup/assistant-sandbox/cgroup.procs
test "$(cat /sys/fs/cgroup/assistant-sandbox/pids.current)" -ge 1
test "$(cat /sys/fs/cgroup/assistant-sandbox/memory.max)" -eq 4294967296
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
	copyGuestTree(t, machine, "testdata/s4repo", "/work/s4repo/riscv64")
	repoScript := `set -eu
cp /work/s4repo/riscv64/*.rsa.pub /etc/apk/keys/
/sbin/apk add --no-scripts --repositories-file /dev/null \
	--repository /work/s4repo git-init-template
test -f /usr/share/git-core/templates/description
grep -qx 'P:git-init-template' /lib/apk/db/installed
/sbin/apk info -e git-init-template >/dev/null
/sbin/apk del --no-scripts --repositories-file /dev/null \
	--repository /work/s4repo git-init-template
test ! -e /usr/share/git-core/templates/description
printf 's4-repo-ok\n'`
	output, diagnostic, code = runGuestProgram(t, connection,
		"/bin/bash", []string{"--noprofile", "--norc", "-c", repoScript},
		map[string]string{
			"HOME": "/work", "PATH": "/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"LANG": "C.UTF-8",
		}, 300*time.Second)
	if code != 0 || !strings.Contains(output, "OK:") || !strings.HasSuffix(output, "s4-repo-ok\n") {
		t.Fatalf("S4 包仓库 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
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
