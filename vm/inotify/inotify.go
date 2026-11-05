// Package inotify 实现 Linux inotify(7) 所需子集的内核核心部分:
// 监视表、事件队列、溢出与读取编码,并与 poll/epoll 的就绪通知集成。
//
// 事件由系统调用层在成功的文件系统操作后经 Broker 扇出;本包按
// 路径匹配监视(见 A03 兼容配置的登记边界):rename 后旧路径不再
// 收到事件,与内核的 inode 语义存在差异,不作为 inode 语义交付。
package inotify

import (
	"context"
	"encoding/binary"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// 事件位(Linux uapi inotify.h 值)。
const (
	InAccess      uint32 = 0x00000001
	InModify      uint32 = 0x00000002
	InAttrib      uint32 = 0x00000004
	InCloseWrite  uint32 = 0x00000008
	InCloseNowrit uint32 = 0x00000010
	InOpen        uint32 = 0x00000020
	InMovedFrom   uint32 = 0x00000040
	InMovedTo     uint32 = 0x00000080
	InCreate      uint32 = 0x00000100
	InDelete      uint32 = 0x00000200
	InDeleteSelf  uint32 = 0x00000400
	InMoveSelf    uint32 = 0x00000800

	// 内核生成的事件位:不可作为监视掩码请求。
	InQOverflow uint32 = 0x00004000
	InIgnored   uint32 = 0x00008000
	InIsdir     uint32 = 0x40000000
)

// add_watch 的控制标志。
const (
	InOnlydir    uint32 = 0x01000000
	InDontFollow uint32 = 0x02000000
	InExclUnlink uint32 = 0x04000000
	InMaskCreate uint32 = 0x10000000
	InMaskAdd    uint32 = 0x20000000
	InOneshot    uint32 = 0x80000000
)

// allEvents 是可请求的文件系统事件位集合(不含内核生成位与控制位)。
const allEvents = InAccess | InModify | InAttrib | InCloseWrite | InCloseNowrit |
	InOpen | InMovedFrom | InMovedTo | InCreate | InDelete | InDeleteSelf | InMoveSelf

// QueueLimit 是默认事件队列深度(与内核默认 inotify_max_queued_events 一致)。
const QueueLimit = 16384

// EventSize 是 struct inotify_event 的固定部分大小。
const EventSize = 16

// OverflowWD 是 IN_Q_OVERFLOW 事件的 wd(内核约定 -1)。
const OverflowWD = ^uint32(0) // -1

// Event 是一次待读取的文件系统事件。
type Event struct {
	// WD 是产生事件的监视描述符;溢出事件为 OverflowWD。
	WD uint32
	// Mask 是事件位(含 InIsdir/InIgnored/InQOverflow 等内核位)。
	Mask uint32
	// Cookie 用于配对 IN_MOVED_FROM/IN_MOVED_TO。
	Cookie uint32
	// Name 是相对监视目录的条目名;监视目标自身的事件为空。
	Name string
}

// Options 是 inotify_init1 的选项。
type Options struct {
	// NonBlock 表示非阻塞模式:队列为空时读操作立即返回 errno.WouldBlock。
	NonBlock bool
}

// watch 是一个路径监视。
type watch struct {
	path    string
	mask    uint32
	oneshot bool
}

// FD 是一个 inotify 实例:监视表与事件队列。
type FD struct {
	mu       sync.Mutex
	wake     wait.Queue
	nonblock bool
	closed   bool
	nextWD   uint32
	watches  map[uint32]*watch
	queue    []Event
	head     int
	watchers []*watchFunc
}

type watchFunc struct{ fn func() }

// New 创建 inotify 实例。
func New(options Options) *FD {
	return &FD{nonblock: options.NonBlock, watches: map[uint32]*watch{}}
}

// Write 实现 fd.Stream:inotify 不支持写入,报 errno.Invalid。
func (f *FD) Write(p []byte) (int, error) {
	return 0, errno.Invalid
}

// SetNonBlock 切换非阻塞模式。
func (f *FD) SetNonBlock(nonblock bool) {
	f.mu.Lock()
	f.nonblock = nonblock
	f.mu.Unlock()
}

// Pending 返回当前排队的事件数(诊断用)。
func (f *FD) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pendingLocked()
}

func (f *FD) pendingLocked() int {
	return len(f.queue) - f.head
}

