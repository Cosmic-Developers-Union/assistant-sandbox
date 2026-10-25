package guest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
)

// epollExpectations 是 21 个结果字节的 Linux 期望序列:前四位是负值
// errno 标记(0);空管道边缘等待 0、写入 1、边沿报告 1、EPOLLIN 置位
// 1、data 校验 1;边沿消费后 0;未排空新写入 1、新边沿报告 1、排空读
// 2、再写 1、又报告 1;MOD 一次性 0、重新武装立即报告 1;新数据写 1、
// 一次性禁用 0;再 MOD 0、再次报告 1。
var epollExpectations = []byte{0, 0, 0, 0, 0, 1, 1, 1, 1, 0, 1, 1, 2, 1, 1, 0, 1, 1, 0, 0, 1}

// TestEpollProbeIsDeterministic 锁定边缘触发/一次性探针产物:同一源码
// 两次构建字节一致,摘要与登记值相符。
func TestEpollProbeIsDeterministic(t *testing.T) {
	first := guest.EpollProbe()
	second := guest.EpollProbe()
	if !bytes.Equal(first, second) {
		t.Fatal("epoll 探针两次构建字节不一致")
	}
	if got := guest.ImageDigestOf(first); got != guest.EpollProbeImageSHA256 {
		t.Fatalf("epoll 探针摘要=%s want %s", got, guest.EpollProbeImageSHA256)
	}
}

// TestEpollProbeSemanticsMatchLinux 验证 L08:真实 RISC-V ELF 在真实
// 内核上完成边缘触发的初始边沿/新活动边沿/排空后再武装,与一次性
// 通知的禁用/重新武装序列。
func TestEpollProbeSemanticsMatchLinux(t *testing.T) {
	kernel, stdout, code := runGuestOnKernel(t, guest.EpollProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), epollExpectations) {
		t.Fatalf("结果序列=% x want % x", []byte(stdout), epollExpectations)
	}
	if code != 0 {
		t.Fatalf("退出码=%d want 0", code)
	}
}

// TestEpollProbeMatchesReferenceLinux 同一字节码经 qemu-riscv64 参考执行
// 与本 VM 对拍:21 个结果字节与退出码一致。参考执行器不可用时显式
// 跳过,由 CI differential 任务覆盖。
func TestEpollProbeMatchesReferenceLinux(t *testing.T) {
	image := guest.EpollProbe()
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if !bytes.Equal([]byte(reference.Stdout), epollExpectations) {
		t.Fatalf("参考 Linux 输出=% x want % x", []byte(reference.Stdout), epollExpectations)
	}
	if reference.ExitCode != 0 {
		t.Fatalf("参考退出码=%d want 0", reference.ExitCode)
	}
	kernel, stdout, code := runGuestOnKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), epollExpectations) {
		t.Fatalf("VM 输出=% x want % x", []byte(stdout), epollExpectations)
	}
	if code != 0 {
		t.Fatalf("VM 退出码=%d want 0", code)
	}
}
