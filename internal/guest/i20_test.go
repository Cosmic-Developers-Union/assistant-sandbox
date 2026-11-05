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

// i20NotifyDigest 锁定 inotify 探针摘要(重建须显式更新)。源码要点:
// inotify_init1(IN_NONBLOCK) → add_watch(/work,CREATE|MODIFY|DELETE|OPEN)
// → openat(O_CREAT) 核对 CREATE+OPEN → write/close → unlink 核对
// MODIFY+DELETE → rm_watch 成功、重复移除失败 → 重新监视 MODIFY 后
// 写 17000 次核对 IN_Q_OVERFLOW 恰一条且保留事件 ≥16384 → close 后
// 读取 EBADF → 输出 I20-OK。工具链 zig 0.17.0:
// `zig cc -static -target riscv64-linux-musl -O2 -s -o i20-notify i20-notify.c`。
const i20NotifyDigest = "5436bfd2aab2eec7bc1be4475f8fe264a3f9936985d1364e3aa67a59ea611aa7"

// TestInotifyProbeRunsOnVM 验证 I20:真实 musl 程序经 26/27/28 号
// 系统调用完成创建/写入/删除/溢出/关闭的完整文件变更通知闭环。
func TestInotifyProbeRunsOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/i20-notify")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != i20NotifyDigest {
		t.Fatalf("inotify 探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, i20NotifyDigest)
	}
	kernel, system := guestKernel(t, image)
	defer func() { _ = kernel.Close(context.Background()) }()
	if err := system.Mkdir(context.Background(), "/work", 0o755); err != nil {
		t.Fatal(err)
	}
	output := &strings.Builder{}
	process, err := kernel.Begin(0, "/probe", nil, "/", nil, fd.Streams{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	code, err := kernel.RunRegistered(context.Background(), process)
	if err != nil {
		t.Fatalf("探针运行: %v", err)
	}
	if code.Code != 0 || code.Signal.Valid() {
		t.Fatalf("探针退出=%+v(断言码见 i20-notify.c 返回值)", code)
	}
	if got := output.String(); got != "I20-OK\n" {
		t.Fatalf("输出=%q want I20-OK(探针断言码见 i20-notify.c 返回值)", got)
	}
	_ = vmfs.Create
}
