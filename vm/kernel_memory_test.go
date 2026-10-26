package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// idleRegistry 是内存隔离/预算测试的空闲程序注册表。
func idleRegistry(t *testing.T) executable.Registry {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"idle": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// isolationKernel 构造内核并为工作进程绑定手工地址空间,返回内核、
// 进程与守卫(越权上报经 violations 通道观察)。
func isolationKernel(t *testing.T, meters *resources.Meters) (*Kernel, proc.Proc, *memory.Guard, chan memory.Violation) {
	t.Helper()
	kernel, err := New(Services{
		Set:      capability.Set{FD: fd.New(), Resources: meters},
		Meters:   meters,
		Programs: idleRegistry(t),
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kernel.Close(context.Background()) })
	process, err := kernel.Begin(0, "idle", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	table := memory.New(0)
	violations := make(chan memory.Violation, 8)
	guard := memory.NewGuard(table, func(_ context.Context, violation memory.Violation) {
		violations <- violation
	})
	if _, err := kernel.BindAddressSpace(process.ID, table); err != nil {
		t.Fatal(err)
	}
	return kernel, process, guard, violations
}

// TestCrossProcessSameAddressIsolated 验证 E14:fork 后父子对同一
// 虚拟地址各自读写互不可见;跨 VM 实例同样隔离。
func TestCrossProcessSameAddressIsolated(t *testing.T) {
	ctx := context.Background()
	kernel, parent, parentGuard, _ := isolationKernel(t, nil)
	const address = 0x10000
	if err := parentGuard.Map(ctx, memory.Region{Start: address, End: address + memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite}); err != nil {
		t.Fatal(err)
	}
	child, childTable, err := (procControl{k: kernel, self: parent.ID}).Fork()
	if err != nil {
		t.Fatal(err)
	}
	childGuard, ok := kernel.spaces[child.ID].(*memory.Guard)
	if !ok {
		t.Fatal("子进程地址空间不是守卫")
	}
	_ = childTable
	// 父写 0xAA,子写 0xBB:同一虚拟地址各自持有。
	if err := parentGuard.Inner().Write(ctx, address, []byte{0xAA}); err != nil {
		t.Fatal(err)
	}
	if err := childGuard.Inner().Write(ctx, address, []byte{0xBB}); err != nil {
		t.Fatal(err)
	}
	parentView := make([]byte, 1)
	childView := make([]byte, 1)
	if err := parentGuard.Inner().Read(ctx, address, parentView); err != nil || parentView[0] != 0xAA {
		t.Fatalf("父读=%#x err=%v", parentView, err)
	}
	if err := childGuard.Inner().Read(ctx, address, childView); err != nil || childView[0] != 0xBB {
		t.Fatalf("子读=%#x err=%v", childView, err)
	}
	// 父 unmap 不影响子。
	if err := parentGuard.Unmap(ctx, address, address+memory.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := childGuard.Inner().Read(ctx, address, childView); err != nil || childView[0] != 0xBB {
		t.Fatalf("父 unmap 影响子=%#x err=%v", childView, err)
	}

	// 跨 VM:两个内核实例同地址互不可见。
	_, otherProcess, otherGuard, _ := isolationKernel(t, nil)
	if err := otherGuard.Map(ctx, memory.Region{Start: address, End: address + memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite}); err != nil {
		t.Fatal(err)
	}
	if err := otherGuard.Inner().Write(ctx, address, []byte{0xCC}); err != nil {
		t.Fatal(err)
	}
	otherView := make([]byte, 1)
	if err := otherGuard.Inner().Read(ctx, address, otherView); err != nil || otherView[0] != 0xCC {
		t.Fatalf("跨 VM 读=%#x", otherView)
	}
	_ = otherProcess
}

// TestUnauthorizedAccessFailsAndReports 验证 E14:只读区间的写、
// 未映射区间的访问都失败且分类正确,守卫上报违例。
func TestUnauthorizedAccessFailsAndReports(t *testing.T) {
	_, _, guard, violations := isolationKernel(t, nil)
	ctx := context.Background()
	const address = 0x20000
	if err := guard.Map(ctx, memory.Region{Start: address, End: address + memory.PageSize, Prot: memory.ProtRead}); err != nil {
		t.Fatal(err)
	}
	// 越权写:EPERM(保护检查在页表层,CPU 故障上报路径见 E02/E13)。
	if err := guard.Inner().Write(ctx, address, []byte{1}); !errors.Is(err, errno.Permission) {
		t.Fatalf("越权写 err=%v want EPERM", err)
	}
	// 未映射访问:未映射故障类别。
	if err := guard.Inner().Read(ctx, 0x90000, make([]byte, 4)); !errors.Is(err, errno.Range) {
		t.Fatalf("未映射读 err=%v want 区间故障", err)
	}
	// 守卫上报:区域级操作失败(Map 重叠、Protect 空洞)有违例记录。
	if err := guard.Map(ctx, memory.Region{Start: address, End: address + memory.PageSize, Prot: memory.ProtRead}); !errors.Is(err, errno.Exist) {
		t.Fatalf("重叠映射 err=%v want EEXIST", err)
	}
	select {
	case violation := <-violations:
		if violation.Fault != memory.FaultUnmapped && violation.Fault != memory.FaultProtection {
			t.Fatalf("重叠违例=%+v", violation)
		}
	default:
		t.Fatal("区域操作失败未上报违例")
	}
	if err := guard.Protect(ctx, 0x80000, 0x81000, memory.ProtRead); !errors.Is(err, errno.Range) {
		t.Fatalf("空洞 Protect err=%v want 区间故障", err)
	}
	select {
	case violation := <-violations:
		if violation.Start != 0x80000 {
			t.Fatalf("空洞违例=%+v", violation)
		}
	default:
		t.Fatal("空洞未上报违例")
	}
	// 检查接口:只读区间申请写失败。
	if err := guard.Check(ctx, address, 4, memory.ProtWrite); !errors.Is(err, errno.Permission) {
		t.Fatalf("Check 写 err=%v want EPERM", err)
	}
	// mprotect 提权后可写(W^X 之外)。
	if err := guard.Protect(ctx, address, address+memory.PageSize, memory.ProtRead|memory.ProtWrite); err != nil {
		t.Fatal(err)
	}
	if err := guard.Inner().Write(ctx, address, []byte{2}); err != nil {
		t.Fatalf("提权后写 err=%v", err)
	}
}

// TestForkChargesChildPrivatePagesOnly 验证 E15:fork 为子进程的
// 私有页副本计费;共享映射区间(直通后备,不落页表)不重复扣费。
func TestForkChargesChildPrivatePagesOnly(t *testing.T) {
	meters, err := resources.NewSession(
		resources.Limits{Memory: 1 << 20, Work: 1 << 20},
		resources.Limits{Memory: 1 << 20, Work: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	kernel, parent, parentGuard, _ := isolationKernel(t, meters)
	ctx := context.Background()
	const address = 0x30000
	// 匿名两页(mapper 计费口径,此处手动模拟)+ 共享后备一页。
	if err := parentGuard.Map(ctx, memory.Region{Start: address, End: address + 2*memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite}); err != nil {
		t.Fatal(err)
	}
	if err := parentGuard.Map(ctx, memory.Region{
		Start: address + 2*memory.PageSize, End: address + 3*memory.PageSize,
		Prot: memory.ProtRead | memory.ProtWrite, Backend: sharedBackend{}, FileBase: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := parentGuard.Inner().Write(ctx, address, make([]byte, memory.PageSize)); err != nil {
		t.Fatal(err)
	}
	if err := parentGuard.Inner().Write(ctx, address+memory.PageSize, make([]byte, 8)); err != nil {
		t.Fatal(err)
	}
	// mapper 在映射创建时为父进程计费(此处模拟):只按私有页。
	if err := meters.ChargeKind(ctx, uint64(parent.ID), resources.KindMemory, 2*memory.PageSize); err != nil {
		t.Fatal(err)
	}
	child, _, err := (procControl{k: kernel, self: parent.ID}).Fork()
	if err != nil {
		t.Fatal(err)
	}
	childView, err := meters.View(ctx, uint64(child.ID))
	if err != nil {
		t.Fatal(err)
	}
	if childView.MemoryUsed != 2*memory.PageSize {
		t.Fatalf("子进程计费=%d want %d(两私有页;共享区间不重复扣费)", childView.MemoryUsed, 2*memory.PageSize)
	}
	parentView, err := meters.View(ctx, uint64(parent.ID))
	if err != nil {
		t.Fatal(err)
	}
	if parentView.MemoryUsed != 2*memory.PageSize {
		t.Fatalf("父进程计费=%d want %d(fork 不动父计费)", parentView.MemoryUsed, 2*memory.PageSize)
	}
}

// sharedBackend 是空的直通后备(不落页表)。
type sharedBackend struct{}

func (sharedBackend) ReadAt(offset uint64, buffer []byte) error {
	clear(buffer)
	return nil
}

func (sharedBackend) WriteAt(uint64, []byte) error { return nil }

// TestForkBudgetExhaustedFailsCleanly 验证 E15:fork 超出子进程内存
// 预算时失败,子进程被回收不留残骸,父进程计费不受影响。
func TestForkBudgetExhaustedFailsCleanly(t *testing.T) {
	meters, err := resources.NewSession(
		resources.Limits{Memory: 8 * memory.PageSize, Work: 1 << 20},
		resources.Limits{Memory: 4 * memory.PageSize, Work: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	kernel, parent, parentGuard, _ := isolationKernel(t, meters)
	ctx := context.Background()
	const address = 0x40000
	// 父进程 6 页私有数据(> 子进程 4 页预算),fork 的私有页副本
	// 计费将超出子进程预算。
	if err := parentGuard.Map(ctx, memory.Region{Start: address, End: address + 6*memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite}); err != nil {
		t.Fatal(err)
	}
	if err := parentGuard.Inner().Write(ctx, address, make([]byte, 6*memory.PageSize)); err != nil {
		t.Fatal(err)
	}
	before, err := meters.View(ctx, uint64(parent.ID))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = (procControl{k: kernel, self: parent.ID}).Fork()
	if err == nil {
		t.Fatal("超预算 fork 应失败")
	}
	after, err := meters.View(ctx, uint64(parent.ID))
	if err != nil {
		t.Fatal(err)
	}
	if after.MemoryUsed != before.MemoryUsed {
		t.Fatalf("失败 fork 改动父计费 %d→%d", before.MemoryUsed, after.MemoryUsed)
	}
	// 无残留子进程。
	for _, process := range kernel.Processes() {
		if process.Parent == parent.ID {
			t.Fatalf("失败 fork 留下子进程 %d", process.ID)
		}
	}
}
