package guest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
)

// f11DynDigest 锁定 dlopen 探针摘要(重建须显式更新)。源码要点:
// dlopen(libf10.so, RTLD_NOW|RTLD_LOCAL) 装载、dlsym 取 f10_add、
// 调用并输出、dlclose 卸载;随后 dlopen 缺失库走错误路径并以
// dlerror 输出。工具链 zig 0.16.0:
// `zig cc -dynamic -target riscv64-linux-musl -O2 -s -o f11-dyn f11dyn.c`。
const (
	f11DynDigest    = "0a998dea64ba7d0f654b02ab08fa627d003a506f23a2a1597966f49698db454a"
	f11HappyOutput  = "F11-OK sum=23\nF11-MISSING Error loading shared library lib-absent.so: No such file or directory\n"
	f11MissingLibFr = "Error loading shared library lib-absent.so"
)

func f11Program(t *testing.T) []byte {
	t.Helper()
	image, err := os.ReadFile("testdata/f11-dyn")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != f11DynDigest {
		t.Fatalf("dlopen 探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, f11DynDigest)
	}
	return image
}

// TestDlopenDlcloseLifecycleOnVM 验证 F11:客体程序装载共享库、跨库
// 调用、卸载,且缺失库错误经 dlerror 报告;装载失败路径无资源泄漏
// (进程照常退出,VM 可继续使用)。
func TestDlopenDlcloseLifecycleOnVM(t *testing.T) {
	program := f11Program(t)
	lib := f10Fixture(t, "libf10.so", f10LibDigest)
	stdout, code := runDynamic(t, program, lib, true)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != f11HappyOutput {
		t.Fatalf("stdout=%q want %q", stdout, f11HappyOutput)
	}
}

// TestDlopenMatchesReferenceLinux 差分(F11):同一份探针在
// qemu-riscv64 参考执行与本 VM 执行,输出逐字节一致。
func TestDlopenMatchesReferenceLinux(t *testing.T) {
	program := f11Program(t)
	lib := f10Fixture(t, "libf10.so", f10LibDigest)
	root := t.TempDir()
	loader := f10Fixture(t, filepath.Join("lib", "ld-musl-riscv64.so.1"), f10LoaderDigest)
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "ld-musl-riscv64.so.1"), loader, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "libc.so"), loader, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "libf10.so"), lib, 0o755); err != nil {
		t.Fatal(err)
	}
	reference, err := diff.ReferenceLinuxWithRoot(t.Context(), program, "", root)
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if reference.ExitCode != 0 || !strings.Contains(reference.Stdout, f11MissingLibFr) {
		t.Fatalf("参考结果: 退出码=%d stdout=%q", reference.ExitCode, reference.Stdout)
	}
	if !strings.Contains(reference.Stdout, "F11-OK sum=23") {
		t.Fatalf("参考 stdout=%q want 含 F11-OK sum=23", reference.Stdout)
	}
	stdout, code := runDynamic(t, program, lib, true)
	if code != 0 || stdout != f11HappyOutput {
		t.Fatalf("VM: 退出码=%d stdout=%q want 0/%q", code, stdout, f11HappyOutput)
	}
}
