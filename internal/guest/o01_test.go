package guest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

const o01IdentityDigest = "f182e75d91e108a6443426d0411cd84f98db3e15d0136858338f95e4e0da309e"

// TestIdentitySyscallsFilesAndInheritance 验证 O01:真实 ELF 完成
// setgid/setuid、文件权限检查、文件归属与 fork 子进程身份继承;
// 非特权进程不能再 setuid(0)。
func TestIdentitySyscallsFilesAndInheritance(t *testing.T) {
	image, err := os.ReadFile("testdata/o01-identity")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != o01IdentityDigest {
		t.Fatalf("O01 探针摘要=%s want %s", got, o01IdentityDigest)
	}
	kernel, system := guestKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	if err := system.Mkdir(context.Background(), "/work", 0o777); err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(context.Background(), "/work/owned", 0o777); err != nil {
		t.Fatal(err)
	}
	output := &strings.Builder{}
	process, err := kernel.Begin(0, "/probe", nil, "/", nil, fd.Streams{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	status, err := kernel.RunRegistered(context.Background(), process)
	if err != nil {
		t.Fatalf("探针运行: %v", err)
	}
	if status.Code != 0 || status.Signal.Valid() {
		t.Fatalf("探针退出=%+v 输出=%q(断言码见 o01-identity.c)", status, output.String())
	}
	if output.String() != "O01-OK\n" {
		t.Fatalf("输出=%q want O01-OK", output.String())
	}
	if _, err := system.Stat(context.Background(), "/work/owned/child"); err != nil {
		t.Fatalf("子进程继承身份后未能写文件: %v", err)
	}
}
