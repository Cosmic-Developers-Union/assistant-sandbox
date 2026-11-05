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

// mNetDigest 锁定网络栈探针摘要(重建须显式更新)。源码要点:
// TCP 回环(fork 子进程 connect+收发,父进程 accept+收发+waitpid)、
// UDP 数据报(sendto/recvfrom 边界与来源地址、fcntl O_NONBLOCK 后
// EAGAIN)、AF_UNIX socketpair(双向+shutdown(WR) 后 EOF)——全部经
// 真实 musl 系统调用(198/200/201/202/203/206/207 等)。工具链
// zig 0.17.0:`zig cc -static -target riscv64-linux-musl -O2 -s -o m-net m-net.c`。
const mNetDigest = "a7a15b82058123621cd2afb0ffb15b9bdceec32ac67781530fd0a54b62671d26"

// TestNetworkProbeRunsOnVM 验证 M02–M05:socket/bind/listen/accept/
// connect/sendto/recvfrom/socketpair/shutdown 由真实客体 ELF 完成,
// 回环撮合在 VM 命名空间内。
func TestNetworkProbeRunsOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/m-net")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != mNetDigest {
		t.Fatalf("网络探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, mNetDigest)
	}
	kernel, _ := guestKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
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
		t.Fatalf("探针退出=%+v(断言码:m-net.c 返回值,100+TCP/200+UDP/300+unix)", status)
	}
	if got := output.String(); got != "M-NET-OK\n" {
		t.Fatalf("输出=%q want M-NET-OK", got)
	}
}
