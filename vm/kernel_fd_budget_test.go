package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

func descriptorBudgetKernel(t *testing.T, descriptors uint64) (*Kernel, proc.Proc) {
	t.Helper()
	meters, err := resources.NewSession(
		resources.Limits{Memory: 1 << 20, Work: 1 << 20, Descriptors: descriptors},
		resources.Limits{},
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:      capability.Set{FD: fd.New()},
		Programs: registry,
		Meters:   meters,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process
}

func TestDescriptorBudgetIsEnforced(t *testing.T) {
	ctx := context.Background()

	kernel, process := descriptorBudgetKernel(t, 4)
	if err := kernel.InstallFD(process.ID, 10, &countingStream{}); err != nil {
		t.Fatalf("预算内应可安装: %v", err)
	}

	if err := kernel.InstallFD(process.ID, 11, &countingStream{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("超预算应报 EAGAIN: %v", err)
	}
	if _, err := kernel.OpenFDs(process.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.ReadFD(ctx, process.ID, 11, make([]byte, 1)); !errors.Is(err, errno.BadHandle) {
		t.Fatal("被拒绝的安装不得留下描述符")
	}

	if err := kernel.InstallFD(process.ID, 10, &countingStream{}); err != nil {
		t.Fatalf("替换不应被预算挡住: %v", err)
	}

	if err := kernel.CloseFD(process.ID, 10); err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(process.ID, 11, &countingStream{}); err != nil {
		t.Fatalf("归还后应可安装: %v", err)
	}

	if err := kernel.CloseFD(process.ID, 11); err != nil {
		t.Fatal(err)
	}
	if err := kernel.CloseFD(process.ID, 11); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("重复关闭应报 EBADF: %v", err)
	}
	if err := kernel.InstallFD(process.ID, 12, &countingStream{}); err != nil {
		t.Fatalf("重复关闭后仍应有一个空位: %v", err)
	}
	if err := kernel.InstallFD(process.ID, 13, &countingStream{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("重复关闭不得释放额外额度: %v", err)
	}
}

func TestDescriptorBudgetReleasedOnProcessExit(t *testing.T) {
	kernel, process := descriptorBudgetKernel(t, 4)
	if err := kernel.InstallFD(process.ID, 10, &countingStream{}); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	if err := kernel.Finish(0, process.ID, ExitStatus{}); err != nil {
		t.Fatal(err)
	}

	next, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(next.ID, 10, &countingStream{}); err != nil {
		t.Fatalf("回收后新进程应可安装: %v", err)
	}
}

func TestBudgetFuncInjectsPerProcessLimits(t *testing.T) {
	meters, err := resources.NewSession(
		resources.Limits{Memory: 1 << 20, Work: 1 << 20, Descriptors: 64},
		resources.Limits{},
	)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{
		"tight": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
		"noop":  executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:      capability.Set{FD: fd.New()},
		Programs: registry,
		Meters:   meters,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}

	kernel.SetBudget(func(process proc.Proc) resources.Limits {
		limits := resources.Limits{Memory: 1 << 20, Work: 1 << 20, Descriptors: 64}
		if process.Program == "tight" {
			limits.Descriptors = 3
		}
		return limits
	})
	tight, err := kernel.Begin(0, "tight", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(tight.ID, 10, &countingStream{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("注入的紧限额应生效: %v", err)
	}

	loose, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(loose.ID, 10, &countingStream{}); err != nil {
		t.Fatalf("默认限额应放行: %v", err)
	}
}
