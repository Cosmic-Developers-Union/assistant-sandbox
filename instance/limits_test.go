package instance

import (
	"context"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestCPUBudgetStopsSpinAndKeepsManagementResponsive 验证 O03:纯 CPU
// 死循环在指令预算耗尽后被限制,其他进程/连接仍可工作。
func TestCPUBudgetStopsSpinAndKeepsManagementResponsive(t *testing.T) {
	quick := executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 7, nil })
	machine, err := New(t.Context(), Options{
		Deterministic: true,
		Seed:          1,
		Session:       resources.Limits{Memory: 128 << 20, Work: 1 << 30, Descriptors: 64},
		Process:       resources.Limits{Memory: 64 << 20, Work: 1 << 20, Descriptors: 32},
		Programs:      map[string]executable.Program{"quick": quick},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	probe, err := machine.Filesystem().Open(t.Context(), "/spin", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Write(guest.SpinForeverProbe()); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	spin, err := connection.Exec(t.Context(), ExecSpec{Program: "/spin", Streams: fd.Streams{}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err := connection.Wait(t.Context(), spin.ID)
		if err == nil {
			if state.ExitCode == 0 {
				t.Fatalf("死循环不应成功退出: %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("指令预算未限制死循环: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// 管理面/其他任务仍响应:新进程有独立预算并正常完成。
	other, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := other.Exec(t.Context(), ExecSpec{Program: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := other.Wait(t.Context(), process.ID)
	if err != nil || state.ExitCode != 7 {
		t.Fatalf("预算耗尽后 VM 不可用: %+v err=%v", state, err)
	}
}

// TestProcessWallTimeoutStopsRunaway 验证 O03 的运行时间限制与资源
// 回收:超过墙钟上限的进程结束,VM 继续服务。
func TestProcessWallTimeoutStopsRunaway(t *testing.T) {
	machine, err := New(t.Context(), Options{
		Deterministic:  true,
		Seed:           1,
		ProcessTimeout: 60 * time.Millisecond,
		Session:        resources.Limits{Memory: 128 << 20, Work: 1 << 32},
		Process:        resources.Limits{Memory: 64 << 20, Work: 1 << 32},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	probe, err := machine.Filesystem().Open(t.Context(), "/spin", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Write(guest.SpinForeverProbe()); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	process, err := connection.Exec(t.Context(), ExecSpec{Program: "/spin", Streams: fd.Streams{}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode == 0 {
		t.Fatalf("超时进程应非零退出: %+v", state)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("墙钟限制未生效: %v", elapsed)
	}
	if _, err := connection.Exec(t.Context(), ExecSpec{Program: "missing"}); err == nil {
		t.Fatal("VM 仍应可接受新请求")
	}
}

// TestMemoryBudgetIsolatesOneVM 验证 O04:每进程内存预算拒绝大映射,
// 另一台 VM 在自身预算内不受影响。
func TestMemoryBudgetIsolatesOneVM(t *testing.T) {
	limited, err := New(t.Context(), Options{
		Deterministic: true, Seed: 1,
		Session: resources.Limits{Memory: 64 << 20, Work: 1 << 32},
		Process: resources.Limits{Memory: 16 << 20, Work: 1 << 32},
		Programs: map[string]executable.Program{
			"quick": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 5, nil }),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = limited.Close(context.Background()) }()
	spacious, err := New(t.Context(), Options{
		Deterministic: true, Seed: 2,
		Session: resources.Limits{Memory: 256 << 20, Work: 1 << 32},
		Process: resources.Limits{Memory: 128 << 20, Work: 1 << 32},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spacious.Close(context.Background()) }()
	for _, machine := range []*VM{limited, spacious} {
		probe, err := machine.Filesystem().Open(t.Context(), "/pressure", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := probe.Write(guest.MemoryPressureProbe()); err != nil {
			t.Fatal(err)
		}
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}
	}
	run := func(machine *VM) uint8 {
		connection, err := machine.Connect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		process, err := connection.Exec(t.Context(), ExecSpec{Program: "/pressure"})
		if err != nil {
			t.Fatal(err)
		}
		state, err := connection.Wait(t.Context(), process.ID)
		if err != nil {
			t.Fatal(err)
		}
		return state.ExitCode
	}
	if code := run(limited); code != 2 {
		t.Fatalf("受限 VM 的 64MiB 映射应失败(exit 2),得到 %d", code)
	}
	if code := run(spacious); code != 0 {
		t.Fatalf("另一台 VM 的映射应成功(exit 0),得到 %d", code)
	}
	// 受限 VM 仍可运行小任务(预算只影响目标分配)。
	connection, err := limited.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := connection.Exec(t.Context(), ExecSpec{Program: "quick"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil || state.ExitCode != 5 {
		t.Fatalf("受限 VM 的小任务=%+v err=%v", state, err)
	}
}
