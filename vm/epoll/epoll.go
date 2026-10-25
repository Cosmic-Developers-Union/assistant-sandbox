// Package epoll 实现 Linux epoll 的内核核心部分(L07):登记表 +
// 等待图,水平触发语义——等待时按当前就绪状态重查全部登记项,
// 仍就绪的项重复报告;就绪不消费状态,读空后不再报告。
//
// 事件位掩码由调用层定义(客体 EPOLLIN 等),内核核心只关心
// poll.Readiness 就绪位的交集;错误码与登记生命周期在此管理。
package epoll

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
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
	cancel   func()
	sequence uint64 // 登记次序,报告按登记顺序稳定
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
// 登记即订阅就绪变化,变化时唤醒全部等待者。
func (f *FD) Add(fd uint32, source poll.Source, want poll.Readiness, data uint64) error {
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
	entry := &item{source: source, want: want, data: data, sequence: f.next}
	entry.cancel = source.OnChange(func() { f.wake.WakeAll(wait.Data) })
	f.items[fd] = entry
	f.mu.Unlock()
	f.wake.WakeAll(wait.Data)
	return nil
}

// Modify 更新登记项:fd 未登记报 ENOENT。
func (f *FD) Modify(fd uint32, want poll.Readiness, data uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
	}
	entry, ok := f.items[fd]
	if !ok {
		return errno.Wrap(errno.NotFound, "描述符未登记")
	}
	entry.want = want
	entry.data = data
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

// readyLocked 收集当前就绪项(水平触发:按当前状态重查);调用方
// 必须持有锁。
func (f *FD) readyLocked() []Event {
	ordered := make([]*item, 0, len(f.items))
	for _, entry := range f.items {
		ordered = append(ordered, entry)
	}
	slices.SortFunc(ordered, func(a, b *item) int {
		return cmp.Compare(a.sequence, b.sequence)
	})
	var events []Event
	for _, entry := range ordered {
		received := entry.source.Ready()
		if matched := received & entry.want; matched != 0 {
			events = append(events, Event{Readiness: matched, Data: entry.data})
		}
	}
	return events
}

// Wait 报告当前就绪项;全部未就绪时阻塞到变化或上下文结束。
// maxEvents 限定单次报告条数,剩余就绪项留给后续调用(水平触发)。
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
		events := f.readyLocked()
		f.mu.Unlock()
		if len(events) > maxEvents {
			events = events[:maxEvents]
		}
		if len(events) > 0 {
			return events, nil
		}
		_, err := f.wake.Wait(ctx, func() (bool, wait.Reason, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return len(f.readyLocked()) > 0 || f.closed, wait.Data, nil
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

// ReadyNow 立即返回当前就绪项(0 等待);实例关闭报 EBADF。
func (f *FD) ReadyNow(maxEvents int) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errno.Wrap(errno.BadHandle, "epoll 实例已关闭")
	}
	events := f.readyLocked()
	if len(events) > maxEvents {
		events = events[:maxEvents]
	}
	return events, nil
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
