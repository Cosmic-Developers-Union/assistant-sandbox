package scheduler

import (
	"sync"
)

const (
	KindSpawn  = "spawn"
	KindExit   = "exit"
	KindSignal = "signal"
	KindReap   = "reap"
	KindClose  = "close"

	KindExec = "exec"

	KindSnapshot = "snapshot"
	KindRestore  = "restore"

	KindMemory = "memory"
)

type Event struct {
	Seq    uint64
	PID    uint64
	Kind   string
	Detail string
}

type Sequencer struct {
	mu    sync.Mutex
	next  uint64
	log   []Event
	limit int
}

func New(limit int) *Sequencer {
	if limit <= 0 {
		limit = 65536
	}
	return &Sequencer{limit: limit}
}

func (s *Sequencer) Append(pid uint64, kind, detail string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.log) >= s.limit {
		return 0
	}
	s.next++
	s.log = append(s.log, Event{Seq: s.next, PID: pid, Kind: kind, Detail: detail})
	return s.next
}

func (s *Sequencer) Log() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.log))
	copy(out, s.log)
	return out
}

func (s *Sequencer) Full() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.log) >= s.limit
}

func Identical(a, b []Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].PID != b[i].PID || a[i].Kind != b[i].Kind || a[i].Detail != b[i].Detail {
			return false
		}
	}
	return true
}
