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

// k04TlsDigest 锁定 TLS 隔离探针摘要(重建须显式更新)。源码要点:
// 两个线程各自写自己的 _Thread_local 槽位与本线程 errno,万次迭代
// 中反复校验未被对方覆盖(写入经 write(-1) 触发 EBADF 写回本线程
// errno)。工具链 zig 0.16.0:
// `zig cc -dynamic -target riscv64-linux-musl -O2 -s -o k04-tls k04tls.c`。
const (
	k04TlsDigest = "d4661767b3b1c303d1a210e3fdddd465ec09141bc42fcd60c9e548b37421b26f"
	k04TlsOutput = "K04-TLS ra=1000001 rb=2000002\n"
)

// TestPerThreadTlsIsolationOnVM 验证 K04:两个真实线程的 thread-local
// 存储与 errno 在并发读写下互不覆盖(tp=x4 由 clone(CLONE_SETTLS)
// 按线程装载,内核与执行器不共享 TLS 页)。
func TestPerThreadTlsIsolationOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/k04-tls")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != k04TlsDigest {
		t.Fatalf("TLS 隔离探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, k04TlsDigest)
	}
	stdout, code := runDynamic(t, image, nil, false)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != k04TlsOutput {
		t.Fatalf("stdout=%q want %q", stdout, k04TlsOutput)
	}
}

// TestPerThreadTlsMatchesReferenceLinux 差分(K04):同一份探针在
// qemu-riscv64 参考执行与本 VM 执行,输出逐字节一致。
func TestPerThreadTlsMatchesReferenceLinux(t *testing.T) {
	image, err := os.ReadFile("testdata/k04-tls")
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
	if reference.ExitCode != 0 || !strings.Contains(reference.Stdout, "K04-TLS") {
		t.Fatalf("参考结果: 退出码=%d stdout=%q", reference.ExitCode, reference.Stdout)
	}
	stdout, code := runDynamic(t, image, nil, false)
	if code != 0 || stdout != k04TlsOutput {
		t.Fatalf("VM: 退出码=%d stdout=%q want 0/%q", code, stdout, k04TlsOutput)
	}
}
