package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/ipc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

func TestKernelRunsGuestLifecycleAndDeniedResources(t *testing.T) {
	var kernel *Kernel
	reg, _ := executable.New(map[string]executable.Program{"probe": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		if p.ID != 2 || p.Parent != 1 || p.State != proc.Running || p.Dir != "/" {
			t.Fatal(p)
		}
		network, err := p.Resources.Network()
		if err != nil {
			t.Fatal(err)
		}
		if stream, err := network.Dial(ctx, "tcp", "localhost:80"); stream != nil || !errors.Is(err, errno.NetworkUnreachable) {
			t.Fatal(stream, err)
		}
		comm, err := p.Resources.Communication()
		if err != nil {
			t.Fatal(err)
		}
		if stream, err := comm.Open(ctx, ipc.NamedPipe, "pipe"); stream != nil || !errors.Is(err, errno.NotImplemented) {
			t.Fatal(stream, err)
		}
		if _, err := p.Resources.Filesystem(); !errors.Is(err, errno.NotImplemented) {
			t.Fatal(err)
		}
		if _, err := kernel.Exec(ctx, "probe", nil); !errors.Is(err, errno.Busy) {
			t.Fatal("重入", err)
		}
		p.Args[0] = "mutated"
		return 7, nil
	})})
	var err error
	kernel, err = New(Services{Programs: reg}, 2)
	if err != nil {
		t.Fatal(err)
	}
	result, err := kernel.Exec(t.Context(), "probe", []string{"original"})
	if err != nil || result.State != proc.Zombie || result.ExitCode != 7 || result.Args[0] != "original" {
		t.Fatal(result, err)
	}
	if _, err := kernel.Exec(t.Context(), "probe", nil); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(result.ID); err != nil {
		t.Fatal(err)
	}
	if len(kernel.Processes()) != 1 {
		t.Fatal(kernel.Processes())
	}

	if err := kernel.CheckFeature(ScheduledJobs); err != nil {
		t.Fatal(err)
	}
	for _, f := range []Feature{PackageManagement, Sudo, MultiUser} {
		if !errors.Is(kernel.CheckFeature(f), errno.NotSupported) {
			t.Fatal(f)
		}
	}
	if !errors.Is(kernel.CheckFeature(99), errno.Invalid) {
		t.Fatal("接受未知能力")
	}
}
func TestKernelFailuresKeepExplicitExitStatus(t *testing.T) {
	if _, err := New(Services{}, 2); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	registry, _ := executable.New(nil)
	if _, err := New(Services{Programs: registry}, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	failure := errors.New("程序失败")
	ctx, cancel := context.WithCancel(t.Context())
	registry, _ = executable.New(map[string]executable.Program{
		"fail":   executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, failure }),
		"cancel": executable.Func(func(context.Context, proc.Proc) (uint8, error) { cancel(); return 0, nil }),
	})
	kernel, _ := New(Services{Programs: registry}, 5)
	if _, err := kernel.Exec(t.Context(), "missing", nil); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
	result, err := kernel.Exec(t.Context(), "fail", nil)
	if !errors.Is(err, failure) || result.ExitCode != 125 || result.State != proc.Zombie {
		t.Fatal(result, err)
	}
	result, err = kernel.Exec(ctx, "cancel", nil)
	if !errors.Is(err, context.Canceled) || result.ExitCode != 125 {
		t.Fatal(result, err)
	}
	before := len(kernel.Processes())
	if _, err := kernel.Exec(ctx, "fail", nil); !errors.Is(err, context.Canceled) || len(kernel.Processes()) != before {
		t.Fatal(err)
	}
	var typedNil *executable.Static
	if _, err := New(Services{Programs: typedNil}, 2); !errors.Is(err, errno.Invalid) {
		t.Fatal("typed nil 注册表", err)
	}
	empty, _ := New(Services{Programs: nilRegistry{}}, 2)
	if _, err := empty.Exec(t.Context(), "bad", nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

type nilRegistry struct{}

func (nilRegistry) Lookup(string) (executable.Program, error) { return nil, nil }

func TestPanicLeavesKernelFailedClosed(t *testing.T) {
	reg, _ := executable.New(map[string]executable.Program{"panic": executable.Func(func(context.Context, proc.Proc) (uint8, error) { panic("客体故障") })})
	kernel, _ := New(Services{Programs: reg}, 3)

	if _, err := kernel.Exec(t.Context(), "panic", nil); err == nil {
		t.Fatal("故障应返回错误")
	}
	if _, err := kernel.Exec(t.Context(), "panic", nil); !errors.Is(err, errno.Busy) {
		t.Fatal("故障后继续执行", err)
	}
}

func TestRegistryCallbacksCannotReenterKernel(t *testing.T) {
	registry := &callbackRegistry{}
	kernel, err := New(Services{Programs: registry}, 2)
	if err != nil {
		t.Fatal(err)
	}
	registry.lookup = func(name string) (executable.Program, error) {
		if _, err := kernel.Exec(t.Context(), name, nil); !errors.Is(err, errno.Busy) {
			t.Fatal("查询期间重入", err)
		}
		return executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }), nil
	}
	if _, err := kernel.Exec(t.Context(), "probe", nil); err != nil {
		t.Fatal(err)
	}
}

type callbackRegistry struct {
	lookup func(string) (executable.Program, error)
}

func (r *callbackRegistry) Lookup(name string) (executable.Program, error) { return r.lookup(name) }

func TestBeginRollsBackWhenMeterRejectsSpawn(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{
		"probe": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}

	meters, err := resources.NewSession(resources.Limits{Memory: 1 << 20, Work: 1}, resources.Limits{Memory: 1 << 20, Work: 2})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Programs: registry, Meters: meters}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Begin(kernel.Init(), "probe", nil, "/", nil, fd.Streams{}); err == nil {
		t.Fatal("计量拒绝时 Begin 必须失败")
	}

	if processes := kernel.Processes(); len(processes) != 1 || processes[0].ID != kernel.Init() {
		t.Fatalf("回滚残留进程: %+v", processes)
	}
}
