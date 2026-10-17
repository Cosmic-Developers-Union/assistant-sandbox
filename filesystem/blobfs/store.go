package blobfs

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/digest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Store interface {
	Put(ctx context.Context, data []byte) (string, error)

	Open(ctx context.Context, identity string) (io.ReadCloser, error)

	Has(ctx context.Context, identity string) (bool, error)

	Account(ctx context.Context) (Account, error)
}

type Account struct {
	Blobs int
	Bytes uint64
	Limit uint64
}

type Memory struct {
	mu    sync.Mutex
	blobs map[string][]byte
	bytes uint64
	limit uint64
}

func NewMemory(limitBytes uint64) *Memory {
	return &Memory{blobs: map[string][]byte{}, limit: limitBytes}
}

func (m *Memory) Put(ctx context.Context, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	identity := digest.Content(data)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blobs[identity]; ok {
		return identity, nil
	}
	size := uint64(len(data))

	if m.limit != 0 && size > m.limit-m.bytes {
		return "", errno.Wrap(errno.Limit, "Blob 存储预算已耗尽")
	}
	m.blobs[identity] = append([]byte(nil), data...)
	m.bytes += size
	return identity, nil
}

func (m *Memory) Open(ctx context.Context, identity string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if identity == "" {
		return nil, errno.Wrap(errno.Invalid, "内容身份不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.blobs[identity]
	if !ok {
		return nil, errno.Wrap(errno.NotFound, "内容不存在: "+identity)
	}
	if computed := digest.Content(stored); computed != identity {
		return nil, errno.Wrap(errno.Version, "内容身份不符: "+identity)
	}

	return io.NopCloser(bytes.NewReader(append([]byte(nil), stored...))), nil
}

func (m *Memory) Has(ctx context.Context, identity string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.blobs[identity]
	return ok, nil
}

func (m *Memory) Account(ctx context.Context) (Account, error) {
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return Account{Blobs: len(m.blobs), Bytes: m.bytes, Limit: m.limit}, nil
}

var _ Store = (*Memory)(nil)
