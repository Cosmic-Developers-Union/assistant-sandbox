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

// k10ShmDigest 锁定共享同步探针摘要(重建须显式更新)。源码要点:
// 父进程建立 MAP_SHARED 文件映射并 fork;子进程在共享 futex 上等待,
// 父进程置值唤醒——跨进程交接;两进程在同数值私有地址上互不唤醒
// (子 WAKE、父 ETIMEDOUT)。工具链 zig 0.16.0:
// `zig cc -dynamic -target riscv64-linux-musl -O2 -s -o k10-shm k10shm.c`。
const (
	k10ShmDigest  = "08259cb56696678ca3a3604e0f06865cd3b323bd5195b4272089cadabc9fe722"
	k10ShmOutput  = "K10-OK shared=77 private=-1\n"
	k10ShmPrivate = -1 // 私有等待的 ETIMEDOUT 原始返回
)

func k10Program(t *testing.T) []byte {
	t.Helper()
	image, err := os.ReadFile("testdata/k10-shm")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != k10ShmDigest {
		t.Fatalf("共享同步探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, k10ShmDigest)
	}
	return image
}

// TestSharedFutexAcrossForkOnVM 验证 K10:fork 父子经共享映射上的
// futex 交接(键按后备标识加盐);私有 futex 不跨地址空间错误唤醒。
func TestSharedFutexAcrossForkOnVM(t *testing.T) {
	program := k10Program(t)
	stdout, code := runDynamic(t, program, nil, false)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != k10ShmOutput {
		t.Fatalf("stdout=%q want %q", stdout, k10ShmOutput)
	}
}

// TestSharedFutexMatchesReferenceLinux 差分(K10):同一份探针在
// qemu-riscv64 参考执行与本 VM 执行,输出逐字节一致。
func TestSharedFutexMatchesReferenceLinux(t *testing.T) {
	program := k10Program(t)
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
	reference, err := diff.ReferenceLinuxWithRoot(t.Context(), program, "", root)
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if reference.ExitCode != 0 || !strings.Contains(reference.Stdout, "K10-OK") {
		t.Fatalf("参考结果: 退出码=%d stdout=%q", reference.ExitCode, reference.Stdout)
	}
	stdout, code := runDynamic(t, program, nil, false)
	if code != 0 || stdout != k10ShmOutput {
		t.Fatalf("VM: 退出码=%d stdout=%q want 0/%q", code, stdout, k10ShmOutput)
	}
}
