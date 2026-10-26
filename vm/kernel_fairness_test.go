package vm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// spinnerProgram 模拟纯 CPU 死循环:反复让出时间片(与执行器的
// 64Ki 指令片 Switcher 契约一致),直到上下文取消。
func spinnerProgram(counter *atomic.Int64) executable.Program {
	return executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		scheduling, ok := p.Control.(proc.ExecutionScheduling)
		if !ok {
			return 125, context.Canceled
		}
		for ctx.Err() == nil {
			if counter != nil {
				counter.Add(1)
			}
			if err := scheduling.Slice(ctx); err != nil {
				return 125, err
			}
		}
		return 0, ctx.Err()
	})
}

// startRegistered 在后台运行进程并回收状态。
func startRegistered(t *testing.T, kernel *Kernel, process proc.Proc) (done chan ExitStatus, errs chan error) {
	t.Helper()
	done = make(chan ExitStatus, 1)
	errs = make(chan error, 1)
	go func() {
		status, err := kernel.RunRegistered(context.Background(), process)
		done <- status
		errs <- err
	}()
	return done, errs
}

// TestSpinDoesNotStarveShortJob 验证 H14:CPU 死循环进程持续让片,
// 短任务仍及时获得执行权完成——不饿死。
func TestSpinDoesNotStarveShortJob(t *testing.T) {
	spinCount := &atomic.Int64{}
	registry, err := executable.New(map[string]executable.Program{
		"spin": spinnerProgram(spinCount),
		"short": executable.Func(func(context.Context, proc.Proc) (uint8, error) {
			return 7, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = kernel.Close(context.Background()) }()

	spinner, err := kernel.Begin(0, "spin", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	startRegistered(t, kernel, spinner)
	// 让死循环先占住执行权。
	deadline := time.Now().Add(3 * time.Second)
	for spinCount.Load() < 20 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if spinCount.Load() < 20 {
		t.Fatal("死循环未开始让片")
	}
	short, err := kernel.Begin(0, "short", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	done, errs := startRegistered(t, kernel, short)
	select {
	case status := <-done:
		if status.Code != 7 {
			t.Fatalf("短任务状态=%d want 7", status.Code)
		}
		if err := <-errs; err != nil {
			t.Fatalf("短任务 err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("死循环饿死了短任务")
	}
}

// TestFrequentWakeupDoesNotStarve 验证 H14:高频阻塞/唤醒的进程
// 反复进出执行队列,其他进程仍持续推进。
func TestFrequentWakeupDoesNotStarve(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{
		"flicker": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			scheduling, ok := p.Control.(proc.ExecutionScheduling)
			if !ok {
				return 125, context.Canceled
			}
			// 快速 Enter/Leave 三百次,模拟频繁唤醒。
			for i := 0; i < 300; i++ {
				if err := ctx.Err(); err != nil {
					return 0, ctx.Err()
				}
				if err := scheduling.Slice(ctx); err != nil {
					return 125, err
				}
			}
			return 3, nil
		}),
		"steady": executable.Func(func(context.Context, proc.Proc) (uint8, error) {
			return 9, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = kernel.Close(context.Background()) }()
	flicker, err := kernel.Begin(0, "flicker", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	startRegistered(t, kernel, flicker)
	steady, err := kernel.Begin(0, "steady", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	done, errs := startRegistered(t, kernel, steady)
	select {
	case status := <-done:
		if status.Code != 9 {
			t.Fatalf("稳态任务状态=%d want 9", status.Code)
		}
		if err := <-errs; err != nil {
			t.Fatalf("稳态任务 err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("频繁唤醒饿死了稳态任务")
	}
}

// TestExitRaceKeepsQueueServiceable 验证 H14:大批进程同时退出时,
// 等待执行权的进程仍拿到执行权并完成(退出竞争不清空队列)。
func TestExitRaceKeepsQueueServiceable(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{
		"quick": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 1, nil }),
		"late":  executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 5, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = kernel.Close(context.Background()) }()
	// 先起 late(排队持令牌),再并发 12 个 quick 竞争退出。
	late, err := kernel.Begin(0, "late", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	lateDone, _ := startRegistered(t, kernel, late)
	time.Sleep(20 * time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		process, err := kernel.Begin(0, "quick", nil, "/", nil, fd.Streams{})
		if err != nil {
			t.Fatal(err)
		}
		go func(p proc.Proc) {
			defer wg.Done()
			_, _ = kernel.RunRegistered(context.Background(), p)
		}(process)
	}
	wg.Wait()
	select {
	case status := <-lateDone:
		if status.Code != 5 {
			t.Fatalf("late 状态=%d want 5", status.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("退出竞争饿死了 late")
	}
}

// TestCloseTerminatesSpinners 验证 H14:死循环进程运行中,内核关闭
// 在有界时间内放行全部等待者并结束运行(VM 仍可关闭)。
func TestCloseTerminatesSpinners(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{
		"spin": spinnerProgram(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 8)
	if err != nil {
		t.Fatal(err)
	}
	spinners := make([]chan ExitStatus, 0, 3)
	for i := 0; i < 3; i++ {
		process, err := kernel.Begin(0, "spin", nil, "/", nil, fd.Streams{})
		if err != nil {
			t.Fatal(err)
		}
		done, _ := startRegistered(t, kernel, process)
		spinners = append(spinners, done)
	}
	time.Sleep(50 * time.Millisecond)
	closeStarted := time.Now()
	if err := kernel.Close(context.Background()); err != nil {
		t.Fatalf("关闭 err=%v", err)
	}
	for index, done := range spinners {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("死循环 %d 未被关闭终止", index)
		}
	}
	if elapsed := time.Since(closeStarted); elapsed > 5*time.Second {
		t.Fatalf("关闭耗时=%v", elapsed)
	}
}
