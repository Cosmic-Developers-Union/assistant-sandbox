package poll

import (
	"context"
	"sync/atomic"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

type Readiness uint8

const (
	Readable Readiness = 1 << iota
	Writable
	Hangup
	Error
)

func (r Readiness) String() string {
	s := ""
	if r&Readable != 0 {
		s += "readable|"
	}
	if r&Writable != 0 {
		s += "writable|"
	}
	if r&Hangup != 0 {
		s += "hangup|"
	}
	if r&Error != 0 {
		s += "error|"
	}
	if s == "" {
		return "none"
	}
	return s[:len(s)-1]
}

type Source interface {
	Ready() Readiness
	OnChange(func()) (cancel func())
}

type Entry struct {
	Source Source
	Want   Readiness
}

type Result struct {
	Index    int
	Ready    Readiness
	Received Readiness
}

func Immediate(entries []Entry) []Result {
	var results []Result
	for i, entry := range entries {
		ready := entry.Source.Ready()
		if ready&entry.Want != 0 {
			results = append(results, Result{Index: i, Ready: entry.Want, Received: ready})
		}
	}
	return results
}

// Sleeper 是超时等待所需的最小能力:按纳秒等待,可被上下文取消。
type Sleeper interface {
	Sleep(ctx context.Context, nanoseconds uint64) error
}

func Wait(ctx context.Context, entries []Entry) ([]Result, error) {
	return WaitFor(ctx, nil, entries, PollInfinite)
}

// PollInfinite 是无限等待的哨兵超时值;0 表示立即返回。
const PollInfinite = ^uint64(0)

func WaitFor(ctx context.Context, clk Sleeper, entries []Entry, timeoutNanos uint64) ([]Result, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if results := Immediate(entries); len(results) > 0 {
		return results, nil
	}
	if timeoutNanos == 0 {
		// 零超时=立即返回(不等待):poll/ppoll 的 timeout=0 语义。
		return nil, nil
	}
	wake := wait.New()
	cancels := make([]func(), 0, len(entries))
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	notify := func() { wake.Wake(wait.Data) }
	for _, entry := range entries {
		cancels = append(cancels, entry.Source.OnChange(notify))
	}

	if results := Immediate(entries); len(results) > 0 {
		return results, nil
	}
	var timedOut atomic.Bool
	if clk != nil && timeoutNanos > 0 && timeoutNanos != PollInfinite {
		timerCtx, cancelTimer := context.WithCancel(ctx)
		defer cancelTimer()
		go func() {
			if err := clk.Sleep(timerCtx, timeoutNanos); err == nil {
				timedOut.Store(true)
			}
			wake.Wake(wait.Timer)
		}()
	}
	var reason wait.Reason
	for {
		_, err := wake.Wait(ctx, func() (bool, wait.Reason, error) {
			if len(Immediate(entries)) > 0 {
				reason = wait.Data
				return true, wait.Data, nil
			}
			if timedOut.Load() {
				reason = wait.Timer
				return true, wait.Timer, nil
			}
			return false, wait.Data, nil
		})
		if err != nil {
			return nil, err
		}
		if reason == wait.Timer {
			return nil, errno.TimedOut
		}
		if results := Immediate(entries); len(results) > 0 {
			return results, nil
		}

	}
}
