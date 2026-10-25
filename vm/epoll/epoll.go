// Package epoll 实现 Linux epoll 的内核核心部分(L07/L08):登记表 +
// 等待图与触发模式——水平触发(默认)等待时按当前就绪状态重查全部
// 登记项,仍就绪的项重复报告;边缘触发(Edge)只在登记后的新活动
// (OnChange 通知)产生"边沿"时报告一次,排空后需新活动才再报告;
// 一次性(OneShot)报告一次后禁用登记项,直到 Modify 重新武装。
//
// 事件位掩码由调用层定义(客体 EPOLLIN 等),内核核心只关心
// poll.Readiness 就绪位的交集;错误码与登记生命周期在此管理。
package epoll

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// Flags 是登记项的触发模式。
type Flags uint8

const (
	// LevelTrigger 水平触发:就绪期间每次等待都报告(默认)。
	LevelTrigger Flags = 0
	// EdgeTrigger 边缘触发:仅在登记或重新武装后的新活动(就绪变化
	// 通知)时报告一次;等待不消费就绪状态,读空后不再报告,直到
	// 下一次活动。客体对应 EPOLLET。
	EdgeTrigger Flags = 1 << iota
	// OneShot 一次性:报告一次后禁用登记项(新活动也不报告),直到
	// Modify 重新武装。客体对应 EPOLLONESHOT。
	OneShot
)

// Event 是一次就绪报告:Want 是登记时请求的就绪位,Data 是调用方
// 关联的透传数据(客体 epoll_event.data)。
type Event struct {
	Readiness poll.Readiness
	Data      uint64
}

// item 是一个登记项:目标描述符的就绪源、请求位与透传数据。
type item struct {
	source   poll.Source
	want     poll.Readiness
	data     uint64
	flags    Flags
	cancel   func()
	sequence uint64 // 登记次序,报告按登记顺序稳定

	// edgePending 表示自上次报告后源有新活动(OnChange 通知);
	// 边缘触发据此判定"边沿"。活动回调可能持源自身锁触发,故用
	// 原子量而非实例锁保护。
	edgePending atomic.Bool

	// disabled 是一次性模式报告后的禁用标记(等实例锁)。
	disabled bool
}

// FD 是一个 epoll 实例:持有登记表与等待队列;关闭后全部操作
// 报 EBADF,等待者被唤醒。
type FD struct {
	mu     sync.Mutex
	wake   wait.Queue
	items  map[uint32]*item
	next   uint64
	closed bool
}

// New 创建一个 epoll 实例。
func New() *FD {
	return &FD{items: map[uint32]*item{}}
}

// Add 登记一个描述符:fd 已登记报 EEXIST,source 为空报 EINVAL。
// 登记即订阅就绪变化,变化时唤醒全部等待者;登记时已就绪视为初始
// 边沿(与 Linux ep_insert 登记后立即轮询一致)。
func (f *FD) Add(fd uint32, source poll.Source, want poll.Readiness, data uint64, flags Flags) error {
	if source == nil {
		return errno.Wrap(errno.Invalid, "epoll 登记需要就绪源")
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
	}
	if _, exists := f.items[fd]; exists {
		f.mu.Unlock()
		return errno.Wrap(errno.Exist, "描述符已登记")
	}
	f.next++
	entry := &item{source: source, want: want, data: data, flags: flags, sequence: f.next}
	entry.cancel = source.OnChange(func() {
		entry.edgePending.Store(true)
		f.wake.WakeAll(wait.Data)
	})
	if source.Ready()&want != 0 {
		entry.edgePending.Store(true)
	}
	f.items[fd] = entry
	f.mu.Unlock()
	f.wake.WakeAll(wait.Data)
	return nil
}

// Modify 更新登记项并重新武装:fd 未登记报 ENOENT;一次性模式报告后
// 的禁用被清除,按当前就绪状态重设边沿(与 Linux ep_modify 重新轮询
// 一致),等待者被唤醒以便重新评估。
func (f *FD) Modify(fd uint32, want poll.Readiness, data uint64, flags Flags) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
	}
	entry, ok := f.items[fd]
	if !ok {
		f.mu.Unlock()
		return errno.Wrap(errno.NotFound, "描述符未登记")
	}
	entry.want = want
	entry.data = data
	entry.flags = flags
	entry.disabled = false
	entry.edgePending.Store(entry.source.Ready()&want != 0)
	f.mu.Unlock()
	f.wake.WakeAll(wait.Data)
	return nil
}

