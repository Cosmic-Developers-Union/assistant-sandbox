package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
)

func addressKernel(t *testing.T) (*Kernel, proc.Proc, *audit.Memory) {
	t.Helper()
	sink := audit.NewMemory()
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:       capability.Set{FD: fd.New(), Audit: sink},
		Programs:  registry,
		Sequencer: scheduler.New(64),
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process, sink
}

func TestAddressViolationReachesAudit(t *testing.T) {
	ctx := context.Background()
	kernel, process, sink := addressKernel(t)
	space, err := kernel.BindAddressSpace(process.ID, memory.New(0))
	if err != nil {
		t.Fatal(err)
	}

	err = space.Map(ctx, memory.Region{
		Start: 4096, End: 8192,
		Prot: memory.ProtRead | memory.ProtWrite | memory.ProtExec,
	})
	if !errors.Is(err, errno.Permission) {
		t.Fatalf("W^X 应报 EACCES: %v", err)
	}

	events := memoryEvents(sink.Events())
	if len(events) != 1 {
		t.Fatalf("应记录一条地址空间事件: %+v", events)
	}
	event := events[0]
	if event.PID != uint64(process.ID) {
		t.Fatalf("审计应带发起进程: %+v", event)
	}
	if event.Kind != scheduler.KindMemory {
		t.Fatalf("事件类别应为 memory: %+v", event)
	}
	if event.Sequence == 0 {
		t.Fatalf("事件必须有内核分配的序号: %+v", event)
	}
	detail := string(event.Payload)
	for _, want := range []string{"fault=protection", "start=4096", "end=8192", "rwx"} {
		if !containsText(detail, want) {
			t.Fatalf("审计详情应含 %q: %q", want, detail)
		}
	}

	if err := space.Map(ctx, memory.Region{Start: 1, End: 4096, Prot: memory.ProtRead}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未对齐应报 EINVAL: %v", err)
	}
	all := memoryEvents(sink.Events())
	if len(all) != 2 {
		t.Fatalf("应累计两条地址空间事件: %+v", all)
	}
	if all[1].Sequence <= all[0].Sequence {
		t.Fatalf("序号必须严格递增: %d -> %d", all[0].Sequence, all[1].Sequence)
	}

	every := sink.Events()
	for index := 1; index < len(every); index++ {
		if every[index].Sequence <= every[index-1].Sequence {
			t.Fatalf("全局序号必须严格递增: %+v", every)
		}
	}
}

func memoryEvents(events []audit.Event) []audit.Event {
	var out []audit.Event
	for _, event := range events {
		if event.Kind == scheduler.KindMemory {
			out = append(out, event)
		}
	}
	return out
}

func TestAddressSpaceLifecycleIsBoundToProcess(t *testing.T) {
	ctx := context.Background()
	kernel, process, _ := addressKernel(t)
	if _, err := kernel.AddressSpace(process.ID); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未绑定应报 ENOENT: %v", err)
	}
	space, err := kernel.BindAddressSpace(process.ID, memory.New(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := space.Map(ctx, memory.Region{Start: 4096, End: 8192, Prot: memory.ProtRead}); err != nil {
		t.Fatal(err)
	}
	if got, err := kernel.AddressSpace(process.ID); err != nil || got == nil {
		t.Fatalf("绑定后应可取到: %v %v", got, err)
	}

	if _, err := kernel.BindAddressSpace(proc.PID(999), memory.New(0)); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}

	if _, err := kernel.RunRegistered(ctx, process); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(process.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.AddressSpace(process.ID); !errors.Is(err, errno.NotFound) {
		t.Fatalf("回收后应报 ENOENT: %v", err)
	}

	if err := kernel.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.BindAddressSpace(process.ID, memory.New(0)); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后绑定应报 ECANCELED: %v", err)
	}
	if _, err := kernel.AddressSpace(process.ID); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后查询应报 ECANCELED: %v", err)
	}
}

func containsText(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
