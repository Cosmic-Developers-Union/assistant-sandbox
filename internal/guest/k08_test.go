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

// k08SigDigest 锁定线程信号探针摘要(重建须显式更新)。源码要点:
// pthread_kill(SIGUSR1)×10 到登记 SA_SIGINFO 处理器的目标线程,处理器
// 以 gettid 判定落在目标线程(共享计数 target=10/other=0);随后
// pthread_kill(SIGTERM):默认终止信号终结整组,进程以 128+15 退出。
// 工具链 zig 0.16.0:
// `zig cc -dynamic -target riscv64-linux-musl -O2 -s -o k08-sig k08sig.c`。
const (
	k08SigDigest = "e3458e05c5224f2719d72fd839473746b6c6259a4db2661f0f52a88b361ce68e"
	k08SigOutput = "K08-OK handled=10 other=0\n"
	k08GroupExit = 143 // 128 + SIGTERM
)

// TestThreadDirectedSignalsOnVM 验证 K08:处理器信号只落在目标线程
// (无关线程与主线程不受影响);默认终止信号终结整组,退出状态为
// 信号终止(128+15)。
func TestThreadDirectedSignalsOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/k08-sig")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != k08SigDigest {
		t.Fatalf("线程信号探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, k08SigDigest)
	}
	stdout, code := runDynamic(t, image, nil, false)
	if code != k08GroupExit {
		t.Fatalf("退出码=%d want %d(stdout=%q)", code, k08GroupExit, stdout)
	}
	if !strings.Contains(stdout, k08SigOutput) {
		t.Fatalf("stdout=%q want 含 %q", stdout, k08SigOutput)
	}
}

// TestThreadSignalsMatchReferenceLinux 差分(K08):同一份探针在
// qemu-riscv64 参考执行与本 VM 执行,输出与退出码一致。
func TestThreadSignalsMatchReferenceLinux(t *testing.T) {
	image, err := os.ReadFile("testdata/k08-sig")
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
	if reference.ExitCode != k08GroupExit || !strings.Contains(reference.Stdout, "K08-OK") {
		t.Fatalf("参考结果: 退出码=%d stdout=%q", reference.ExitCode, reference.Stdout)
	}
	stdout, code := runDynamic(t, image, nil, false)
	if code != k08GroupExit || !strings.Contains(stdout, "K08-OK") {
		t.Fatalf("VM: 退出码=%d stdout=%q", code, stdout)
	}
}