// AddWatch 登记 path 的监视掩码并返回监视描述符。flags 的控制位:
// InMaskAdd(叠加掩码)、InMaskCreate(已存在报 EEXIST)、InOnlydir
// (目标非目录报 ENOTDIR,由调用方以 isDir 标记)、InOneshot(一次
// 事件后自动移除);InDontFollow/InExclUnlink 不在子集内,报
// ENOTSUP;未知位 EINVAL;已关闭 EBADF;掩码含不可请求位 EINVAL。
func (f *FD) AddWatch(path string, mask uint32, flags uint32, isDir bool) (uint32, error) {
	if flags&^(InMaskAdd|InMaskCreate|InOnlydir|InOneshot|InDontFollow|InExclUnlink) != 0 {
		return 0, errno.Invalid
	}
	if flags&(InDontFollow|InExclUnlink) != 0 {
		return 0, errno.NotSupported
	}
	if flags&InOnlydir != 0 && !isDir {
		return 0, errno.NotDirectory
	}
	events := mask &^ InOneshot
	if events&^allEvents != 0 {
		return 0, errno.Invalid
	}
	if events == 0 {
		return 0, errno.Invalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errno.BadHandle
	}
	for id, existing := range f.watches {
		if existing.path == path {
			if flags&InMaskCreate != 0 {
				return 0, errno.Exist
			}
			if flags&InMaskAdd != 0 {
				existing.mask |= events
			} else {
				existing.mask = events
			}
			existing.oneshot = flags&InOneshot != 0
			return id, nil
		}
	}
	f.nextWD++
	if f.nextWD == OverflowWD {
		f.nextWD = 1
	}
	id := f.nextWD
	f.watches[id] = &watch{path: path, mask: events, oneshot: flags&InOneshot != 0}
	return id, nil
}

// RemoveWatch 移除监视;未知描述符报 errno.Invalid。
func (f *FD) RemoveWatch(wd uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errno.BadHandle
	}
	if _, ok := f.watches[wd]; !ok {
		return errno.Invalid
	}
	delete(f.watches, wd)
	return nil
}

// Observe 扇出一次事件:path 是受影响条目的客体绝对路径,mask 为
// 纯事件位(不含 InIsdir/InIgnored 等内核位)。同一 path 命中:监视
// 目标自身(DELETE 转为 DELETE_SELF 并自动移除+IN_IGNORED;MOVED_FROM
// 转为 MOVE_SELF,与内核一致)与父目录监视(条目名)。队列满时丢弃
// 新事件并保证队尾恰有一条 IN_Q_OVERFLOW(wd=-1),与内核语义一致。
// 有事件入队时唤醒等待者并通知就绪订阅者。
func (f *FD) Observe(path string, mask uint32, cookie uint32, isDir bool) bool {
	if mask&^allEvents != 0 {
		return false
	}
	if f.enqueue(path, mask, cookie, isDir) {
		f.notify()
		return true
	}
	return false
}

// enqueue 在锁内入队一次事件;返回是否有事件入队。
func (f *FD) enqueue(path string, mask uint32, cookie uint32, isDir bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	base, parent := splitPath(path)
	grew := false
	for id, w := range f.watches {
		if w.mask&mask == 0 {
			// 该监视未请求这些事件位,不命中。
			continue
		}
		switch {
		case path == w.path:
			bits := mask
			if bits&InDelete != 0 {
				// 监视目标自身被删除:DELETE_SELF + 自动移除。
				grew = f.enqueueLocked(Event{WD: id, Mask: bits&^InDelete | InDeleteSelf | dirBit(isDir), Cookie: cookie}) || grew
				delete(f.watches, id)
				grew = f.enqueueLocked(Event{WD: id, Mask: InIgnored, Cookie: cookie}) || grew
				continue
			}
			if bits&InMovedFrom != 0 {
				// 监视目标自身被移走:MOVE_SELF,监视保留(路径语义
				// 的既定差异,见包注释)。
				grew = f.enqueueLocked(Event{WD: id, Mask: bits&^InMovedFrom | InMoveSelf | dirBit(isDir), Cookie: cookie}) || grew
				continue
			}
			grew = f.enqueueLocked(Event{WD: id, Mask: bits | dirBit(isDir), Cookie: cookie}) || grew
		case parent != "" && parent == w.path:
			grew = f.enqueueLocked(Event{WD: id, Mask: mask | dirBit(isDir), Cookie: cookie, Name: base}) || grew
		}
		if w.oneshot {
			// 一次性监视消费后自动移除(内核语义,配 IN_IGNORED)。
			delete(f.watches, id)
			grew = f.enqueueLocked(Event{WD: id, Mask: InIgnored, Cookie: cookie}) || grew
		}
	}
	return grew
}

// dirBit 把目录标记转成 IN_ISDIR 位。
func dirBit(isDir bool) uint32 {
	if isDir {
		return InIsdir
	}
	return 0
}

