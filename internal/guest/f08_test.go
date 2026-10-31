package guest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
)

// f08TlsDigest 锁定 TLS 探针产物摘要(重建须显式更新)。源码要点:
// _Thread_local 整数与字符经函数读写、errno 强制置零后由失败的
// openat 写为 ENOENT、strerror 读取 errno 文案。工具链 zig 0.16.0:
// `zig cc -target riscv64-linux-musl -static -O2 -s -o f08-tls f08tls.c`。
// riscv64 的线程指针即 x4:musl 静态启动以内联指令设置 tp(无系统
// 调用),errno/TLS 经 tp 相对寻址,CPU 对 x4 透传。
const (
	f08TlsDigest = "fc4dd529ec3e5f3f0673966a3b5e8fecfdedab4f689634ba30719a36ca405ceb"
	f08TlsOutput = "F08-TLS counter=12 tag=B open=-1 errno=2 text=No such file or directory\n"
)

// TestMainThreadTlsAndErrnoOnVM 验证 F08:静态 musl 程序的主线程
// TLS(_Thread_local 读写跨函数保留)与 errno(置零后由内核应答写
// 回、strerror 可读)在 VM 内正确工作。
func TestMainThreadTlsAndErrnoOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/f08-tls")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != f08TlsDigest {
		t.Fatalf("TLS 探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, f08TlsDigest)
	}
	stdout, code := runSessionProbeOnKernel(t, image)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != f08TlsOutput {
		t.Fatalf("stdout=%q want %q", stdout, f08TlsOutput)
	}
}

// TestMainThreadTlsMatchesReferenceLinux 差分(F08):同一份 TLS 探针
// 在 qemu-riscv64 参考执行与本 VM 执行,输出逐字节一致。
func TestMainThreadTlsMatchesReferenceLinux(t *testing.T) {
	image, err := os.ReadFile("testdata/f08-tls")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if reference.ExitCode != 0 || reference.Stdout != f08TlsOutput {
		t.Fatalf("参考结果: 退出码=%d stdout=%q want 0/%q", reference.ExitCode, reference.Stdout, f08TlsOutput)
	}
	stdout, code := runSessionProbeOnKernel(t, image)
	if code != 0 || stdout != f08TlsOutput {
		t.Fatalf("VM: 退出码=%d stdout=%q want 0/%q", code, stdout, f08TlsOutput)
	}
}
