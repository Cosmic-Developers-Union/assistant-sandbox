package guest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
)

// sessionErrnos 是十次会话调用的 Linux 期望 errno 序列:
// getpgid(0) 成功、getsid(0) 成功、setpgid(0,0) 成功、setsid 因调用方
// 已是组长报 EPERM、其余六次因目标进程/进程组不存在报 ESRCH。
// 进程组号与会话号本身不比较:参考 Linux(qemu 用户态)与本 VM 的进程
// 号体系不同,errno 序列才是可比语义。
var sessionErrnos = []byte{0, 0, 0, 1, 3, 3, 3, 3, 3, 3}

// TestSessionProbeIsDeterministic 锁定差分探针产物:同一源码两次
// 构建字节一致,摘要与登记值相符。
func TestSessionProbeIsDeterministic(t *testing.T) {
	first := guest.SessionProbe()
	second := guest.SessionProbe()
	if !bytes.Equal(first, second) {
		t.Fatal("会话探针两次构建字节不一致")
	}
	if got := guest.ImageDigestOf(first); got != guest.SessionProbeImageSHA256 {
		t.Fatalf("会话探针摘要=%s want %s", got, guest.SessionProbeImageSHA256)
	}
}

// guestKernel 构造承载客体程序的内核:客体文件系统、可写描述符表
// 与真实内核控制端口齐备(不走替身控制端口)。
func guestKernel(t *testing.T, image []byte) (*vm.Kernel, fs.System) {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := system.Open(context.Background(), "/probe", fs.Create|fs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set: capability.Set{
			FS:       system,
			FD:       fd.New(),
			Identity: identity.SingleUser{},
			Clock:    &clock.Virtual{},
			Entropy:  entropy.NewSeeded(1),
		},
		Programs: registry,
		Binaries: elf.Factory{},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, system
}

// runGuestOnKernel 在真实内核(而非替身控制端口)上执行客体程序:
// 内核的会话端口按 Linux 语义应答,客体经 riscv64 ABI 编号调用。
func runGuestOnKernel(t *testing.T, image []byte) (*vm.Kernel, string, uint8) {
	t.Helper()
	kernel, system := guestKernel(t, image)
	stdout, code := runGuestOnKernelWith(t, kernel, system, image)
	return kernel, stdout, code
}

// runGuestOnKernelWith 在既有内核上再次执行同一客体程序。
func runGuestOnKernelWith(t *testing.T, kernel *vm.Kernel, system fs.System, image []byte) (string, uint8) {
	t.Helper()
	file, err := system.Open(context.Background(), "/probe", fs.Create|fs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	process, err := kernel.Begin(0, "/probe", nil, "/", nil, fd.Streams{Output: writerFunc(output.Write)})
	if err != nil {
		t.Fatal(err)
	}
	status, err := kernel.RunRegistered(context.Background(), process)
	if err != nil {
		t.Fatalf("运行客体程序: %v", err)
	}
	return output.String(), status.Code
}

// runSessionProbeOnKernel 执行会话探针并返回输出与退出码。
func runSessionProbeOnKernel(t *testing.T, image []byte) (string, uint8) {
	t.Helper()
	kernel, stdout, code := runGuestOnKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	return stdout, code
}

// TestSessionProbeErrnosMatchLinuxSemantics 验证 H11:内核按 Linux
// 语义应答会话与进程组调用——成功为 0,已是组长时 setsid 报 EPERM,
// 目标不存在时报 ESRCH。
func TestSessionProbeErrnosMatchLinuxSemantics(t *testing.T) {
	stdout, code := runSessionProbeOnKernel(t, guest.SessionProbe())
	if code != 0 {
		t.Fatalf("退出码=%d want 0", code)
	}
	if stdout != string(sessionErrnos) {
		t.Fatalf("errno 序列=%v want %v", []byte(stdout), sessionErrnos)
	}
}

// TestSessionProbeMatchesReferenceLinux 差分(H11):同一份客体字节码在
// qemu-riscv64 参考 Linux 与本 VM 执行,errno 序列逐字节一致。
func TestSessionProbeMatchesReferenceLinux(t *testing.T) {
	image := guest.SessionProbe()
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	stdout, code := runSessionProbeOnKernel(t, image)
	if reference.Stdout != stdout {
		t.Fatalf("差分失败:参考 %v vs VM %v", []byte(reference.Stdout), []byte(stdout))
	}
	if reference.ExitCode != int(code) {
		t.Fatalf("差分失败:参考退出码=%d vs VM=%d", reference.ExitCode, code)
	}
}
