package guest_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestCompatibilityExternalLTP 运行外部 LTP rootfs。未提供镜像时跳过，
// 避免把“未运行”伪装成通过。
//
//	COMPAT_LTP_TARBALL=/path/to/ltp-rootfs.tar.gz
//	COMPAT_LTP_ARGS="-g ltp-syscalls"
func TestCompatibilityExternalLTP(t *testing.T) {
	tarball := os.Getenv("COMPAT_LTP_TARBALL")
	if tarball == "" {
		t.Skip("设置 COMPAT_LTP_TARBALL 后运行外部 LTP 兼容门禁")
	}
	_, connection := compatRootfsGuest(t, tarball)
	args := strings.Fields(os.Getenv("COMPAT_LTP_ARGS"))
	if len(args) == 0 {
		args = []string{"-g", "ltp-syscalls"}
	}
	env := map[string]string{
		"HOME": "/work", "PATH": "/usr/local/bin:/usr/bin:/bin",
		"LANG": "C.UTF-8", "LTPROOT": "/opt/ltp",
	}
	output, diagnostic, code := runGuestProgram(t, connection,
		"/opt/ltp/runltp", args, env, 2*time.Hour)
	if code != 0 {
		t.Fatalf("LTP exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
}

// TestCompatibilityS4DevelopmentImageExternal 验证 S4 开发镜像合同：
// 发行版 init、包管理器、动态工具链和 /proc、/sys。未提供镜像时跳过。
//
//	COMPAT_S4_IMAGE=/path/to/development-rootfs.tar.gz
func TestCompatibilityS4DevelopmentImageExternal(t *testing.T) {
	image := os.Getenv("COMPAT_S4_IMAGE")
	if image == "" {
		t.Skip("设置 COMPAT_S4_IMAGE 后运行 S4 开发镜像门禁")
	}
	_, connection := compatRootfsGuest(t, image)
	script := `set -eu
test -x /sbin/init
command -v apk >/dev/null
command -v git >/dev/null
command -v cc >/dev/null
command -v python >/dev/null
command -v python3 >/dev/null
command -v node >/dev/null
test -r /proc/self/status
test -r /sys/kernel
apk --version >/dev/null
printf 's4-ok\n'`
	output, diagnostic, code := runGuestProgram(t, connection,
		"/bin/sh", []string{"-c", script},
		map[string]string{"HOME": "/work", "PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8"},
		300*time.Second)
	if code != 0 || output != "s4-ok\n" {
		t.Fatalf("S4 开发镜像 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
}

func compatRootfsGuest(t *testing.T, archivePath string) (*instance.VM, *instance.Connection) {
	t.Helper()
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := instance.New(t.Context(), instance.Options{
		Deterministic: false,
		Seed:          1,
		Session:       resources.Limits{Memory: 8 << 30, Work: 1 << 40},
		Process:       resources.Limits{Memory: 4 << 30, Work: 1 << 40},
		TraceLimit:    8192,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	extractGuestTar(t, machine, archive)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return machine, connection
}
