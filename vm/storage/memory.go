package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Memory struct {
	mu        sync.Mutex
	sessions  map[string][]byte
	snapshots map[string][]byte
	limit     int
}

const defaultLimit = 64 << 20

func New(limit int) *Memory {
	if limit <= 0 {
		limit = defaultLimit
	}
	return &Memory{
		sessions:  map[string][]byte{},
		snapshots: map[string][]byte{},
		limit:     limit,
	}
}

func (m *Memory) Load(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, errno.Invalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.sessions[name]
	if !ok {
		return nil, errno.NotFound
	}
	return append([]byte(nil), data...), nil
}

func (m *Memory) Save(ctx context.Context, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" {
		return errno.Invalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	total := m.usedLocked() - len(m.sessions[name]) + len(data)
	if total > m.limit {
		return errno.NoSpace
	}
	m.sessions[name] = append([]byte(nil), data...)
	return nil
}

func (m *Memory) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" {
		return errno.Invalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[name]; !ok {
		return errno.NotFound
	}
	delete(m.sessions, name)
	return nil
}

func (m *Memory) SessionNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.sessions))
	for name := range m.sessions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (m *Memory) usedLocked() int {
	total := 0
	for _, data := range m.sessions {
		total += len(data)
	}
	return total
}

func (m *Memory) snapshotSize() int {
	total := 0
	for _, data := range m.snapshots {
		total += len(data)
	}
	return total
}

func (m *Memory) Get(ctx context.Context, digest string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if digest == "" {
		return nil, errno.Invalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.snapshots[digest]
	if !ok {
		return nil, errno.NotFound
	}
	return append([]byte(nil), data...), nil
}

func (m *Memory) Put(ctx context.Context, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if data == nil {
		data = []byte{}
	}
	digest := Digest(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.snapshots[digest]; ok {

		return digest, nil
	}
	if m.snapshotSize()+len(data) > m.limit {
		return "", errno.NoSpace
	}
	m.snapshots[digest] = append([]byte(nil), data...)
	return digest, nil
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
