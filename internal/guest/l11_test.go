package guest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
)

// TestSignalfdProbeIsDeterministic 锁定 signalfd 探针产物:同一源码两次
// 构建字节一致,摘要与登记值相符。
func TestSignalfdProbeIsDeterministic(t *testing.T) {
	first := guest.SignalfdProbe()
	second := guest.SignalfdProbe()
	if !bytes.Equal(first, second) {
		t.Fatal("signalfd 探针两次构建字节不一致")
	}
	if got := guest.ImageDigestOf(first); got != guest.SignalfdProbeImageSHA256 {
		t.Fatalf("signalfd 探针摘要=%s want %s", got, guest.SignalfdProbeImageSHA256)
	}
}

// TestSignalfdProbeRunsOnKernel 验证 L11 端到端:真实 RISC-V ELF 在
// 真实内核(非替身控制端口)上 rt_sigprocmask 屏蔽 SIGUSR1、
// signalfd4 创建、kill(getpid(), SIGUSR1)、read(fd) 读取 128 字节
// siginfo,以 ssi_signo=10 退出。SIGUSR1 默认处置本应终止进程;
// 退出码为 10 证明信号经 fd 队列被读取而非触发默认处置。
func TestSignalfdProbeRunsOnKernel(t *testing.T) {
	kernel, stdout, code := runGuestOnKernel(t, guest.SignalfdProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	if stdout != "" {
		t.Fatalf("输出=%q want 空", stdout)
	}
	if code != 10 {
		t.Fatalf("退出码=%d want 10(ssi_signo)", code)
	}
}
