package guest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
)

// futexExpectations 是八个结果字节的 Linux 期望序列(低字节,-errno
// 取补码低 8 位):值不匹配 -EAGAIN(0xF5)、超时 -ETIMEDOUT(0x92)、
// 两次 WAKE 无等待者 0 0、未对齐/未知操作/实时钟/负超时 -EINVAL(0xEA)。
var futexExpectations = []byte{0xF5, 0x92, 0, 0, 0xEA, 0xEA, 0xEA, 0xEA}

// TestFutexProbeIsDeterministic 锁定 futex 探针产物摘要。
func TestFutexProbeIsDeterministic(t *testing.T) {
	first := guest.FutexProbe()
	second := guest.FutexProbe()
	if !bytes.Equal(first, second) {
		t.Fatal("futex 探针两次构建字节不一致")
	}
	if got := guest.ImageDigestOf(first); got != guest.FutexProbeImageSHA256 {
		t.Fatalf("futex 探针摘要=%s want %s", got, guest.FutexProbeImageSHA256)
	}
}

// TestFutexProbeSemanticsMatchLinux 验证 K05:真实 RISC-V ELF 在真实
// 内核上完成 WAIT 值校验(EAGAIN)、匹配阻塞超时(ETIMEDOUT)、
// WAKE 计数与 EINVAL 参数校验族。
func TestFutexProbeSemanticsMatchLinux(t *testing.T) {
	kernel, stdout, code := runGuestOnKernel(t, guest.FutexProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), futexExpectations) {
		t.Fatalf("结果序列=% x want % x", []byte(stdout), futexExpectations)
	}
	if code != 0 {
		t.Fatalf("退出码=%d want 0", code)
	}
}

// TestFutexProbeMatchesReferenceLinux 同一字节码经 qemu-riscv64 参考
// 执行与本 VM 对拍。参考执行器不可用时显式跳过,由 CI differential
// 任务覆盖。
func TestFutexProbeMatchesReferenceLinux(t *testing.T) {
	image := guest.FutexProbe()
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if !bytes.Equal([]byte(reference.Stdout), futexExpectations) {
		t.Fatalf("参考 Linux 输出=% x want % x", []byte(reference.Stdout), futexExpectations)
	}
	if reference.ExitCode != 0 {
		t.Fatalf("参考退出码=%d want 0", reference.ExitCode)
	}
	kernel, stdout, code := runGuestOnKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), futexExpectations) {
		t.Fatalf("VM 输出=% x want % x", []byte(stdout), futexExpectations)
	}
	if code != 0 {
		t.Fatalf("VM 退出码=%d want 0", code)
	}
}
