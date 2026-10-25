// Package trace 实现受限的系统调用追踪(G08):记录 PID/TID、调用编号、
// 返回结果与耗时,不记录任何缓冲区内容;记录条数有界,超出后丢弃最旧
// 条目并计数,避免长时间运行的 VM 无界占用宿主内存。
package trace

import (
	"sync"
)

// Duration 是以纳秒计的耗时。客体核心不引入 time 包(见
// vm/wasm_test.go 的资源旁路守卫),耗时类型在此自定义。
type Duration uint64

// Entry 是一次系统调用轨迹。字段刻意只包含标识、调用、结果与耗时:
// 路径、参数与缓冲区内容一律不落盘,避免追踪泄露客体数据。
type Entry struct {
	PID uint64
	TID uint64

	// Number 是客体的系统调用编号(asm-generic)。
	Number uint64

	// Result 是返回值;失败时为 -errno。
	Result int64

	// Duration 是本次调用的耗时(纳秒)。
	Duration Duration
}

// DefaultLimit 是默认保留的轨迹条数。
const DefaultLimit = 1024

// Recorder 是有界的轨迹记录器:只保留最近 limit 条,更早的条目被
// 丢弃并计数。零值 Recorder 不可用,请用 New 创建。
type Recorder struct {
	mu      sync.Mutex
	entries []Entry
	limit   int
	dropped uint64
}

// New 创建保留 limit 条轨迹的记录器;limit 非正时使用默认值。
func New(limit int) *Recorder {
	if limit <= 0 {
		limit = DefaultLimit
	}
	return &Recorder{limit: limit, entries: make([]Entry, 0, min(limit, 64))}
}

// Limit 返回当前保留上限。
func (r *Recorder) Limit() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.limit
}

// SetLimit 调整保留上限:缩小时丢弃最旧的条目以满足新上限。
func (r *Recorder) SetLimit(limit int) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limit = limit
	r.trimLocked()
}

// Record 记录一条轨迹;条目超出上限时丢弃最旧的一条并计数。
func (r *Recorder) Record(entry Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	r.trimLocked()
}

// trimLocked 按上限裁剪;调用方必须已持有锁。
func (r *Recorder) trimLocked() {
	if len(r.entries) <= r.limit {
		return
	}
	excess := len(r.entries) - r.limit
	r.entries = append(r.entries[:0], r.entries[excess:]...)
	r.dropped += uint64(excess)
}

// Entries 返回当前保留的轨迹(按时间顺序,最旧在前)。
func (r *Recorder) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

// Dropped 返回因超出上限而丢弃的条目数。
func (r *Recorder) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Len 返回当前保留的条目数。
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}
