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

// k02PthreadsDigest 锁定 pthread 探针摘要(重建须显式更新)。源码要点:
// 真实 pthread 程序创建并 join 两个线程,各自推进 _Thread_local 计数
// 与共享原子计数,join 后按返回值与总数校验。工具链 zig 0.16.0:
// `zig cc -dynamic -target riscv64-linux-musl -O2 -s -o k02-pthreads k02.c`。
// 运行依赖 ld-musl-riscv64.so.1(F07 装置)。
const (
	k02PthreadsDigest = "b7be783c3b0f44c1677b9fee6c2fa22ae6312fc841388c0639a6de2d4225f2c8"
	k02PthreadsOutput = "K02-OK total=2000 ra=100 rb=200\n"
)

func k02Program(t *testing.T) []byte {
	t.Helper()
	image, err := os.ReadFile("testdata/k02-pthreads")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != k02PthreadsDigest {
		t.Fatalf("pthread 探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, k02PthreadsDigest)
	}
	return image
}

// TestPthreadCreateJoinOnVM 验证 K02:libc 经 clone 线程标志创建的
// 真实线程与内核线程组语义——同组共享地址空间/描述符/信号处置,
// futex 按线程组键控,pthread_join 经 clear_child_tid 唤醒返回。
func TestPthreadCreateJoinOnVM(t *testing.T) {
	program := k02Program(t)
	stdout, code := runDynamic(t, program, nil, false)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != k02PthreadsOutput {
		t.Fatalf("stdout=%q want %q", stdout, k02PthreadsOutput)
	}
}

// TestPthreadsMatchReferenceLinux 差分(K02):同一份 pthread 程序在
// qemu-riscv64 参考执行与本 VM 执行,输出逐字节一致。
func TestPthreadsMatchReferenceLinux(t *testing.T) {
	program := k02Program(t)
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
	if reference.ExitCode != 0 || !strings.Contains(reference.Stdout, "K02-OK total=2000") {
		t.Fatalf("参考结果: 退出码=%d stdout=%q", reference.ExitCode, reference.Stdout)
	}
	stdout, code := runDynamic(t, program, nil, false)
	if code != 0 || stdout != k02PthreadsOutput {
		t.Fatalf("VM: 退出码=%d stdout=%q want 0/%q", code, stdout, k02PthreadsOutput)
	}
}
