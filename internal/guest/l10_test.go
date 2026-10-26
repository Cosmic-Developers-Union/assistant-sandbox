package guest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
)

// timerfdExpectations 是 14 个结果字节的 Linux 期望序列:create 无负值
// (0)、无到期非阻塞读 -EAGAIN(0xF5)、settime 0、gettime 0、value
// 秒==0(1)、value 纳秒>0(1)、interval 全零(1、1)、nanosleep 0、
// 过期计数==1(1)、到期后 gettime 0、interval 全零(1、1)、解除武装 0。
var timerfdExpectations = []byte{0, 0xF5, 0, 0, 1, 1, 1, 1, 0, 1, 0, 1, 1, 0}

// TestTimerfdProbeIsDeterministic 锁定 timerfd 探针产物摘要。
func TestTimerfdProbeIsDeterministic(t *testing.T) {
	first := guest.TimerfdProbe()
	second := guest.TimerfdProbe()
	if !bytes.Equal(first, second) {
		t.Fatal("timerfd 探针两次构建字节不一致")
	}
	if got := guest.ImageDigestOf(first); got != guest.TimerfdProbeImageSHA256 {
		t.Fatalf("timerfd 探针摘要=%s want %s", got, guest.TimerfdProbeImageSHA256)
	}
}

// TestTimerfdProbeSemanticsMatchLinux 验证 L10:真实 RISC-V ELF 在真实
// 内核上完成 timerfd 创建、非阻塞读 EAGAIN、武装/剩余查询、过期计数
// 读取、一次性解除与显式解除武装。
func TestTimerfdProbeSemanticsMatchLinux(t *testing.T) {
	kernel, stdout, code := runGuestOnKernel(t, guest.TimerfdProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), timerfdExpectations) {
		t.Fatalf("结果序列=% x want % x", []byte(stdout), timerfdExpectations)
	}
	if code != 0 {
		t.Fatalf("退出码=%d want 0", code)
	}
}

// TestTimerfdProbeMatchesReferenceLinux 同一字节码经 qemu-riscv64 参考
// 执行与本 VM 对拍。参考执行器不可用时显式跳过,由 CI differential
// 任务覆盖。
func TestTimerfdProbeMatchesReferenceLinux(t *testing.T) {
	image := guest.TimerfdProbe()
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if !bytes.Equal([]byte(reference.Stdout), timerfdExpectations) {
		t.Fatalf("参考 Linux 输出=% x want % x", []byte(reference.Stdout), timerfdExpectations)
	}
	if reference.ExitCode != 0 {
		t.Fatalf("参考退出码=%d want 0", reference.ExitCode)
	}
	kernel, stdout, code := runGuestOnKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), timerfdExpectations) {
		t.Fatalf("VM 输出=% x want % x", []byte(stdout), timerfdExpectations)
	}
	if code != 0 {
		t.Fatalf("VM 退出码=%d want 0", code)
	}
}