// Remove 删除登记项并退订;fd 未登记报 ENOENT。
func (f *FD) Remove(fd uint32) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
	}
	entry, ok := f.items[fd]
	if !ok {
		f.mu.Unlock()
		return errno.Wrap(errno.NotFound, "描述符未登记")
	}
	delete(f.items, fd)
	f.mu.Unlock()
	entry.cancel()
	return nil
}

// reportable 返回该项当前可报告的就绪位;无副作用,可安全用于
// 等待条件探测(探测不能消费边沿或一次性状态)。调用方持有锁。
func (e *item) reportable() poll.Readiness {
	if e.disabled {
		return 0
	}
	matched := e.source.Ready() & e.want
	if matched == 0 {
		return 0
	}
	if e.flags&EdgeTrigger != 0 && !e.edgePending.Load() {
		return 0
	}
	return matched
}

// peekLocked 无副作用地报告是否存在可报告项;等待循环的条件探测
// 用它,避免消费边沿/一次性状态后真正收集时反而取不到。
func (f *FD) peekLocked() bool {
	for _, entry := range f.items {
		if entry.reportable() != 0 {
			return true
		}
	}
	return false
}

// collectLocked 收集并消费当前可报告项:报告消耗边沿(边缘触发),
// 一次性项报告后禁用;达到 maxEvents 即停止——被截断的项保持
// 未消费状态,留给后续调用(边缘触发/一次性通知不因截断丢失)。
// 按登记次序稳定报告;调用方必须持有锁。
func (f *FD) collectLocked(maxEvents int) []Event {
	ordered := make([]*item, 0, len(f.items))
	for _, entry := range f.items {
		ordered = append(ordered, entry)
	}
	slices.SortFunc(ordered, func(a, b *item) int {
		return cmp.Compare(a.sequence, b.sequence)
	})
	var events []Event
	for _, entry := range ordered {
		matched := entry.reportable()
		if matched == 0 {
			continue
		}
		if entry.flags&EdgeTrigger != 0 {
			entry.edgePending.Store(false)
		}
		if entry.flags&OneShot != 0 {
			entry.disabled = true
		}
		events = append(events, Event{Readiness: matched, Data: entry.data})
		if len(events) == maxEvents {
			break
		}
	}
	return events
}

// Wait 报告当前可报告项;全部不可报告时阻塞到变化或上下文结束。
// maxEvents 限定单次报告条数,被截断项的状态不被消费(水平触发
// 项本来就每次重查;边缘触发/一次性项的边沿与禁用留给后续调用)。
// 上下文结束返回 errno.Interrupted。
func (f *FD) Wait(ctx context.Context, maxEvents int) ([]Event, error) {
	if maxEvents <= 0 {
		return nil, errno.Wrap(errno.Invalid, "maxevents 必须为正")
	}
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return nil, errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
		}
		events := f.collectLocked(maxEvents)
		f.mu.Unlock()
		if len(events) > 0 {
			return events, nil
		}
		_, err := f.wake.Wait(ctx, func() (bool, wait.Reason, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.peekLocked() || f.closed, wait.Data, nil
		})
		if err != nil {
			f.mu.Lock()
			closed := f.closed
			f.mu.Unlock()
			if closed {
				return nil, errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
			}
			return nil, errno.Interrupted
		}
	}
}

// ReadyNow 立即返回当前可报告项(0 等待);实例关闭报 EBADF,
// maxEvents 非正返回空(与 Wait 的 EINVAL 校验对齐,不报告)。
func (f *FD) ReadyNow(maxEvents int) ([]Event, error) {
	if maxEvents <= 0 {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
	}
	return f.collectLocked(maxEvents), nil
}

// Close 关闭实例:退订全部登记并唤醒等待者;之后全部操作报 EBADF。
func (f *FD) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
	}
	f.closed = true
	cancels := make([]func(), 0, len(f.items))
	for _, entry := range f.items {
		cancels = append(cancels, entry.cancel)
	}
	f.items = map[uint32]*item{}
	f.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	f.wake.WakeAll(wait.Hangup)
	return nil
}

// Read 实现 fd.Stream:epoll 实例不支持读,报 EINVAL(等待经
// epoll_pwait 完成)。
func (f *FD) Read([]byte) (int, error) { return 0, errno.Invalid }

// Write 实现 fd.Stream:epoll 实例不支持写,报 EINVAL。
func (f *FD) Write([]byte) (int, error) { return 0, errno.Invalid }
