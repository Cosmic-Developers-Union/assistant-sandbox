// Package locks 实现 POSIX 字节范围文件锁的内核核心(I14):按
// (inode, 范围) 记录持锁者(进程),读锁共享、写锁互斥;同进程
// 范围合并/拆分;进程退出释放全部锁并唤醒阻塞的 SETLKW 等待者。
package locks

import (
	"context"
	"sort"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// 锁类型:与 F_RDLCK/F_WRLCK/F_UNLCK 同值。
const (
	Read  = 0
	Write = 1
	// UnLock 在 Set 调用中表示解锁。
	UnLock = 2
)

// Range 是一段字节范围锁:[Start, End);End == 0 表示至文件尾(EOF)。
type Range struct {
	Start uint64
	End   uint64 // 0 = EOF
}

// lock 是一条持锁记录:holder 为进程号。
type lock struct {
	holder uint64
	typ    int
	Range
}

// Key 是锁对象标识(通常为文件 inode 号)。
type Key uint64

// Table 是文件锁核心:按对象记录锁段;关闭后阻塞等待以错误放行。
type Table struct {
	mu     sync.Mutex
	locks  map[Key][]lock
	closed bool
	wake   wait.Queue
}

// New 创建锁表。
func New() *Table {
	return &Table{locks: map[Key][]lock{}}
}

// Close 关闭锁表:唤醒全部阻塞等待者(以取消错误)。
func (t *Table) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	t.wake.Close()
}

func overlaps(a, b Range) bool {
	if a.End != 0 && a.End <= b.Start {
		return false
	}
	if b.End != 0 && b.End <= a.Start {
		return false
	}
	return true
}

// conflict 返回与 (holder, rng, typ) 冲突的锁:不同持有者的写锁冲突
// 任意请求;不同持有者的读锁与写锁冲突。
func conflict(locks []lock, holder uint64, rng Range, typ int) *lock {
	for index := range locks {
		existing := &locks[index]
		if existing.holder == holder {
			continue
		}
		if !overlaps(existing.Range, rng) {
			continue
		}
		if typ == Write || existing.typ == Write {
			return existing
		}
	}
	return nil
}

// Test 返回与请求冲突的第一条锁(供 F_GETLK);无冲突时 ok=false。
func (t *Table) Test(key Key, holder uint64, rng Range, typ int) (Range, int, uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if typ == UnLock {
		return Range{}, 0, 0, false
	}
	if existing := conflict(t.locks[key], holder, rng, typ); existing != nil {
		return existing.Range, existing.typ, existing.holder, true
	}
	return Range{}, 0, 0, false
}

// Set 应用锁请求:UnLock 释放;Read/Write 在无冲突时登记(同进程
// 相邻/重叠段合并);冲突时返回 EAGAIN(F_SETLKW 由调用方经
// WaitConflict 阻塞重试)。
func (t *Table) Set(key Key, holder uint64, rng Range, typ int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errno.Cancelled
	}
	if typ == UnLock {
		t.releaseLocked(key, holder, rng)
		return nil
	}
	if conflict(t.locks[key], holder, rng, typ) != nil {
		return errno.WouldBlock
	}
	t.applyLocked(key, holder, rng, typ)
	return nil
}

func (t *Table) applyLocked(key Key, holder uint64, rng Range, typ int) {
	locks := t.locks[key]
	// 先移除本持有者与请求重叠的段(后统一并入)。
	kept := make([]lock, 0, len(locks)+1)
	for _, existing := range locks {
		if existing.holder == holder && overlaps(existing.Range, rng) {
			if existing.typ == typ {
				// 同类型:并入请求范围。
				if existing.Start < rng.Start {
					rng.Start = existing.Start
				}
				if existing.End == 0 || rng.End == 0 {
					rng.End = 0
				} else if existing.End > rng.End {
					rng.End = existing.End
				}
			}
			continue // 不同类型(升级/降级)时旧段移除,新段覆盖
		}
		kept = append(kept, existing)
	}
	kept = append(kept, lock{holder: holder, typ: typ, Range: rng})
	locks = kept // gocritic: kept 是新切片
	// 归一化:同持有者同类型重叠/相邻合并,按起点排序。
	sort.Slice(locks, func(i, j int) bool {
		if locks[i].holder != locks[j].holder {
			return locks[i].holder < locks[j].holder
		}
		return locks[i].Start < locks[j].Start
	})
	merged := make([]lock, 0, len(locks))
	for _, l := range locks {
		if n := len(merged); n > 0 {
			last := &merged[n-1]
			if last.holder == l.holder && last.typ == l.typ && overlaps(last.Range, l.Range) {
				if l.End == 0 || last.End == 0 {
					last.End = 0
				} else if l.End > last.End {
					last.End = l.End
				}
				continue
			}
		}
		merged = append(merged, l)
	}
	t.locks[key] = merged
}

func (t *Table) releaseLocked(key Key, holder uint64, rng Range) {
	locks := t.locks[key]
	out := make([]lock, 0, len(locks)+2)
	for _, l := range locks {
		if l.holder != holder || !overlaps(l.Range, rng) {
			out = append(out, l)
			continue
		}
		// 拆分:保留未覆盖的前后段。
		if l.Start < rng.Start {
			out = append(out, lock{holder: holder, typ: l.typ, Range: Range{Start: l.Start, End: rng.Start}})
		}
		if l.End == 0 {
			// 尾段被部分解锁(有限范围):拆出 [rng.End, EOF)。
			if rng.End != 0 && rng.End > l.Start {
				out = append(out, lock{holder: holder, typ: l.typ, Range: Range{Start: rng.End}})
			}
			continue
		}
		if rng.End != 0 && rng.End < l.End {
			if rng.End > l.Start {
				out = append(out, lock{holder: holder, typ: l.typ, Range: Range{Start: rng.End, End: l.End}})
			}
		}
	}
	if len(out) == 0 {
		delete(t.locks, key)
		return
	}
	t.locks[key] = out
}

// ReleaseHolder 释放进程持有的全部锁并唤醒阻塞等待者(进程退出)。
func (t *Table) ReleaseHolder(holder uint64) {
	t.mu.Lock()
	for key, locks := range t.locks {
		kept := locks[:0]
		changed := false
		for _, l := range locks {
			if l.holder == holder {
				changed = true
				continue
			}
			kept = append(kept, l)
		}
		if changed {
			if len(kept) == 0 {
				delete(t.locks, key)
			} else {
				t.locks[key] = kept
			}
		}
	}
	count := len(t.locks)
	t.mu.Unlock()
	if count > 0 || true {
		t.wake.WakeAll(wait.Data)
	}
}

// WaitConflict 阻塞等待一次唤醒(F_SETLKW 的重试节拍):锁释放或
// 上下文结束返回。
func (t *Table) WaitConflict(ctx context.Context) error {
	_, err := t.wake.Wait(ctx, func() (bool, wait.Reason, error) {
		return false, wait.Data, nil
	})
	return err
}
