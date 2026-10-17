package clock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestLogicalClockAdvanceCancellationAndOverflow(t *testing.T) {
	var clock Virtual
	if wall, err := clock.Wall(t.Context()); wall != 0 || err != nil {
		t.Fatal(wall, err)
	}
	if err := clock.Sleep(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if monotonic, err := clock.Monotonic(t.Context()); monotonic != 100 || err != nil {
		t.Fatal(monotonic, err)
	}

	if err := clock.Sleep(t.Context(), 1<<63); !errors.Is(err, errno.Overflow) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := clock.Wall(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := clock.Sleep(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if wall, _ := clock.Wall(t.Context()); wall != 100 {
		t.Fatal(wall)
	}
}

func TestSleepZeroDoesNotAdvance(t *testing.T) {
	t.Parallel()
	var clock Virtual
	if err := clock.Sleep(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if now, _ := clock.Monotonic(t.Context()); now != 0 {
		t.Fatal(now)
	}
}

func TestConcurrentSleepsConverge(t *testing.T) {
	t.Parallel()
	var clock Virtual
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, delay := range []uint64{50, 100, 20} {
		wg.Go(func() {
			<-start
			if err := clock.Sleep(t.Context(), delay); err != nil {
				t.Error(err)
				return
			}
			now, _ := clock.Monotonic(t.Context())

			if now < delay {
				t.Errorf("returned early: %d < %d", now, delay)
			}
		})
	}
	close(start)
	wg.Wait()
	if now, _ := clock.Monotonic(t.Context()); now < 100 {
		t.Fatalf("final=%d", now)
	}
}

func TestSameDeadlineWakesInRegistrationOrder(t *testing.T) {
	t.Parallel()
	var clock Virtual
	var mu sync.Mutex
	var order []int
	done := make(chan struct{})
	var wg sync.WaitGroup
	for id := range 3 {
		wg.Go(func() {
			if err := clock.Sleep(t.Context(), 10); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			done <- struct{}{}
		})
		time.Sleep(10 * time.Millisecond)
	}
	go func() {
		for range 3 {
			<-done
		}
		close(done)
	}()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != 0 || order[1] != 1 || order[2] != 2 {
		t.Fatalf("wake order=%v", order)
	}
}

func TestCloseCancelsBlockedSleepAndStopsAdvance(t *testing.T) {
	t.Parallel()
	var clock Virtual
	if err := clock.Sleep(t.Context(), 1000); err != nil {
		t.Fatal(err)
	}

	clock.Close()
	if err := clock.Sleep(t.Context(), 1); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("err=%v", err)
	}
	if now, _ := clock.Monotonic(t.Context()); now != 1000 {
		t.Fatalf("关闭后时间被推进: %d", now)
	}
	if err := clock.Advance(10); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("advance=%v", err)
	}
}

func TestAdvanceMovesTimeForLaterSleeps(t *testing.T) {
	t.Parallel()
	var clock Virtual

	if err := clock.Sleep(t.Context(), 500); err != nil {
		t.Fatal(err)
	}
	if now, _ := clock.Monotonic(t.Context()); now != 500 {
		t.Fatalf("now=%d", now)
	}

	if err := clock.Advance(300); err != nil {
		t.Fatal(err)
	}
	if now, _ := clock.Monotonic(t.Context()); now != 800 {
		t.Fatalf("now=%d", now)
	}
	if err := clock.Sleep(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if now, _ := clock.Monotonic(t.Context()); now != 900 {
		t.Fatalf("now=%d", now)
	}
}

func TestSleepRejectsOverflowAtCurrentEpoch(t *testing.T) {
	t.Parallel()
	var clock Virtual
	if err := clock.Advance(maxNanoseconds - 5); err != nil {
		t.Fatal(err)
	}
	if err := clock.Sleep(t.Context(), 100); !errors.Is(err, errno.Overflow) {
		t.Fatalf("err=%v", err)
	}
	if err := clock.Advance(100); !errors.Is(err, errno.Overflow) {
		t.Fatalf("err=%v", err)
	}
}

func TestSleepRegistersThenClearsTimer(t *testing.T) {
	t.Parallel()
	var clock Virtual
	if err := clock.Sleep(t.Context(), 500); err != nil {
		t.Fatal(err)
	}
	if now, _ := clock.Monotonic(t.Context()); now != 500 {
		t.Fatalf("now=%d", now)
	}
	clock.mu.Lock()
	left := len(clock.timers)
	clock.mu.Unlock()
	if left != 0 {
		t.Fatalf("返回后不得残留注册: %d", left)
	}
}

func TestSleepAccumulatesFromCurrentEpoch(t *testing.T) {
	t.Parallel()
	var clock Virtual
	for i := range 3 {
		if err := clock.Sleep(t.Context(), 500); err != nil {
			t.Fatal(err)
		}
		if now, _ := clock.Monotonic(t.Context()); now != uint64(500*(i+1)) {
			t.Fatalf("now=%d", now)
		}
	}
}

func TestWallReadDuringAdvance(t *testing.T) {
	t.Parallel()
	var clock Virtual
	if err := clock.Advance(7); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if wall, err := clock.Wall(t.Context()); err != nil || wall != 7 {
			t.Errorf("wall=%d err=%v", wall, err)
		}
	})
	wg.Wait()
}
