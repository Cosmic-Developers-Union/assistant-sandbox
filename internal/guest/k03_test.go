package guest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
)

// tidAddressExpectations:两次 set_tid_address 的返回都等于 getpid
// (登记与覆盖语义)。
var tidAddressExpectations = []byte{1, 1}

// TestTidAddressProbeIsDeterministic 锁定 set_tid_address 探针产物摘要。
func TestTidAddressProbeIsDeterministic(t *testing.T) {
	first := guest.TidAddressProbe()
	second := guest.TidAddressProbe()
	if !bytes.Equal(first, second) {
		t.Fatal("set_tid_address 探针两次构建字节不一致")
	}
	if got := guest.ImageDigestOf(first); got != guest.TidAddressProbeImageSHA256 {
		t.Fatalf("set_tid_address 探针摘要=%s want %s", got, guest.TidAddressProbeImageSHA256)
	}
}

// TestTidAddressProbeSemanticsMatchLinux 验证 K03:set_tid_address
// 返回调用方 TID,重复登记覆盖后仍返回 TID。
func TestTidAddressProbeSemanticsMatchLinux(t *testing.T) {
	kernel, stdout, code := runGuestOnKernel(t, guest.TidAddressProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), tidAddressExpectations) {
		t.Fatalf("结果序列=% x want % x", []byte(stdout), tidAddressExpectations)
	}
	if code != 0 {
		t.Fatalf("退出码=%d want 0", code)
	}
}

// TestTidAddressProbeMatchesReferenceLinux 同一字节码经 qemu-riscv64
// 参考执行与本 VM 对拍。参考执行器不可用时显式跳过,由 CI
// differential 任务覆盖。
func TestTidAddressProbeMatchesReferenceLinux(t *testing.T) {
	image := guest.TidAddressProbe()
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if !bytes.Equal([]byte(reference.Stdout), tidAddressExpectations) {
		t.Fatalf("参考 Linux 输出=% x want % x", []byte(reference.Stdout), tidAddressExpectations)
	}
	if reference.ExitCode != 0 {
		t.Fatalf("参考退出码=%d want 0", reference.ExitCode)
	}
	kernel, stdout, code := runGuestOnKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), tidAddressExpectations) {
		t.Fatalf("VM 输出=% x want % x", []byte(stdout), tidAddressExpectations)
	}
	if code != 0 {
		t.Fatalf("VM 退出码=%d want 0", code)
	}
}
