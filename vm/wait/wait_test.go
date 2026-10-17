package wait

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func waitRegistered(t *testing.T, q *Queue, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for q.Waiters() < n {
		if time.Now().After(deadline) {
			t.Fatalf("等待者未注册: %d/%d", q.Waiters(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWaitReturnsImmediatelyWhenConditionHolds(t *testing.T) {
	t.Parallel()
	q := New()

	reason, err := q.Wait(t.Context(), func() (bool, Reason, error) { return true, Data, nil })
	if err != nil || reason != Data {
		t.Fatalf("reason=%v err=%v", reason, err)
	}
}

func TestWaitWakesOnExplicitSignal(t *testing.T) {
	t.Parallel()
	q := New()
	var ready atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := q.Wait(t.Context(), func() (bool, Reason, error) { return ready.Load(), Data, nil })
		if err != nil {
			t.Error(err)
		}
	}()
	waitRegistered(t, q, 1)
	ready.Store(true)
	if n := q.Wake(Data); n != 1 {
		t.Fatalf("woken=%d", n)
	}
	<-done
}

func TestWaitCancelOnlyEndsThisWaiter(t *testing.T) {
	t.Parallel()
	q := New()
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	reason, err := q.Wait(ctx, func() (bool, Reason, error) { return false, Explicit, nil })
	if !errors.Is(err, context.Canceled) || reason != Cancel {
		t.Fatalf("reason=%v err=%v", reason, err)
	}

	if n := q.Waiters(); n != 0 {
		t.Fatalf("waiters=%d", n)
	}
	if _, err := q.Wait(t.Context(), func() (bool, Reason, error) { return true, Explicit, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWaitShutdownWakesAllAndRejectsNewWaits(t *testing.T) {
	t.Parallel()
	q := New()
	results := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := q.Wait(t.Context(), func() (bool, Reason, error) { return false, Explicit, nil })
			results <- err
		}()
	}
	waitRegistered(t, q, 3)
	q.Close()
	for range 3 {
		if err := <-results; !errors.Is(err, errno.Cancelled) {
			t.Fatalf("err=%v", err)
		}
	}
	if _, err := q.Wait(t.Context(), func() (bool, Reason, error) { return true, Explicit, nil }); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("wait after close: %v", err)
	}
	q.Close()
}

func TestWaitShutdownReachesWaiterWithPendingWake(t *testing.T) {
	t.Parallel()
	q := New()
	errs := make(chan error, 1)
	go func() {
		_, err := q.Wait(t.Context(), func() (bool, Reason, error) { return false, Explicit, nil })
		errs <- err
	}()
	waitRegistered(t, q, 1)

	if n := q.Wake(Explicit); n != 1 {
		t.Fatalf("wake=%d", n)
	}
	q.Close()
	if err := <-errs; !errors.Is(err, errno.Cancelled) {
		t.Fatalf("err=%v", err)
	}
}

func TestWaitSurvivesSpuriousWakeups(t *testing.T) {
	t.Parallel()
	q := New()
	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := q.Wait(t.Context(), func() (bool, Reason, error) {
			return count.Add(1) >= 3, Data, nil
		})
		if err != nil {
			t.Error(err)
		}
	}()
	waitRegistered(t, q, 1)

	q.Wake(Explicit)
	q.Wake(Explicit)
	count.Store(2)
	q.Wake(Data)
	<-done
}

func TestWakeRotatesFairlyBetweenWaiters(t *testing.T) {
	t.Parallel()
	q := New()
	first := make(chan error, 1)
	second := make(chan error, 1)

	spread := func() func() (bool, Reason, error) {
		var checks atomic.Int32
		return func() (bool, Reason, error) { return checks.Add(1) > 1, Explicit, nil }
	}
	go func() {
		_, err := q.Wait(t.Context(), spread())
		first <- err
	}()
	go func() {
		_, err := q.Wait(t.Context(), spread())
		second <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if n := q.Wake(Explicit); n != 1 {
		t.Fatalf("first wake=%d", n)
	}
	if n := q.Wake(Explicit); n != 1 {
		t.Fatalf("second wake=%d", n)
	}
	<-first
	<-second
}

func TestWaitNoLostWakeupUnderStress(t *testing.T) {
	t.Parallel()
	q := New()
	var mu sync.Mutex
	ready := false
	const waiters = 16
	var wg sync.WaitGroup
	errs := make(chan error, waiters)
	for range waiters {
		wg.Go(func() {
			_, err := q.Wait(t.Context(), func() (bool, Reason, error) {
				mu.Lock()
				defer mu.Unlock()
				return ready, Data, nil
			})
			errs <- err
		})
	}
	waitRegistered(t, q, waiters)
	mu.Lock()
	ready = true
	mu.Unlock()

	if n := q.WakeAll(Data); n != waiters {
		t.Fatalf("woken=%d", n)
	}
	for range waiters {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	wg.Wait()
}

func TestWaitReturnsCheckError(t *testing.T) {
	t.Parallel()
	q := New()
	_, err := q.Wait(t.Context(), func() (bool, Reason, error) {
		return false, Fault, errno.Permission
	})
	if !errors.Is(err, errno.Permission) {
		t.Fatalf("err=%v", err)
	}
}

func TestReasonString(t *testing.T) {
	t.Parallel()

	for reason, want := range map[Reason]string{
		Explicit: "explicit",
		Data:     "data",
		Hangup:   "hangup",
		Fault:    "fault",
		Signal:   "signal",
		Timer:    "timer",
		Shutdown: "shutdown",
		Cancel:   "cancel",
	} {
		if got := reason.String(); got != want {
			t.Fatalf("Reason(%d).String()=%q want %q", reason, got, want)
		}
	}
	if Reason(99).String() != "unknown" {
		t.Fatal("unexpected reason text")
	}
}

func TestWaitRejectsCancelledContextBeforeRegistering(t *testing.T) {
	t.Parallel()
	q := New()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reason, err := q.Wait(ctx, func() (bool, Reason, error) { return true, Data, nil })
	if !errors.Is(err, context.Canceled) || reason != Cancel {
		t.Fatalf("reason=%v err=%v", reason, err)
	}
	if n := q.Waiters(); n != 0 {
		t.Fatalf("取消的等待不得注册: waiters=%d", n)
	}
}

func TestQueueClosedAccessor(t *testing.T) {
	t.Parallel()
	q := New()
	if q.Closed() {
		t.Fatal("新队列不应是关闭状态")
	}
	q.Close()
	if !q.Closed() {
		t.Fatal("Close 后 Closed 必须为真")
	}
}

func TestWaitCheckErrorSurfacesWithoutBlocking(t *testing.T) {
	t.Parallel()
	q := New()

	_, err := q.Wait(t.Context(), func() (bool, Reason, error) {
		return false, Fault, errno.Permission
	})
	if !errors.Is(err, errno.Permission) {
		t.Fatal(err)
	}
}
