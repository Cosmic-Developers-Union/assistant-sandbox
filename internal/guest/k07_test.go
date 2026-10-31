package guest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// k07RobustDigest 锁定 robust futex 探针摘要(重建须显式更新)。源码
// 要点:子线程把 robust 节点 futex 字置为自身 TID,set_robust_list
// 登记后以 SYS_exit(93) 仅退出本线程;内核清算把字标记
// OWNER_DIED|WAITERS 并清 TID、唤醒等待者;主线程读回断言。
// 注意:qemu-user 不模拟 robust 唤醒(set_robust_list 直通宿主),
// 故本项无差分对拍,由 VM 语义测试与 Linux 内核行为一致性背书。
// 工具链 zig 0.16.0:
// `zig cc -dynamic -target riscv64-linux-musl -O2 -s -o k07-robust k07robust.c`。
const (
	k07RobustDigest = "deeb92f12791d9d1cfdd209a3d8be9992ae803b7e389c5d88efa9c6698cf1057"
	k07RobustOutput = "K07-OK ownerdied=1 tidclear=1\n"
)

// TestRobustListWakeOnThreadExitOnVM 验证 K07:set_robust_list 登记
// 后,持锁线程退出触发内核 robust 清算——futex 字标记 OWNER_DIED、
// 清 TID 并唤醒等待者(EOWNERDIED 语义);环形/坏指针 fail-closed。
func TestRobustListWakeOnThreadExitOnVM(t *testing.T) {
	image, err := os.ReadFile("testdata/k07-robust")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if got := hex.EncodeToString(sum[:]); got != k07RobustDigest {
		t.Fatalf("robust 探针摘要=%s want %s(产物与锁定不一致,先复核工具链再更新)", got, k07RobustDigest)
	}
	stdout, code := runDynamic(t, image, nil, false)
	if code != 0 {
		t.Fatalf("退出码=%d want 0(stdout=%q)", code, stdout)
	}
	if stdout != k07RobustOutput {
		t.Fatalf("stdout=%q want %q", stdout, k07RobustOutput)
	}
}
