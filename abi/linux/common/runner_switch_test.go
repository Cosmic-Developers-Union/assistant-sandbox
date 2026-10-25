package common

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
)

// countingCore 是一个只计步的执行端口:每步返回继续,达到步数上限
// 后取消上下文(模拟纯 CPU 任务的执行边界)。
type countingCore struct {
	steps  uint64
	limit  uint64
	cancel context.CancelFunc
}

func (c *countingCore) Reset(context.Context, cpu.Machine) error { return nil }

func (c *countingCore) Step(context.Context) (cpu.Outcome, error) {
	c.steps++
	if c.steps >= c.limit {
		c.cancel()
	}
	return cpu.Continued, nil
}

func (c *countingCore) State(context.Context) (cpu.Machine, error) {
	return cpu.Machine{Registers: make([]uint64, 32), Floats: make([]uint64, 32)}, nil
}

func (c *countingCore) WordSize() cpu.WordSize {
	return cpu.WordSize{Bits: 64, Bytes: 8, LittleEndian: true}
}

// rotatingSwitcher 在时间片用尽时轮换任务,并记录每个任务的推进步数。
type rotatingSwitcher struct {
	slice   uint64
	used    uint64
	current int
	counts  [2]uint64
}

func (s *rotatingSwitcher) Tick() bool {
	s.used++
	if s.used < s.slice {
		return false
	}
	s.used = 0
	return true
}

func (s *rotatingSwitcher) Switch(context.Context) (bool, error) {
	s.counts[s.current]++
	s.current = (s.current + 1) % len(s.counts)
	return true, nil
}

// noTrapDispatcher 不处理任何 trap:计步核心不会产生 trap。
type noTrapDispatcher struct{}

func (noTrapDispatcher) Dispatch(context.Context, cpu.Trap) (Exit, error) {
	return Exit{}, nil
}

// TestRunnerSwitchesTasksOnTimeSlice 验证 D11:时间片用尽后执行体切换
// 到另一个任务——纯 CPU 任务不会独占核心,其他任务继续前进。
func TestRunnerSwitchesTasksOnTimeSlice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	switcher := &rotatingSwitcher{slice: 4}
	core := &countingCore{limit: 40, cancel: cancel}
	runner, err := NewRunner(Config{
		Core:       core,
		Dispatcher: noTrapDispatcher{},
		Switcher:   switcher,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx); err == nil {
		t.Fatal("执行结束应报告取消")
	}
	// 纯 CPU 任务没有独占核心:两个任务都被推进。
	if switcher.counts[0] == 0 || switcher.counts[1] == 0 {
		t.Fatalf("任务未轮转推进: %+v", switcher.counts)
	}
	if core.steps < 40 {
		t.Fatalf("执行步数=%d want ≥40", core.steps)
	}
}