// enqueueLocked 入队事件;队列满时丢弃新事件并保证队尾恰有一条
// 溢出事件。返回是否实际入队。
func (f *FD) enqueueLocked(event Event) bool {
	if f.pendingLocked() >= QueueLimit {
		// 溢出:新事件丢弃;若无溢出事件在队尾,补一条(wd=-1)。
		if f.pendingLocked() > 0 && f.queue[len(f.queue)-1].Mask&InQOverflow == 0 {
			f.queue = append(f.queue, Event{WD: OverflowWD, Mask: InQOverflow})
			return true
		}
		return false
	}
	f.queue = append(f.queue, event)
	return true
}

// Read 读取事件流;缓冲区容纳不下一条完整事件时报 errno.Invalid,
// 否则按整事件填充(与内核 read(2) 语义一致)。队列为空时非阻塞
// 读返回 errno.WouldBlock,阻塞读等待新事件。
func (f *FD) Read(p []byte) (int, error) {
	return f.ReadContext(context.Background(), p)
}

// ReadContext 是 Read 的可取消版本:等待期间 ctx 结束返回 errno.Interrupted。
func (f *FD) ReadContext(ctx context.Context, p []byte) (int, error) {
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, errno.BadHandle
		}
		if n := f.pendingLocked(); n > 0 {
			total := 0
			for i := f.head; i < len(f.queue); i++ {
				size := EventSize + nameLen(f.queue[i].Name)
				if total+size > len(p) {
					break
				}
				encodeEvent(p[total:], f.queue[i])
				total += size
				f.head++
			}
			if f.head == len(f.queue) {
				f.queue = nil
				f.head = 0
			}
			f.mu.Unlock()
			f.notify()
			return total, nil
		}
		if f.nonblock {
			f.mu.Unlock()
			return 0, errno.WouldBlock
		}
		f.mu.Unlock()
		_, err := f.wake.Wait(ctx, func() (bool, wait.Reason, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.pendingLocked() > 0 || f.closed, wait.Data, nil
		})
		if err != nil {
			return 0, errno.Interrupted
		}
	}
}

// nameLen 返回名字段的编码长度:名字 + NUL,按 4 字节对齐(内核
// 布局:len 字段含填充)。
func nameLen(name string) int {
	return (len(name) + 1 + 3) &^ 3
}

// encodeEvent 按 struct inotify_event 布局写出(小端):
//
//	偏移  0: int32  wd
//	偏移  4: uint32 mask
//	偏移  8: uint32 cookie
//	偏移 12: uint32 len(名字段长度,含 NUL 与对齐填充)
//	偏移 16: 名字节(NUL 终止,零填充)
func encodeEvent(dst []byte, event Event) {
	binary.LittleEndian.PutUint32(dst[0:4], event.WD)
	binary.LittleEndian.PutUint32(dst[4:8], event.Mask)
	binary.LittleEndian.PutUint32(dst[8:12], event.Cookie)
	length := nameLen(event.Name)
	binary.LittleEndian.PutUint32(dst[12:16], uint32(length))
	clear(dst[EventSize : EventSize+length])
	copy(dst[EventSize:], event.Name)
}

// Close 关闭实例:唤醒等待者,之后读操作返回 errno.BadHandle。
func (f *FD) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errno.BadHandle
	}
	f.closed = true
	f.mu.Unlock()
	f.wake.Close()
	f.notify()
	return nil
}

// Ready 返回就绪状态:队列非空 Readable;恒 Writable;关闭 Hangup。
func (f *FD) Ready() poll.Readiness {
	f.mu.Lock()
	defer f.mu.Unlock()
	var bits poll.Readiness
	if f.closed {
		return bits | poll.Hangup
	}
	if f.pendingLocked() > 0 {
		bits |= poll.Readable
	}
	return bits | poll.Writable
}

// OnChange 注册状态变化回调(入队或关闭时触发),返回取消函数;
// poll/epoll 层用它把 inotify 接入等待图。
func (f *FD) OnChange(fn func()) (cancel func()) {
	w := &watchFunc{fn: fn}
	f.mu.Lock()
	f.watchers = append(f.watchers, w)
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for index, registered := range f.watchers {
			if registered == w {
				f.watchers = append(f.watchers[:index], f.watchers[index+1:]...)
				return
			}
		}
	}
}

// notify 唤醒等待者并通知订阅者;调用方不得持有 f.mu。
func (f *FD) notify() {
	f.wake.WakeAll(wait.Data)
	f.mu.Lock()
	watchers := make([]*watchFunc, len(f.watchers))
	copy(watchers, f.watchers)
	f.mu.Unlock()
	for _, w := range watchers {
		w.fn()
	}
}

// splitPath 拆出条目名与父目录("/" 的父目录为空)。
func splitPath(path string) (base, parent string) {
	index := strings.LastIndexByte(path, '/')
	if index <= 0 {
		return path, ""
	}
	return path[index+1:], path[:index]
}
