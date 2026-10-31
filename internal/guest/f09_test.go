package guest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
)

// f09StaticDigest 锁定静态 libc 探针的产物摘要:摘要变化说明工具链或
// 源码变了,必须显式重建并复核(与 A08 的摘要锁定同一纪律)。
// 探针源码与构建命令:testdata/f09-static 同目录说明;工具链 zig 0.16.0,
// `zig cc -target riscv64-linux-musl -static -O2 -s -o f09-static f09.c`。
const f09StaticDigest = "8289621e2f0598ce673a76009c2a6d878663e858703e52980b58d420ba200024"

// f09StaticOutput 是探针在参考 Linux 与本 VM 上的固定输出;文件副作用
// 为工作目录下 f09-out.txt,内容 "F09-DATA"(源码内锁定)。
const f09StaticOutput = "F09-OK argc=1 buf=F09 heap=5a5a\n"

func f09StaticImage(t *testing.T) []byte {
	t.Helper()
	image, err := os.ReadFile("testdata/f09-static")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(image)
	if got := hex.EncodeToString(digest[:]); got != f09StaticDigest {
		t.Fatalf("静态 libc 探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, f09StaticDigest)
	}
	return image
}

// TestStaticLibcProgramRunsOnVM 验证 F09:静态链接 musl 的真实 C 程序
// 在 VM 内完成启动、堆分配、文件写/定位/读与正常退出,输出与参考
// Linux 逐字节一致。
func TestStaticLibcProgramRunsOnVM(t *testing.T) {
	image := f09StaticImage(t)
	kernel, system := guestKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()

	stdout, code := runGuestOnKernelWith(t, kernel, system, image)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != f09StaticOutput {
		t.Fatalf("stdout=%q want %q", stdout, f09StaticOutput)
	}
	file, err := system.Open(context.Background(), "/f09-out.txt", 0, 0)
	if err != nil {
		t.Fatalf("读回副作用文件: %v", err)
	}
	defer func() { _ = file.Close() }()
	content := make([]byte, 32)
	n, err := file.Read(content)
	if err != nil && n == 0 {
		t.Fatalf("读取副作用文件: %v", err)
	}
	if string(content[:n]) != "F09-DATA" {
		t.Fatalf("文件内容=%q want F09-DATA", content[:n])
	}
}

// TestStaticLibcProbeMatchesReferenceLinux 差分(F09):同一份静态 libc
// 程序在 qemu-riscv64 参考执行与本 VM 执行,退出码、stdout 与文件
// 副作用逐项一致;参考不可用时跳过,由 CI differential 任务覆盖。
func TestStaticLibcProbeMatchesReferenceLinux(t *testing.T) {
	image := f09StaticImage(t)
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if reference.ExitCode != 0 {
		t.Fatalf("参考退出码=%d want 0", reference.ExitCode)
	}
	if reference.Stdout != f09StaticOutput {
		t.Fatalf("参考 stdout=%q want %q", reference.Stdout, f09StaticOutput)
	}
	if reference.Files["f09-out.txt"] != "F09-DATA" {
		t.Fatalf("参考文件副作用=%q want F09-DATA", reference.Files["f09-out.txt"])
	}
	stdout, code := runSessionProbeOnKernel(t, image)
	if code != 0 || stdout != f09StaticOutput {
		t.Fatalf("VM 退出码=%d stdout=%q want 0/%q", code, stdout, f09StaticOutput)
	}
}
