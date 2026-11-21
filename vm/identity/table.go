package identity

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// Table 是每进程身份表(O01):默认身份用于未登记的进程,Set 修改
// 当前进程的真实/有效 UID/GID,Inherit 在 fork/exec 边界复制身份。
// 表与宿主身份完全解耦:UID 0 只是客体命名空间中的值。
type Table struct {
	mu       sync.Mutex
	entries  map[uint64]Identity
	fallback Identity
}

func NewTable(fallback Identity) *Table {
	return &Table{entries: map[uint64]Identity{}, fallback: fallback}
}

func (t *Table) Lookup(ctx context.Context, pid uint64) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if pid == 0 {
		return Identity{}, errno.NoProcess
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if identity, ok := t.entries[pid]; ok {
		return identity, nil
	}
	return t.fallback, nil
}

func (t *Table) Set(pid uint64, value Identity) error {
	if pid == 0 {
		return errno.NoProcess
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries[pid] = value
	return nil
}

// Inherit 把 parent 的当前身份复制到 child;parent 未登记时使用默认
// 身份,与 fork 后子进程继承凭证的语义一致。
func (t *Table) Inherit(parent, child uint64) error {
	if child == 0 {
		return errno.NoProcess
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	identity := t.fallback
	if parent != 0 {
		if value, ok := t.entries[parent]; ok {
			identity = value
		}
	}
	t.entries[child] = identity
	return nil
}

func (t *Table) Remove(pid uint64) {
	t.mu.Lock()
	delete(t.entries, pid)
	t.mu.Unlock()
}
