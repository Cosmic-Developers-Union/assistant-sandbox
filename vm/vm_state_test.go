package vm

import (
	"errors"
	"sync"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestLifecycleLegalTransitions(C01):状态机表中全部合法转换成功且幂等。
func TestLifecycleLegalTransitions(t *testing.T) {
	for from, targets := range legalTransitions {
		for _, to := range targets {
			life := newLifecycle()
			life.state = from
			if err := life.enter(to); err != nil {
				t.Fatalf("%s → %s 应合法: %v", from, to, err)
			}
			if life.get() != to {
				t.Fatalf("%s → %s 后状态=%s", from, to, life.get())
			}
			if err := life.enter(to); err != nil {
				t.Fatalf("%s 重复进入 %s: %v", from, to, err)
			}
		}
	}
}

// TestLifecycleIllegalTransitionsAreStable(C01):全部非法转换返回稳定
// 错误(EINVAL、文本一致、状态不变);终态无任何出边。
func TestLifecycleIllegalTransitionsAreStable(t *testing.T) {
	all := []State{StateCreated, StateRunning, StateStopping, StateFailed, StateClosed}
	for _, from := range all {
		for _, to := range all {
			if from == to || transition(from, to) {
				continue
			}
			life := newLifecycle()
			life.state = from
			first, second := life.enter(to), life.enter(to)
			if first == nil {
				t.Fatalf("%s → %s 意外成功", from, to)
			}
			if !errors.Is(first, errno.Invalid) {
				t.Fatalf("%s → %s 错误=%v want EINVAL", from, to, first)
			}
			if first.Error() != second.Error() {
				t.Fatalf("%s → %s 错误不稳定: %q vs %q", from, to, first, second)
			}
			if life.get() != from {
				t.Fatalf("非法转换改变了状态: %s → %s", from, life.get())
			}
		}
	}
}

// TestLifecycleUsableOnlyWhenRunning(C01):仅运行态接受新任务。
func TestLifecycleUsableOnlyWhenRunning(t *testing.T) {
	for _, state := range all {
		life := newLifecycle()
		life.state = state
		if want := state == StateRunning; life.usable() != want {
			t.Fatalf("%s usable=%v want %v", state, life.usable(), want)
		}
	}
}

// TestKernelLifecycleObservation(C01):真实内核的状态可观察——构造后
// 运行,Close 后终态关闭,重复关闭幂等。
func TestKernelLifecycleObservation(t *testing.T) {
	kernel := snapshotKernel(t)
	if got := kernel.life.get(); got != StateRunning {
		t.Fatalf("构造后状态=%s want running", got)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := kernel.life.get(); got != StateClosed {
		t.Fatalf("Close 后状态=%s want closed", got)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatalf("重复 Close: %v", err)
	}
	if kernel.life.get() != StateClosed {
		t.Fatal("重复 Close 改变了终态")
	}
}

// TestLifecycleConcurrentEnter(C01):并发转换不撕裂状态。
func TestLifecycleConcurrentEnter(t *testing.T) {
	life := newLifecycle()
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_ = life.enter(StateRunning)
		}()
	}
	group.Wait()
	if life.get() != StateRunning {
		t.Fatalf("并发进入后状态=%s", life.get())
	}
}

var all = []State{StateCreated, StateRunning, StateStopping, StateFailed, StateClosed}
