package audit

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Memory struct {
	mu       sync.Mutex
	next     uint64
	events   []Event
	limit    int
	failNext error
	subs     []*subscriber
}

type subscriber struct {
	mu     sync.Mutex
	queue  []Event
	limit  int
	gap    uint64
	closed bool
}

const (
	defaultEventLimit = 4096
	defaultQueueLimit = 256
)

func NewMemory() *Memory {
	return &Memory{limit: defaultEventLimit, next: 1}
}

func (m *Memory) Append(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.failNext != nil {
		err := m.failNext
		m.failNext = nil
		m.mu.Unlock()
		return err
	}
	if len(m.events) >= m.limit {
		m.mu.Unlock()
		return errno.NoSpace
	}
	event.Sequence = m.next
	m.next++
	if event.Payload != nil {
		event.Payload = append([]byte(nil), event.Payload...)
	}
	m.events = append(m.events, event)
	subs := make([]*subscriber, len(m.subs))
	copy(subs, m.subs)
	m.mu.Unlock()
	for _, s := range subs {
		s.deliver(event)
	}
	return nil
}

func (m *Memory) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

func (m *Memory) FailNext(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext = err
}

func (m *Memory) Subscribe() *Subscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &subscriber{limit: defaultQueueLimit}
	m.subs = append(m.subs, s)
	return &Subscription{m: m, s: s}
}

type Subscription struct {
	m *Memory
	s *subscriber
}

func (sub *Subscription) Drain() (events []Event, gap uint64) {
	sub.s.mu.Lock()
	defer sub.s.mu.Unlock()
	events = sub.s.queue
	sub.s.queue = nil
	gap = sub.s.gap
	sub.s.gap = 0
	return events, gap
}

func (sub *Subscription) Close() {
	sub.m.mu.Lock()
	defer sub.m.mu.Unlock()
	for i, s := range sub.m.subs {
		if s == sub.s {
			sub.m.subs = append(sub.m.subs[:i], sub.m.subs[i+1:]...)
			return
		}
	}
}

func (s *subscriber) deliver(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if len(s.queue) >= s.limit {

		s.queue = s.queue[1:]
		s.gap++
	}
	s.queue = append(s.queue, event)
}
