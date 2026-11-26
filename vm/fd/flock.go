package fd

import (
	"context"
	"math/bits"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const (
	flockShared    = 0x1
	flockExclusive = 0x2
	flockNoBlock   = 0x4
	flockUnlock    = 0x8
	flockValid     = flockShared | flockExclusive | flockNoBlock | flockUnlock
)

// Flock 对打开文件描述施加 flock(2) 锁。锁随打开文件描述共享:dup 与
// fork 后仍指向同一 reference;最后一个引用关闭时释放。冲突的非阻塞
// 请求返回 EWOULDBLOCK,阻塞请求等待解锁或上下文取消。
func (t *Table) Flock(ctx context.Context, pid uint64, id uint32, operation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if operation&^uint64(flockValid) != 0 ||
		bits.OnesCount64(operation&(flockShared|flockExclusive|flockUnlock)) != 1 {
		return errno.Invalid
	}
	t.mu.Lock()
	for {
		table := t.entries[pid]
		if table == nil {
			t.mu.Unlock()
			return errno.NoProcess
		}
		item := table[id]
		if item == nil {
			t.mu.Unlock()
			return errno.BadHandle
		}
		reference := item.ref
		if operation&flockUnlock != 0 {
			if _, locked := t.flocks[reference]; locked {
				delete(t.flocks, reference)
				t.notifyFlockLocked()
			}
			t.mu.Unlock()
			return nil
		}
		mode := uint32(operation) & (flockShared | flockExclusive)
		conflict := false
		for other, held := range t.flocks {
			if other != reference && (mode == flockExclusive || held == flockExclusive) {
				conflict = true
				break
			}
		}
		if !conflict {
			t.flocks[reference] = mode
			t.mu.Unlock()
			return nil
		}
		if operation&flockNoBlock != 0 {
			t.mu.Unlock()
			return errno.WouldBlock
		}
		wait := t.flockCh
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		}
		t.mu.Lock()
	}
}

func (t *Table) notifyFlockLocked() {
	close(t.flockCh)
	t.flockCh = make(chan struct{})
}

func (t *Table) releaseFlockLocked(reference *reference) {
	if reference.refs != 0 {
		return
	}
	if _, locked := t.flocks[reference]; locked {
		delete(t.flocks, reference)
		t.notifyFlockLocked()
	}
}

func (t *Table) releaseEntryLocked(item *entry) error {
	err := item.ref.release()
	t.releaseFlockLocked(item.ref)
	return err
}
