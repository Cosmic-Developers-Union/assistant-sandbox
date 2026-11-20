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

// mMsgDigest 锁定 sendmsg/recvmsg 探针摘要(重建须显式更新)。源码
// 要点:socketpair 上 sendmsg 传递两段分散缓冲 + SCM_RIGHTS 文件句柄,
// 发端随即关闭自己的副本;收端 recvmsg 取回 "xyz" 与控制消息,对
// 传入句柄读写验证独立引用所有权。工具链 zig 0.17.0:
// `zig cc -static -target riscv64-linux-musl -O2 -s -o m-msg m-msg.c`。
const mMsgDigest = "277639de6debd1c7070a4f7a7ef47c253832f1c94cb48664776fae9d4f620e2d"

// TestSendMsgRightsProbeRunsOnVM 验证 M08:sendmsg/recvmsg 的分散
// 缓冲、SCM_RIGHTS 句柄传递与接收方独立引用所有权经真实 musl ELF
// 完成(211/212 号调用)。
func TestSendMsgRightsProbeRunsOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/m-msg")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != mMsgDigest {
		t.Fatalf("msg 探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, mMsgDigest)
	}
	kernel, system := guestKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	// 探针的传递目标文件须存在于客体文件系统。
	file, err := system.Open(context.Background(), "/msg-data", 0x41 /*CREATE|RDWR*/, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
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
		t.Fatalf("探针退出=%+v 输出=%q(断言码见 m-msg.c 返回值)", status, output.String())
	}
	if got := output.String(); got != "M-MSG-OK\n" {
		t.Fatalf("输出=%q want M-MSG-OK", got)
	}
}
