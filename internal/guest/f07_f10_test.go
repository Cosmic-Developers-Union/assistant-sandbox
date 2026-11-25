package guest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// F07/F10 探针的产物摘要锁定:摘要变化说明工具链或源码变了,必须显式
// 重建并复核。源码与构建命令(zig 0.16.0):
//
//	libf10.so:   zig cc -shared -target riscv64-linux-musl -O2 -s \
//	               -Wl,-soname,libf10.so -o libf10.so f10lib.c
//	f10-dyn:     zig cc -dynamic -target riscv64-linux-musl -O2 -s \
//	               -o f10-dyn f10dyn.c -L. -lf10
//	f10-missing: 同 f10-dyn,但链接同 SONAME 的桩库(桩定义
//	               f10_missing);运行时装载真实 libf10.so(无此符号)
//	               即复现缺失符号错误。
//
// ld-musl-riscv64.so.1 由 A04 固定的 musl 1.2.6 源码构建,附加
// fpathconf 描述符校验补丁(见 testdata/lib/README.md);musl 的
// libc.so 与 ld-musl 同体,测试内以同一内容安装两个路径。
const (
	f10ProgramDigest   = "d348c2cb92af340a87cb72202b04ef3073c37f2e81d2b7905192f6458fb3ebe2"
	f10MissingDigest   = "23efbd1afd1440abbba5e3831eec9be513f124b1a79f02cef44c70760e863919"
	f10LibDigest       = "0232b1ecc51e982a5efa80cf00e6bb665b315918243f682c125c8f5d2f44fcaa"
	f10LoaderDigest    = "23ea7ef0c0fb58d350b0a847c75de7529a68c240af87c4fdd4a327444801b0fd"
	f10HappyOutput     = "F10-OK sum=23 heap=120\n"
	f10LoaderExitCode  = 127
	f10MissingFragment = "f10_missing: symbol not found"
	f10NoLibFragment   = "Error loading shared library libf10.so"
)

func f10Fixture(t *testing.T, name, digest string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if got := hex.EncodeToString(sum[:]); got != digest {
		t.Fatalf("%s 摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", name, got, digest)
	}
	return content
}

// bytesBuffer 聚合 stdout/stderr;错误路径下 ld-musl 把诊断写入 stderr。
type bytesBuffer struct{ data []byte }

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *bytesBuffer) String() string { return string(b.data) }

