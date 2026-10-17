package entropy

import (
	"context"
	"encoding/binary"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

const Algorithm = "splitmix64-v1"

type Seeded struct {
	mu    sync.Mutex
	state uint64
	block [8]byte
	pos   int
}

func NewSeeded(seed uint64) *Seeded {
	return &Seeded{state: seed}
}

func splitmix64(state *uint64) uint64 {
	*state += 0x9E3779B97F4A7C15
	z := *state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (s *Seeded) refill() {
	binary.LittleEndian.PutUint64(s.block[:], splitmix64(&s.state))
	s.pos = 0
}

func (s *Seeded) Read(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range p {
		if s.pos >= len(s.block) {
			s.refill()
		}
		p[i] = s.block[s.pos]
		s.pos++
	}
	return len(p), nil
}

type Recording struct {
	source  Source
	journal *replay.Memory
	pid     uint64
}

func NewRecording(source Source, journal *replay.Memory, pid uint64) *Recording {
	return &Recording{source: source, journal: journal, pid: pid}
}

func (r *Recording) Read(ctx context.Context, p []byte) (int, error) {
	request := []byte{byte(len(p) >> 8), byte(len(p))}
	output, err := replay.Run(ctx, r.journal, r.pid, replay.OpEntropy, request, func(ctx context.Context) ([]byte, string, error) {
		buf := make([]byte, len(p))
		n, readErr := r.source.Read(ctx, buf)
		if readErr != nil {
			return nil, "read", readErr
		}
		return buf[:n], "", nil
	})
	if err != nil {
		return 0, err
	}
	n := copy(p, output)
	if n != len(p) {
		return n, errno.Range
	}
	return n, nil
}
