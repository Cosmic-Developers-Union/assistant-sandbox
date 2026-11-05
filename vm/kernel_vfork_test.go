package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

// TestVforkSharesAddressSpaceAndCopiesProcessState 验证 H12 内核语义:
// vfork 子进程共享调用方地址空间(同一张表,写入互见),描述符表与
// 信号处置按 fork 复制;父进程暂停与执行续接由执行器完成。
func TestVforkSharesAddressSpaceAndCopiesProcessState(t *testing.T) {
	ctx := context.Background()
	kernel, parent, parentGuard, _ := isolationKernel(t, nil)
	const address = 0x20000
	if err := parentGuard.Map(ctx, memory.Region{Start: address, End: address + memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite}); err != nil {
		t.Fatal(err)
	}
	child, childTable, err := (procControl{k: kernel, self: parent.ID}).Vfork()
	if err != nil {
		t.Fatal(err)
	}
	if childTable != parentGuard.Inner() {
		t.Fatal("vfork 子进程应与父进程共享同一地址空间表")
	}
	if got, ok := kernel.spaces[child.ID]; !ok || got != kernel.spaces[parent.ID] {
		t.Fatal("vfork 子进程的空间绑定应与父进程相同")
	}
	// 子进程对共享空间的写入对父进程可见(vfork 共享语义)。
	if err := childTable.Write(ctx, address, []byte{0x5B}); err != nil {
		t.Fatal(err)
	}
	var got [1]byte
	if err := parentGuard.Inner().Read(ctx, address, got[:]); err != nil {
		t.Fatal(err)
	}
	if got[0] != 0x5B {
		t.Fatalf("父进程经共享空间读到 %#x want 0x5B", got[0])
	}
	// 子进程是独立进程:父为调用方,状态 running。
	if child.Parent != parent.ID {
		t.Fatalf("子进程父=%d want %d", child.Parent, parent.ID)
	}
	if child.State.String() != "running" {
		t.Fatalf("子进程状态=%s want running", child.State)
	}
}

// TestVforkSpaceRebindPrepareCommitDiscard 验证 H12 换绑端口:准备
// 返回全新空表(沿用原表上限),提交后内核绑定切换到新表,丢弃后
// 原绑定保持不变。
func TestVforkSpaceRebindPrepareCommitDiscard(t *testing.T) {
	ctx := context.Background()
	kernel, parent, parentGuard, _ := isolationKernel(t, nil)
	control := procControl{k: kernel, self: parent.ID}

	fresh, err := control.PrepareSpace()
	if err != nil {
		t.Fatal(err)
	}
	if fresh == parentGuard.Inner() {
		t.Fatal("准备的新表不应是原表")
	}
	// 新表为空(全新地址空间)。
	regions, err := fresh.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 0 {
		t.Fatalf("新表应无映射区: %+v", regions)
	}
	// 丢弃:原绑定保持。
	before := kernel.spaces[parent.ID]
	control.DiscardSpace()
	if got := kernel.spaces[parent.ID]; got != before {
		t.Fatal("丢弃后绑定不变")
	}

	// 准备+提交:绑定切换到新表,原表仍归持有者使用。
	second, err := control.PrepareSpace()
	if err != nil {
		t.Fatal(err)
	}
	if err := control.CommitSpace(); err != nil {
		t.Fatal(err)
	}
	if bound := kernel.spaces[parent.ID]; bound == nil {
		t.Fatal("提交后应有空间绑定")
	} else if guard, ok := bound.(*memory.Guard); !ok || guard.Inner() != second {
		t.Fatal("提交后内核应绑定准备的新表")
	}
	// 原表内容不受影响(父进程保留原空间)。
	var got [1]byte
	if err := parentGuard.Inner().Read(ctx, 0x20000, got[:]); err == nil {
		t.Fatal("未映射地址读取应失败")
	}
	// 无准备提交:EINVAL。
	if err := control.CommitSpace(); !errors.Is(err, errno.Invalid) {
		t.Fatalf("无准备提交=%v want EINVAL", err)
	}
}