// runDynamic 在安装了动态装载环境的内核上执行主程序:ld-musl 与同体
// libc.so 恒定安装,libf10.so 按用例可选。返回 stdout+stderr 与退出码。
func runDynamic(t *testing.T, program, lib []byte, withLib bool) (string, uint8) {
	t.Helper()
	kernel, system := guestKernel(t, program)
	defer func() { _ = kernel.Close(context.Background()) }()
	if err := system.Mkdir(context.Background(), "/lib", 0o755); err != nil {
		t.Fatal(err)
	}
	install := func(path string, content []byte) {
		t.Helper()
		file, err := system.Open(context.Background(), path, vmfs.Create|vmfs.ReadWrite, 0o755)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if _, err := file.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	loader := f10Fixture(t, filepath.Join("lib", "ld-musl-riscv64.so.1"), f10LoaderDigest)
	install("/lib/ld-musl-riscv64.so.1", loader)
	install("/lib/libc.so", loader)
	if withLib {
		install("/lib/libf10.so", lib)
	}
	output := &bytesBuffer{}
	process, err := kernel.Begin(0, "/probe", nil, "/", nil, fd.Streams{Output: writerFunc(output.Write), Error: writerFunc(output.Write)})
	if err != nil {
		t.Fatal(err)
	}
	status, err := kernel.RunRegistered(context.Background(), process)
	if err != nil {
		t.Fatalf("运行动态程序: %v", err)
	}
	return output.String(), status.Code
}

// TestDynamicProgramRunsSharedLibraryOnVM 验证 F10:真实动态程序经
// ld-musl 装载 libc.so 与 libf10.so,跨库调用、输出与内存分配全部在
// 客体内完成(宿主不替代执行任何库代码)。
func TestDynamicProgramRunsSharedLibraryOnVM(t *testing.T) {
	program := f10Fixture(t, "f10-dyn", f10ProgramDigest)
	lib := f10Fixture(t, "libf10.so", f10LibDigest)
	stdout, code := runDynamic(t, program, lib, true)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != f10HappyOutput {
		t.Fatalf("stdout=%q want %q", stdout, f10HappyOutput)
	}
}

// TestDynamicProgramSyscallsFullyRecognised 验证 F10 的"无未识别必要
// 系统调用":轨迹里没有 ENOSYS(-38)或 ENOTSUP(-95)——二者即内核
// 缺口;ENOENT(-2)是加载器搜索共享库时的正常未命中,与 Linux 一致。
func TestDynamicProgramSyscallsFullyRecognised(t *testing.T) {
	program := f10Fixture(t, "f10-dyn", f10ProgramDigest)
	lib := f10Fixture(t, "libf10.so", f10LibDigest)
	kernel, system := guestKernel(t, program)
	defer func() { _ = kernel.Close(context.Background()) }()
	if err := system.Mkdir(context.Background(), "/lib", 0o755); err != nil {
		t.Fatal(err)
	}
	loader := f10Fixture(t, filepath.Join("lib", "ld-musl-riscv64.so.1"), f10LoaderDigest)
	for _, item := range []struct {
		path    string
		content []byte
	}{
		{"/lib/ld-musl-riscv64.so.1", loader},
		{"/lib/libc.so", loader},
		{"/lib/libf10.so", lib},
	} {
		file, err := system.Open(context.Background(), item.path, vmfs.Create|vmfs.ReadWrite, 0o755)
		if err != nil {
			t.Fatalf("%s: %v", item.path, err)
		}
		if _, err := file.Write(item.content); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	output := &bytesBuffer{}
	process, err := kernel.Begin(0, "/probe", nil, "/", nil, fd.Streams{Output: writerFunc(output.Write), Error: writerFunc(output.Write)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(context.Background(), process); err != nil {
		t.Fatalf("运行: %v", err)
	}
	for _, entry := range kernel.Trace().Entries() {
		if entry.Result == -38 || entry.Result == -95 {
			t.Fatalf("系统调用 %d 返回 %d:装载/运行存在未识别依赖", entry.Number, entry.Result)
		}
	}
	if output.String() != f10HappyOutput {
		t.Fatalf("stdout=%q want %q", output.String(), f10HappyOutput)
	}
}

// TestDynamicProgramMissingSymbolFailsLikeLoader 验证 F07:共享库缺少
// 程序引用的符号时,BIND_NOW 重定位失败报明确错误并以 127 退出,
// 不以伪成功继续。
func TestDynamicProgramMissingSymbolFailsLikeLoader(t *testing.T) {
	program := f10Fixture(t, "f10-missing", f10MissingDigest)
	lib := f10Fixture(t, "libf10.so", f10LibDigest)
	stdout, code := runDynamic(t, program, lib, true)
	if code != f10LoaderExitCode {
		t.Fatalf("退出码=%d want %d", code, f10LoaderExitCode)
	}
	if !strings.Contains(stdout, f10MissingFragment) {
		t.Fatalf("stderr=%q want 含 %q", stdout, f10MissingFragment)
	}
}

// TestDynamicProgramMissingLibraryFails 验证 F07:NEEDED 共享库缺失时
// 装载报明确错误并以 127 退出。
func TestDynamicProgramMissingLibraryFails(t *testing.T) {
	program := f10Fixture(t, "f10-dyn", f10ProgramDigest)
	lib := f10Fixture(t, "libf10.so", f10LibDigest)
	stdout, code := runDynamic(t, program, lib, false)
	if code != f10LoaderExitCode {
		t.Fatalf("退出码=%d want %d", code, f10LoaderExitCode)
	}
	if !strings.Contains(stdout, f10NoLibFragment) {
		t.Fatalf("stderr=%q want 含 %q", stdout, f10NoLibFragment)
	}
}

// TestDynamicProbeMatchesReferenceLinux 差分(F07/F10):同一份动态程序
// 与共享库在 qemu-riscv64(-L sysroot + LD_LIBRARY_PATH)参考执行与本
// VM 执行,退出码与 stdout 逐字节一致;缺失符号与缺库的退出码、错误
// 要点逐项一致。参考不可用时跳过,由 CI differential 任务覆盖。
func TestDynamicProbeMatchesReferenceLinux(t *testing.T) {
	program := f10Fixture(t, "f10-dyn", f10ProgramDigest)
	lib := f10Fixture(t, "libf10.so", f10LibDigest)
	missing := f10Fixture(t, "f10-missing", f10MissingDigest)
	root := t.TempDir()
	loader := f10Fixture(t, filepath.Join("lib", "ld-musl-riscv64.so.1"), f10LoaderDigest)
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		"lib/ld-musl-riscv64.so.1": loader,
		"lib/libc.so":              loader,
		"lib/libf10.so":            lib,
	} {
		if err := os.WriteFile(filepath.Join(root, path), content, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	happy, err := diff.ReferenceLinuxWithRoot(t.Context(), program, "", root)
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	if happy.ExitCode != 0 || happy.Stdout != f10HappyOutput {
		t.Fatalf("参考结果: 退出码=%d stdout=%q want 0/%q", happy.ExitCode, happy.Stdout, f10HappyOutput)
	}
	missingRef, err := diff.ReferenceLinuxWithRoot(t.Context(), missing, "", root)
	if err != nil {
		t.Fatal(err)
	}
	if missingRef.ExitCode != f10LoaderExitCode || !strings.Contains(missingRef.Stderr, f10MissingFragment) {
		t.Fatalf("参考缺失符号: 退出码=%d stderr=%q", missingRef.ExitCode, missingRef.Stderr)
	}
	if err := os.Remove(filepath.Join(root, "lib", "libf10.so")); err != nil {
		t.Fatal(err)
	}
	noLibRef, err := diff.ReferenceLinuxWithRoot(t.Context(), program, "", root)
	if err != nil {
		t.Fatal(err)
	}
	if noLibRef.ExitCode != f10LoaderExitCode || !strings.Contains(noLibRef.Stderr, f10NoLibFragment) {
		t.Fatalf("参考缺库: 退出码=%d stderr=%q", noLibRef.ExitCode, noLibRef.Stderr)
	}
	// VM 侧同一矩阵。
	if stdout, code := runDynamic(t, program, lib, true); code != 0 || stdout != f10HappyOutput {
		t.Fatalf("VM: 退出码=%d stdout=%q want 0/%q", code, stdout, f10HappyOutput)
	}
	if stdout, code := runDynamic(t, missing, lib, true); code != f10LoaderExitCode || !strings.Contains(stdout, f10MissingFragment) {
		t.Fatalf("VM 缺失符号: 退出码=%d stderr=%q", code, stdout)
	}
	if stdout, code := runDynamic(t, program, lib, false); code != f10LoaderExitCode || !strings.Contains(stdout, f10NoLibFragment) {
		t.Fatalf("VM 缺库: 退出码=%d stderr=%q", code, stdout)
	}
}
