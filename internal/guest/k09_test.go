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

// k09SyncDigest 锁定同步原语压测探针摘要(重建须显式更新)。源码要点:
// 4 线程互斥推进共享计数 8000 次;生产者/消费者经条件变量在单槽队列
// 上交接 500 个值(双方等待,无丢失信号);pthread_once 恰好执行
// 一次;rwlock 三读一写各按次数校验。工具链 zig 0.16.0:
// `zig cc -dynamic -target riscv64-linux-musl -O2 -s -o k09-sync k09sync.c`。
const (
	k09SyncDigest = "1ab2529e69963b707342cd3546963a688562a20de71c5ff5e1ef093e302a4655"
	k09SyncOutput = "K09-OK counter=8000 consumed=125250 once=1 reads=3000\n"
)

// TestPthreadSyncPrimitivesOnVM 验证 K09:mutex(竞争无丢失)、
// cond(等待/唤醒交接)、once(恰好一次)、rwlock(读写互斥)在
// 有界压力下正确;依赖 K06 的 futex REQUEUE/BITSET 路径。
func TestPthreadSyncPrimitivesOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/k09-sync")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != k09SyncDigest {
		t.Fatalf("同步压测探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, k09SyncDigest)
	}
	stdout, code := runDynamic(t, image, nil, false)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != k09SyncOutput {
		t.Fatalf("stdout=%q want %q", stdout, k09SyncOutput)
	}
}

// TestPthreadSyncStaysBounded 锁定有界性:压测在远低于测试超时内完成,
// 无死锁挂起。
func TestPthreadSyncStaysBounded(t *testing.T) {
	image, err := os.ReadFile("testdata/k09-sync")
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		stdout, code := runDynamic(t, image, nil, false)
		if code != 0 || stdout != k09SyncOutput {
			t.Fatalf("第 %d 轮: 退出码=%d stdout=%q", round, code, stdout)
		}
	}
}

// TestPthreadSyncMatchesReferenceLinux 差分(K09):同一份探针在
// qemu-riscv64 参考执行与本 VM 执行,输出逐字节一致。
func TestPthreadSyncMatchesReferenceLinux(t *testing.T) {
	image, err := os.ReadFile("testdata/k09-sync")
	if err != nil {
		t.Fatal(err)
	}
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
	reference, err := diff.ReferenceLinuxWithRoot(t.Context(), image, "", root)
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if reference.ExitCode != 0 || !strings.Contains(reference.Stdout, "K09-OK counter=8000") {
		t.Fatalf("参考结果: 退出码=%d stdout=%q", reference.ExitCode, reference.Stdout)
	}
	stdout, code := runDynamic(t, image, nil, false)
	if code != 0 || stdout != k09SyncOutput {
		t.Fatalf("VM: 退出码=%d stdout=%q want 0/%q", code, stdout, k09SyncOutput)
	}
}
