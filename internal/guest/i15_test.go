package guest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
)

// pipeExpectations 是七个结果字节的 Linux 期望序列(低字节,-errno
// 取补码低 8 位):pipe2 成功 0、空读 -EAGAIN(0xF5)、写 2、读 2、
// 再空读 -EAGAIN(0xF5)、登记 SIG_IGN 成功 0、断管写 -EPIPE(0xE0)。
// 之后进程恢复 SIG_DFL 再断管写,以 SIGPIPE 终止(退出码 128+13)。
var pipeExpectations = []byte{0, 0xF5, 2, 2, 0xF5, 0, 0xE0}

// TestPipeProbeIsDeterministic 锁定管道探针产物摘要。
func TestPipeProbeIsDeterministic(t *testing.T) {
	first := guest.PipeProbe()
	second := guest.PipeProbe()
	if !bytes.Equal(first, second) {
		t.Fatal("管道探针两次构建字节不一致")
	}
	if got := guest.ImageDigestOf(first); got != guest.PipeProbeImageSHA256 {
		t.Fatalf("管道探针摘要=%s want %s", got, guest.PipeProbeImageSHA256)
	}
}

// TestPipeProbeSemanticsMatchLinux 验证 I15:真实 RISC-V ELF 在真实
// 内核上完成非阻塞 EAGAIN、数据往返、SIG_IGN 下断管写返回 EPIPE、
// 恢复默认处置后断管写以 SIGPIPE 终止。
func TestPipeProbeSemanticsMatchLinux(t *testing.T) {
	kernel, stdout, code := runGuestOnKernel(t, guest.PipeProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	if !bytes.Equal([]byte(stdout), pipeExpectations) {
		t.Fatalf("结果序列=% x want % x", []byte(stdout), pipeExpectations)
	}
	if code != 128+13 {
		t.Fatalf("退出码=%d want 141(SIGPIPE 默认处置终止)", code)
	}
}

// TestPipeProbeMatchesReferenceLinux 同一字节码经 qemu-riscv64 参考执行
// 与本 VM 对拍:七个结果字节与退出码一致。参考执行器不可用时显式
// 跳过,由 CI differential 任务覆盖。
func TestPipeProbeMatchesReferenceLinux(t *testing.T) {
	image := guest.PipeProbe()
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if !bytes.Equal([]byte(reference.Stdout), pipeExpectations) {
		t.Fatalf("参考 Linux 输出=% x want % x", []byte(reference.Stdout), pipeExpectations)
	}
	if reference.ExitCode != 128+13 {
		t.Fatalf("参考退出码=%d want 141", reference.ExitCode)
	}
	stdout, code := runPipeProbeOnKernel(t, image)
	if !bytes.Equal([]byte(stdout), pipeExpectations) {
		t.Fatalf("VM 输出=% x want % x", []byte(stdout), pipeExpectations)
	}
	if code != 128+13 {
		t.Fatalf("VM 退出码=%d want 141", code)
	}
}

// runPipeProbeOnKernel 执行管道探针并返回输出与退出码。
func runPipeProbeOnKernel(t *testing.T, image []byte) (string, uint8) {
	t.Helper()
	kernel, stdout, code := runGuestOnKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	return stdout, code
}
