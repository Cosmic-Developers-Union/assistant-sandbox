package guest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// h12 探针摘要锁定(重建须显式更新)。工具链 zig 0.17.0:
// `zig cc -static -target riscv64-linux-musl -O2 -s -o h12-vfork h12-vfork.c`
// 源码要点:parent_marker=0x5A 后 vfork();子进程(共享地址空间)写
// child_marker=0x5B,随后按模式 _exit(42) 或 execve("/h12-target");
// 父进程恢复后断言 child_marker 已写(暂停到子进程完成)、自身内存
// 完好、waitpid 读回退出码,并写 OK 标记。h12-target:write 标记后
// 以 33 退出。
const (
	h12VforkDigest  = "5f5f09d76e8ae65ca1b46f31ad50856675fece4a17c9d748edee749ea8595210"
	h12TargetDigest = "d6af24a2e1eeea95056ac988fcb47b512c5502332512b3360db516fd70d4b65c"
)

func h12LoadProbe(t *testing.T, name, digest string) []byte {
	t.Helper()
	image, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != digest {
		t.Fatalf("%s 摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", name, got, digest)
	}
	return image
}

// TestVforkSuspendsParentUntilChildCompletesOnVM 验证 H12 exit 路径:
// 父进程按 vfork 语义暂停到子进程退出——恢复时子进程写入的共享内存
// 可见(未暂停则存在竞争,断言不成立)、父进程自身内存完好、子进程
// 经 waitpid 读回退出码 42。
func TestVforkSuspendsParentUntilChildCompletesOnVM(t *testing.T) {
	image := h12LoadProbe(t, "h12-vfork", h12VforkDigest)
	kernel, system := guestKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	output := &strings.Builder{}
	parent, err := kernel.Begin(0, "/probe", []string{"exit"}, "/", nil, fd.Streams{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(context.Background(), parent); err != nil {
		t.Fatalf("父进程运行: %v", err)
	}
	if got := output.String(); got != "H12-EXIT-OK" {
		t.Fatalf("输出=%q want H12-EXIT-OK(探针断言码见 h12_test.go 源码注释)", got)
	}
	// 子进程已由父进程 waitpid 回收,不留僵尸(内核自身的 init 常驻
	// 会话除外)。
	for _, p := range kernel.Processes() {
		if p.ID == parent.ID || p.Program == "init" {
			continue
		}
		t.Fatalf("子进程应已被回收: %+v", p)
	}
	_ = system
}

// TestVforkChildExecKeepsParentSpaceOnVM 验证 H12 exec 路径:子进程
// execve 换绑全新地址空间——父进程保留原空间并在子进程 exec 后恢复,
// 继续执行自身指令(若 exec 清掉共享空间,父进程无法完成后续校验);
// 子进程新映像的输出经共享 stdout 到达,退出码 33 由 waitpid 读回。
func TestVforkChildExecKeepsParentSpaceOnVM(t *testing.T) {
	image := h12LoadProbe(t, "h12-vfork", h12VforkDigest)
	target := h12LoadProbe(t, "h12-target", h12TargetDigest)
	kernel, system := guestKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	file, err := system.Open(context.Background(), "/h12-target", vmfs.Create|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(target); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	output := &strings.Builder{}
	parent, err := kernel.Begin(0, "/probe", []string{"exec"}, "/", nil, fd.Streams{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(context.Background(), parent); err != nil {
		t.Fatalf("父进程运行: %v", err)
	}
	got := output.String()
	if !strings.Contains(got, "H12-EXEC-OK") {
		t.Fatalf("输出=%q want H12-EXEC-OK(探针断言码见 h12_test.go 源码注释)", got)
	}
	if !strings.Contains(got, "EXEC-TARGET-OK") {
		t.Fatalf("输出=%q want EXEC-TARGET-OK(子进程新映像输出)", got)
	}
	for _, p := range kernel.Processes() {
		if p.ID == parent.ID || p.Program == "init" {
			continue
		}
		t.Fatalf("子进程应已被回收: %+v", p)
	}
}
