package vm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestRunQueueSingleProcessSliceIsImmediate 验证 H02:没有其他等待者时,
// 让出执行权立即拿回继续执行,单进程执行不产生额外等待。
func TestRunQueueSingleProcessSliceIsImmediate(t *testing.T) {
	q := newRunQueue(1)
	ctx := context.Background()
	if err := q.enter(ctx); err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		if err := q.slice(ctx); err != nil {
			t.Fatal(err)
		}
	}
	q.leave()
	q.leave() // 幂等:重复归还静默忽略
}

// TestRunQueueMutualExclusionAndHandoff 验证 H02:执行权互斥——同一
// 时刻至多一个持有者;让出后排队者获得执行权,两个"进程"交替推进。
func TestRunQueueMutualExclusionAndHandoff(t *testing.T) {
	q := newRunQueue(1)
	ctx := context.Background()
	if err := q.enter(ctx); err != nil {
		t.Fatal(err)
	}
	var holders atomic.Int64
	var violations atomic.Int64
	var wg sync.WaitGroup
	turns := 200
	started := sync.NewCond(&sync.Mutex{})
	ready := 0
	for worker := 1; worker <= 3; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 全部 worker 排队后才归还令牌,确保每次让出都有等待者。
			started.L.Lock()
			ready++
			started.Signal()
			started.L.Unlock()
			if err := q.enter(ctx); err != nil {
				t.Error(err)
				return
			}
			for range turns {
				current := holders.Add(1)
				if current > 1 {
					violations.Add(1)
				}
				holders.Add(-1)
				if err := q.slice(ctx); err != nil {
					t.Error(err)
					return
				}
			}
			q.leave()
		}()
	}
	started.L.Lock()
	for ready < 3 {
		started.Wait()
	}
	started.L.Unlock()
	q.leave() // 三个 worker 均已排队,主协程归还令牌
	wg.Wait()
	if violations.Load() != 0 {
		t.Fatalf("执行权互斥被破坏 %d 次", violations.Load())
	}
}

// TestRunQueueBlockedDoesNotOccupy 验证 H03:归还执行权(进入长期阻塞)
// 后,其他排队者立即获得执行权,不被阻塞者挡住。
func TestRunQueueBlockedDoesNotOccupy(t *testing.T) {
	q := newRunQueue(1)
	ctx := context.Background()
	if err := q.enter(ctx); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		if err := q.enter(ctx); err != nil {
			t.Error(err)
			return
		}
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("执行权被持有时不得被他人获取")
	case <-time.After(20 * time.Millisecond):
	}
	q.leave() // 进入长期阻塞前归还
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("归还后排队者未获得执行权")
	}
}

// TestRunQueueCloseWakesWaiters 验证 H02:队列关闭唤醒全部排队等待者,
// 此后 enter 一律失败。
func TestRunQueueCloseWakesWaiters(t *testing.T) {
	q := newRunQueue(1)
	ctx := context.Background()
	if err := q.enter(ctx); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			errs <- q.enter(ctx)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	q.close()
	for range 2 {
		select {
		case err := <-errs:
			if !errors.Is(err, errno.Cancelled) {
				t.Fatalf("关闭后应报 ECANCELED: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("关闭未唤醒排队等待者")
		}
	}
	if err := q.enter(ctx); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后的 enter: %v", err)
	}
}

// TestRunQueueMultiCoreConcurrency 验证令牌数等于客体 CPU 数:4 核
// 可同时持有 4 个执行权,第 5 个必须等待归还。
func TestRunQueueMultiCoreConcurrency(t *testing.T) {
	q := newRunQueue(4)
	ctx := context.Background()
	acquired := make(chan struct{}, 5)
	for range 4 {
		go func() {
			if err := q.enter(ctx); err != nil {
				t.Error(err)
				return
			}
			acquired <- struct{}{}
		}()
	}
	for range 4 {
		select {
		case <-acquired:
		case <-time.After(2 * time.Second):
			t.Fatal("4 核应允许 4 个执行者并发")
		}
	}
	go func() {
		if err := q.enter(ctx); err != nil {
			t.Error(err)
			return
		}
		acquired <- struct{}{}
	}()
	select {
	case <-acquired:
		t.Fatal("5 个执行者不应同时持有 4 核令牌")
	case <-time.After(20 * time.Millisecond):
	}
	q.leave()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("归还令牌后等待者未获得执行权")
	}
	for range 5 {
		q.leave()
	}
}
