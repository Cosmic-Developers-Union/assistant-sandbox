package guest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
)

// mmapExpectations 是十三个结果字节的 Linux 期望序列:四次 mmap 与
// openat 均无负值(0);私有映射读到 ELF 魔数(1、1);私有改 'X'
// 映射内可见(1)而文件保持 'E'(1);共享改 'Y' 映射内可见(1)、
// msync 返回 0、文件读到 'Y'(1)、第二共享映射一致(1)。
var mmapExpectations = []byte{0, 0, 1, 1, 0, 1, 1, 0, 1, 0, 1, 0, 1}

// TestMmapProbeIsDeterministic 锁定文件映射探针产物摘要。
func TestMmapProbeIsDeterministic(t *testing.T) {
	first := guest.MmapProbe()
	second := guest.MmapProbe()
	if !bytes.Equal(first, second) {
		t.Fatal("mmap 探针两次构建字节不一致")
	}
	if got := guest.ImageDigestOf(first); got != guest.MmapProbeImageSHA256 {
		t.Fatalf("mmap 探针摘要=%s want %s", got, guest.MmapProbeImageSHA256)
	}
}

// TestMmapProbeSemanticsMatchLinux 验证 E08/E09:真实 RISC-V ELF 在
// 真实内核上完成私有文件映射快照(修改不写回)与共享文件映射
// (直通写、msync、持久化与跨映射一致)。
func TestMmapProbeSemanticsMatchLinux(t *testing.T) {
	kernel, stdout, code := runGuestOnKernel(t, guest.MmapProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), mmapExpectations) {
		t.Fatalf("结果序列=% x want % x code=%d", []byte(stdout), mmapExpectations, code)
	}
	if code != 0 {
		t.Fatalf("退出码=%d want 0", code)
	}
}

// TestMmapProbeMatchesReferenceLinux 同一字节码经 qemu-riscv64 参考
// 执行与本 VM 对拍。参考执行器不可用时显式跳过,由 CI differential
// 任务覆盖。
func TestMmapProbeMatchesReferenceLinux(t *testing.T) {
	image := guest.MmapProbe()
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if !bytes.Equal([]byte(reference.Stdout), mmapExpectations) {
		t.Fatalf("参考 Linux 输出=% x want % x", []byte(reference.Stdout), mmapExpectations)
	}
	if reference.ExitCode != 0 {
		t.Fatalf("参考退出码=%d want 0", reference.ExitCode)
	}
	kernel, stdout, code := runGuestOnKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), mmapExpectations) {
		t.Fatalf("VM 输出=% x want % x", []byte(stdout), mmapExpectations)
	}
	if code != 0 {
		t.Fatalf("VM 退出码=%d want 0", code)
	}
}
