package clock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestVirtualSeparatesRealtimeAndMonotonic 验证 L01:测试虚拟时钟把
// 实时时钟与单调时钟分开——调整系统时间只改实时钟,单调时钟不动。
func TestVirtualSeparatesRealtimeAndMonotonic(t *testing.T) {
	t.Parallel()
	var clock Virtual
	if err := clock.Advance(100); err != nil {
		t.Fatal(err)
	}
	monotonic, err := clock.Monotonic(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if monotonic != 100 {
		t.Fatalf("单调时钟=%d want 100", monotonic)
	}
	wall, err := clock.Wall(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if wall != 100 {
		t.Fatalf("实时时钟=%d want 100", wall)
	}
	// 系统时间调整:实时钟跳变,单调时钟保持不变。
	if err := clock.SetWallTo(1_000_000); err != nil {
		t.Fatal(err)
	}
	if wall, err := clock.Wall(t.Context()); err != nil || wall != 1_000_000 {
		t.Fatalf("调整后实时时钟=%d err=%v want 1000000", wall, err)
	}
	if monotonic, err := clock.Monotonic(t.Context()); err != nil || monotonic != 100 {
		t.Fatalf("调整后单调时钟=%d err=%v want 100", monotonic, err)
	}
	// 关闭后不接受调整。
	clock.Close()
	if err := clock.SetWallTo(1); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后调整应报 ECANCELED: %v", err)
	}
}

// TestVirtualSystemTimeChangeKeepsRelativeDeadline 验证 L01:系统时间
// 调整不改变相对等待期限——相对等待仍按单调时钟计量。
func TestVirtualSystemTimeChangeKeepsRelativeDeadline(t *testing.T) {
	t.Parallel()
	var clock Virtual
	if err := clock.SetWallTo(5_000_000_000); err != nil {
		t.Fatal(err)
	}
	start, err := clock.Monotonic(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := clock.Sleep(t.Context(), 1_000_000); err != nil {
		t.Fatal(err)
	}
	end, err := clock.Monotonic(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if end-start != 1_000_000 {
		t.Fatalf("相对等待的单调推进=%d want 1000000", end-start)
	}
}

// TestRealClockMonotonicNeverGoesBack 验证 L01:生产时钟的单调读数
// 只增不减,实时钟与单调钟是两个独立来源。
func TestRealClockMonotonicNeverGoesBack(t *testing.T) {
	t.Parallel()
	real := NewReal()
	previous, err := real.Monotonic(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		current, err := real.Monotonic(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if current < previous {
			t.Fatalf("单调时钟回退:%d → %d", previous, current)
		}
		previous = current
	}
	// 实时钟可读且与单调钟独立。
	if _, err := real.Wall(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestRealClockSleepHonoursContext 验证生产时钟的等待可被上下文取消。
func TestRealClockSleepHonoursContext(t *testing.T) {
	t.Parallel()
	real := NewReal()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := real.Sleep(ctx, 1_000_000_000); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后的等待 err=%v", err)
	}
	start := time.Now()
	if err := real.Sleep(t.Context(), 2_000_000); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < time.Millisecond {
		t.Fatalf("等待时长=%v want ≥1ms", elapsed)
	}
}
