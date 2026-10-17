package poll

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type fakeSource struct {
	mu       sync.Mutex
	ready    Readiness
	watchers []*watcher
}

type watcher struct{ fn func() }

func (f *fakeSource) Ready() Readiness {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeSource) OnChange(fn func()) (cancel func()) {
	w := &watcher{fn: fn}
	f.mu.Lock()
	f.watchers = append(f.watchers, w)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, registered := range f.watchers {
			if registered == w {
				f.watchers = append(f.watchers[:i], f.watchers[i+1:]...)
				return
			}
		}
	}
}

func (f *fakeSource) set(ready Readiness) {
	f.mu.Lock()
	f.ready = ready
	f.mu.Unlock()
}

func (f *fakeSource) fire() {
	f.mu.Lock()
	watchers := make([]*watcher, len(f.watchers))
	copy(watchers, f.watchers)
	f.mu.Unlock()
	for _, w := range watchers {
		w.fn()
	}
}

func TestReadinessString(t *testing.T) {
	if Readable.String() != "readable" {
		t.Fatal(Readable.String())
	}
	if (Readable | Writable | Hangup | Error).String() != "readable|writable|hangup|error" {
		t.Fatal("组合位错误")
	}
	if Readiness(0).String() != "none" {
		t.Fatal("空位应为 none")
	}
}

func TestImmediateSelectsSatisfiedEntries(t *testing.T) {
	ready := &fakeSource{ready: Readable}
	blocked := &fakeSource{ready: 0}
	results := Immediate([]Entry{
		{Source: ready, Want: Readable},
		{Source: blocked, Want: Writable},
	})
	if len(results) != 1 || results[0].Index != 0 || results[0].Received != Readable {
		t.Fatalf("results=%+v", results)
	}

	if results := Immediate(nil); results != nil {
		t.Fatal(results)
	}
}

func TestWaitBlocksUntilChange(t *testing.T) {
	source := &fakeSource{ready: 0}
	results := make(chan []Result, 1)
	go func() {
		res, err := Wait(context.Background(), []Entry{{Source: source, Want: Readable}})
		if err != nil {
			t.Error(err)
		}
		results <- res
	}()
	time.Sleep(20 * time.Millisecond)
	source.set(Readable)
	source.fire()
	select {
	case res := <-results:
		if len(res) != 1 || res[0].Index != 0 {
			t.Fatalf("res=%+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("状态变化未唤醒 Wait")
	}
}

func TestWaitForTimesOutViaVirtualClock(t *testing.T) {
	source := &fakeSource{ready: 0}
	var clk clock.Virtual
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = clk.Advance(100)
	}()
	if _, err := WaitFor(ctx, &clk, []Entry{{Source: source, Want: Readable}}, 50); !errors.Is(err, errno.TimedOut) {
		t.Fatalf("err=%v", err)
	}

	source.set(Readable)
	results, err := WaitFor(ctx, &clk, []Entry{{Source: source, Want: Readable}}, 50)
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}

func TestWaitForWithoutClockBlocksUntilReady(t *testing.T) {
	source := &fakeSource{ready: 0}
	results := make(chan []Result, 1)
	go func() {
		res, err := WaitFor(context.Background(), nil, []Entry{{Source: source, Want: Writable}}, 0)
		if err != nil {
			t.Error(err)
		}
		results <- res
	}()
	time.Sleep(20 * time.Millisecond)
	source.set(Writable)
	source.fire()
	select {
	case res := <-results:
		if len(res) != 1 {
			t.Fatalf("res=%+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("无时钟超时应无限等待就绪")
	}
}

func TestWaitRejectsCancelAndEmptyEntries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Wait(ctx, []Entry{{Source: &fakeSource{}, Want: Readable}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if results, err := Wait(context.Background(), nil); results != nil || err != nil {
		t.Fatalf("results=%v err=%v", results, err)
	}
}

func TestWaitHonorsRegistrationBeforeCheck(t *testing.T) {

	source := &fakeSource{ready: 0}
	results := make(chan []Result, 1)
	go func() {
		res, err := Wait(context.Background(), []Entry{{Source: source, Want: Readable}})
		if err != nil {
			t.Error(err)
		}
		results <- res
	}()
	time.Sleep(20 * time.Millisecond)
	source.set(Readable)
	source.fire()
	source.set(0)
	source.fire()
	source.set(Readable)
	source.fire()
	select {
	case res := <-results:
		if len(res) != 1 {
			t.Fatalf("res=%+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("注册期间的状态变化被丢失")
	}
}
