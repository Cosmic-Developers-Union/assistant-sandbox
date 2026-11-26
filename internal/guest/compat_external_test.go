package guest_test

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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
	machine, connection := compatRootfsGuest(t, image)
	script := `set -eu
test -x /sbin/init
test -r /etc/inittab
test -x /bin/sh
command -v apk >/dev/null
test -r /proc/self/status
test -r /sys/kernel
test -r /proc/self/cgroup
test -r /sys/fs/cgroup/cgroup.controllers
test -L /proc/self/ns/mnt
test -e /proc/self/ns/pid
apk --version >/dev/null
printf 's4-ok\n'`
	output, diagnostic, code := runGuestProgram(t, connection,
		"/bin/sh", []string{"-c", script},
		map[string]string{"HOME": "/work", "PATH": "/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8"},
		300*time.Second)
	if code != 0 || output != "s4-ok\n" {
		t.Fatalf("S4 开发镜像 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}

	repo := os.Getenv("COMPAT_S4_REPO")
	if repo == "" {
		return
	}
	copyGuestTree(t, machine, repo, "/work/s4repo/riscv64")
	pkg := os.Getenv("COMPAT_S4_PACKAGE")
	if pkg == "" {
		pkg = "git-init-template"
	}
	script = `/sbin/apk add --simulate --allow-untrusted --logfile=no \
		--repository /work/s4repo --repositories-file /dev/null \
		--no-scripts --force-non-repository ` + pkg
	output, diagnostic, code = runGuestProgram(t, connection,
		"/bin/sh", []string{"-c", script},
		map[string]string{"HOME": "/work", "PATH": "/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		300*time.Second)
	if code != 0 || !strings.Contains(output, "OK:") {
		t.Fatalf("S4 包仓库 exit=%d stdout=%q stderr=%q", code, output, diagnostic)
	}
}

func copyGuestTree(t *testing.T, machine *instance.VM, source, destination string) {
	t.Helper()
	system := machine.Filesystem()
	err := filepath.WalkDir(source, func(hostPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, hostPath)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := path.Join(destination, filepath.ToSlash(relative))
		if entry.IsDir() {
			if err := system.Mkdir(t.Context(), target, 0o755); err != nil {
				if info, statErr := system.Stat(t.Context(), target); statErr == nil && info.IsDir() {
					return nil
				}
				return err
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(hostPath)
			if err != nil {
				return err
			}
			return system.Symlink(t.Context(), filepath.ToSlash(link), target)
		}
		data, err := os.ReadFile(hostPath) //nolint:gosec // 测试目录由环境变量显式提供
		if err != nil {
			return err
		}
		writeGuestFile(t, machine, target, data, info.Mode().Perm())
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
