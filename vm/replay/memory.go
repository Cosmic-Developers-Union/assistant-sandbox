package replay

import (
	"bytes"
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const (
	OpEntropy    = "entropy.read"
	OpStdin      = "stdin.read"
	OpBridgeCall = "bridge.call"

	OpClockRead = "clock.read"

	OpClockSleep = "clock.sleep"

	OpSchedule = "schedule.control"
)

type Memory struct {
	mu     sync.Mutex
	facts  []Fact
	pos    int
	record bool

	header *Header
}

func Record() *Memory { return &Memory{record: true} }

func Replay(facts []Fact) *Memory { return &Memory{facts: facts} }

func (m *Memory) Facts() []Fact {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Fact, len(m.facts))
	copy(out, m.facts)
	return out
}

func (m *Memory) Record(ctx context.Context, fact Fact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.record {
		return errno.NotSupported
	}
	fact.Sequence = uint64(len(m.facts) + 1)
	fact.Input = cloneBytes(fact.Input)
	fact.Output = cloneBytes(fact.Output)
	m.facts = append(m.facts, fact)
	return nil
}

func (m *Memory) Next(ctx context.Context, expect Fact) (Fact, error) {
	if err := ctx.Err(); err != nil {
		return Fact{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record {
		return Fact{}, errno.NotSupported
	}
	if m.pos >= len(m.facts) {
		return Fact{}, errno.NotFound
	}
	fact := m.facts[m.pos]
	if fact.PID != expect.PID || fact.Operation != expect.Operation {
		return Fact{}, &MismatchError{
			Position: m.pos, Expected: expect, Actual: fact,
			Reason: "顺序或进程不一致",
		}
	}
	if !bytes.Equal(fact.Input, expect.Input) {
		return Fact{}, &MismatchError{
			Position: m.pos, Expected: expect, Actual: fact,
			Reason: "输入参数不一致",
		}
	}
	m.pos++
	return fact, nil
}

func (m *Memory) Position() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pos
}

func (m *Memory) Total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.facts)
}

func (m *Memory) Remaining() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.facts) - m.pos
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}
