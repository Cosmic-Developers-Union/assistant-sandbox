package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/eventfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// nopStream 是预算测试用的空流。
type nopStream struct{}

func (nopStream) Read([]byte) (int, error)    { return 0, nil }
func (nopStream) Write(p []byte) (int, error) { return len(p), nil }
func (nopStream) Close() error                { return nil }

// noopProgram 是预算测试的空程序。
func noopProgram() executable.Program {
	return executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil })
}

// TestProcessLimitBlocksEveryEntry 验证 O02:进程数上限对 Begin 与
// Fork 两个入口一致生效;耗尽后创建失败,退出回收后额度恢复。
func TestProcessLimitBlocksEveryEntry(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{"noop": noopProgram()})
	if err != nil {
		t.Fatal(err)
	}
	const limit = 3
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, limit)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = kernel.Close(context.Background()) }()

	// 内核根进程占用一个名额:limit=3 时还能创建两个。
	first, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	// 上限已满:Begin 入口 EAGAIN。
	if _, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("Begin 超限=%v want EAGAIN", err)
	}
	// Fork 入口同样被上限挡住。
	if _, _, err := (procControl{k: kernel, self: first.ID}).Fork(); !errors.Is(err, errno.Limit) {
		t.Fatalf("Fork 超限=%v want EAGAIN", err)
	}
	// 退出并回收一个进程:僵尸占位,wait 回收后额度恢复(Linux 语义)。
	if err := kernel.Finish(0, second.ID, ExitStatus{Code: 0}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := kernel.WaitChild(context.Background(), 0, second.ID, true); err != nil {
		t.Fatalf("回收: %v", err)
	}
	recovered, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatalf("回收后应可创建: %v", err)
	}
	_ = recovered
	// 再次耗尽仍然拒绝。
	if _, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("再次超限=%v want EAGAIN", err)
	}
	_ = first
}

// descriptorBudgetKernelWithTable 构造带预算的内核(与既有
// descriptorBudgetKernel 同型,返回 fd 表供直接创建断言)。
func descriptorBudgetKernelWithTable(t *testing.T, descriptors uint64) (*Kernel, proc.Proc, *fd.Table, *resources.Meters) {
	t.Helper()
	meters, err := resources.NewSession(
		resources.Limits{Memory: 1 << 20, Work: 1 << 20, Descriptors: descriptors},
		resources.Limits{Memory: 1 << 20, Work: 1 << 20, Descriptors: descriptors},
	)
	if err != nil {
		t.Fatal(err)
	}
	table := fd.New()
	registry, err := executable.New(map[string]executable.Program{"noop": noopProgram()})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:      capability.Set{FD: table, Resources: meters},
		Meters:   meters,
		Programs: registry,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kernel.Close(context.Background()) })
	process, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process, table, meters
}

// TestCreationPathsShareDescriptorBudget 验证 O05:pipe(两槽)、
// eventfd、epoll 的创建全部消耗同一描述符预算;关闭归还后可再创建。
func TestCreationPathsShareDescriptorBudget(t *testing.T) {
	ctx := context.Background()
	// stdio 占 3 槽,预算另给 3 槽。
	_, process, table, _ := descriptorBudgetKernelWithTable(t, 6)

	// 管道占两槽:预算还剩 1。
	readEnd, writeEnd, err := table.Pipe(uint64(process.ID))
	if err != nil {
		t.Fatalf("管道创建: %v", err)
	}
	// eventfd 占最后一槽。
	event, err := table.EventFD(uint64(process.ID), 0, eventfd.Options{})
	if err != nil {
		t.Fatalf("eventfd 创建: %v", err)
	}
	// epoll:预算耗尽 → EAGAIN。
	if _, err := table.Epoll(uint64(process.ID)); !errors.Is(err, errno.Limit) {
		t.Fatalf("epoll 超预算=%v want EAGAIN", err)
	}
	// 关闭管道写端:归还一槽,epoll 现在可创建。
	if err := table.Close(ctx, uint64(process.ID), writeEnd); err != nil {
		t.Fatal(err)
	}
	epfd, err := table.Epoll(uint64(process.ID))
	if err != nil {
		t.Fatalf("归还后 epoll: %v", err)
	}
	// 显式安装路径同样受同一预算约束。
	if err := table.Install(uint64(process.ID), 40, nopStream{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("显式安装超预算=%v want EAGAIN", err)
	}
	_ = readEnd
	_ = event
	_ = epfd
}
