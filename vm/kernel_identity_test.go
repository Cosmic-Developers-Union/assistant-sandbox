package vm

import (
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

// processState 读取指定进程的状态。
func processState(t *testing.T, kernel *Kernel, id proc.PID) proc.State {
	t.Helper()
	for _, p := range kernel.Processes() {
		if p.ID == id {
			return p.State
		}
	}
	t.Fatalf("进程 %d 不存在", id)
	return proc.Ready
}

// TestKernelWaitMarksParentWaiting 验证 H01:父进程阻塞等待子进程时
// 处于等待状态,子进程退出唤醒后恢复运行;等待者拿到的退出状态不受
// 影响。
func TestKernelWaitMarksParentWaiting(t *testing.T) {
	reg, err := registryFor("long")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = kernel.WaitChild(t.Context(), 0, 0, false)
	}()
	// 等待者进入等待状态。
	deadline := time.Now().Add(2 * time.Second)
	for processState(t, kernel, kernel.Init()) != proc.Waiting {
		if time.Now().After(deadline) {
			t.Fatalf("等待者未进入等待状态: %v", processState(t, kernel, kernel.Init()))
		}
		time.Sleep(time.Millisecond)
	}
	// 子进程退出后等待者被唤醒并恢复运行。
	if err := kernel.Signal(child.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("等待者未被唤醒")
	}
	if state := processState(t, kernel, kernel.Init()); state != proc.Running {
		t.Fatalf("唤醒后状态=%v want running", state)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
