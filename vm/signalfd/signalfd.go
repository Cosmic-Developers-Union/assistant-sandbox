// Package signalfd 实现 Linux signalfd(2) 的内核核心部分:
// 被屏蔽的信号可以经由该文件描述符读取,并与 poll/epoll 的就绪通知集成。
//
// 本包只负责"被屏蔽信号的投递与读取",不负责与信号处理器的交互
// (处置、信号帧落地等由 vm/signal 与上层系统调用层负责)。
package signalfd

import (
	"context"
	"encoding/binary"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// RecordSize 是 struct signalfd_siginfo 的字节大小(内核 ABI 固定 128 字节)。
const RecordSize = 128

// SigsetSize 是 64 位平台上 sigset_t 的字节大小(8 字节)。
const SigsetSize = 8

// MaxSignal 是掩码位图能表达的最大信号号(位 n-1 对应信号 n,共 64 位)。
const MaxSignal = 64

// ssi_code 的取值:信号来源代码。
const (
	// CodeUser 表示信号由 kill(2)/tgkill(2) 等用户态请求发送。
	CodeUser uint32 = 0x0
	// CodeKernel 表示信号由内核内部原因产生。
	CodeKernel uint32 = 0x80
	// CodeQueue 表示信号来自 sigqueue(2) 携带数据的排队请求。
	CodeQueue uint32 = 0xfffffff0
)

// Signal 描述一次待投递(待读取)的信号。
type Signal struct {
	// Signo 是信号号(1..MaxSignal)。
	Signo uint32
	// PID 是发送者进程号。
	PID uint32
	// UID 是发送者的用户号。
	UID uint32
	// Code 是信号来源代码(ssi_code),取值见 CodeUser 等常量。
	Code uint32
}

// Options 是 signalfd4 的选项集合。
type Options struct {
	// NonBlock 表示非阻塞模式:队列为空时读操作立即返回 errno.WouldBlock。
	NonBlock bool
}

// watchFunc 是一个 OnChange 订阅。
type watchFunc struct{ fn func() }

// FD 是 signalfd 实例:持有阻塞集合(信号掩码)与待读信号队列。
type FD struct {
	mu       sync.Mutex
	wake     wait.Queue
	mask     uint64
	nonblock bool
	closed   bool
	queue    []Signal
	head     int
	watchers []*watchFunc
}

// New 创建一个 signalfd 实例。mask 是信号掩码位图(位 n-1 对应信号 n);
// 掩码为空时无效(与 signalfd4 一致返回 EINVAL)。
func New(mask uint64, options Options) (*FD, error) {
	if mask == 0 {
		return nil, errno.Invalid
	}
	return &FD{mask: mask, nonblock: options.NonBlock}, nil
}

// SetMask 更新阻塞集合(signalfd4 对已存在描述符的形式)。
// 新掩码为空报 errno.Invalid;已关闭报 errno.BadHandle。
func (f *FD) SetMask(mask uint64) error {
	if mask == 0 {
		return errno.Invalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errno.BadHandle
	}
	f.mask = mask
	return nil
}

// SetNonBlock 切换非阻塞模式。
func (f *FD) SetNonBlock(nonblock bool) {
	f.mu.Lock()
	f.nonblock = nonblock
	f.mu.Unlock()
}

// Write 实现 fd.Stream:signalfd 不支持写入,报 errno.Invalid。
func (f *FD) Write(_ []byte) (int, error) {
	return 0, errno.Invalid
}

// ParseSigset 把客体的 8 字节小端 sigset 转成掩码位图(位 n-1 对应信号 n)。
func ParseSigset(raw []byte) (uint64, error) {
	if len(raw) != SigsetSize {
		return 0, errno.Invalid
	}
	return binary.LittleEndian.Uint64(raw), nil
}

// Mask 返回本 signalfd 的阻塞集合(位 n-1 对应信号 n)。
func (f *FD) Mask() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mask
}

// Pending 返回当前排队的待读信号条数。
func (f *FD) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queue) - f.head
}

// Masked 判断给定信号号是否在本 signalfd 的阻塞集合内。
func (f *FD) Masked(signo uint32) bool {
	if signo == 0 || signo > MaxSignal {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mask&(1<<(signo-1)) != 0
}

// Deliver 投递一个信号。只有信号在阻塞集合内才会入队,
// 否则返回 errno.NotPermitted(上层可以忽略该错误)。
func (f *FD) Deliver(sig Signal) error {
	if sig.Signo == 0 || sig.Signo > MaxSignal {
		return errno.Invalid
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errno.BadHandle
	}
	if f.mask&(1<<(sig.Signo-1)) == 0 {
		f.mu.Unlock()
		return errno.NotPermitted
	}
	f.queue = append(f.queue, sig)
	f.mu.Unlock()
	f.notify()
	return nil
}

// Read 读取待投递的信号。返回值是写入的字节数,恒为 RecordSize 的整数倍。
// 缓冲区为空返回 0;缓冲区长度不是 RecordSize 的整数倍返回 errno.Invalid;
// 队列为空时非阻塞读返回 errno.WouldBlock,阻塞读等待新信号。
func (f *FD) Read(p []byte) (int, error) {
	return f.ReadContext(context.Background(), p)
}

// ReadContext 是 Read 的可取消版本:等待期间 ctx 结束返回 errno.Interrupted。
func (f *FD) ReadContext(ctx context.Context, p []byte) (int, error) {
	if len(p) == 0 {
		f.mu.Lock()
		closed := f.closed
		f.mu.Unlock()
		if closed {
			return 0, errno.BadHandle
		}
		return 0, nil
	}
	if len(p)%RecordSize != 0 {
		return 0, errno.Invalid
	}
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, errno.BadHandle
		}
		if count := f.pendingLocked(); count > 0 {
			taken := count
			if limit := len(p) / RecordSize; taken > limit {
				taken = limit
			}
			buffer := make([]byte, taken*RecordSize)
			window := buffer
			for i := 0; i < taken; i++ {
				encodeRecord(window, f.queue[f.head+i])
				window = window[RecordSize:]
			}
			f.head += taken
			if f.head == len(f.queue) {
				f.queue = nil
				f.head = 0
			}
			f.mu.Unlock()
			f.notify()
			copy(p, buffer)
			return len(buffer), nil
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
			f.mu.Lock()
			closed := f.closed
			f.mu.Unlock()
			if closed {
				return 0, errno.BadHandle
			}
			return 0, errno.Interrupted
		}
	}
}

// pendingLocked 返回排队条数;调用方必须持有锁。
func (f *FD) pendingLocked() int {
	return len(f.queue) - f.head
}

// Close 关闭 signalfd:唤醒所有等待者,之后读操作返回 errno.BadHandle。
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

// Ready 返回当前就绪状态:队列非空时为 Readable,未关闭时恒为 Writable。
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
	bits |= poll.Writable
	return bits
}

// OnChange 注册状态变化回调(入队或关闭时触发),返回取消注册的函数。
// poll/epoll 层用它把 signalfd 接入等待图。
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

// notify 唤醒等待者并通知状态变化订阅者。
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

// encodeRecord 按 Linux struct signalfd_siginfo 布局写出 128 字节记录(小端):
//
//	偏移  0: uint32 ssi_signo
//	偏移  4: int32  ssi_errno(恒为 0)
//	偏移  8: uint32 ssi_code
//	偏移 12: uint32 ssi_fd(恒为 0)
//	偏移 16: uint32 ssi_pid
//	偏移 20: uint32 ssi_uid
//	偏移 24: uint32 ssi_status(恒为 0)
//	偏移 28: uint32 ssi_band(恒为 0)
//	偏移 32..128: 填充零
//
// dst 的长度必须不少于 RecordSize。
func encodeRecord(dst []byte, sig Signal) {
	binary.LittleEndian.PutUint32(dst[0:4], sig.Signo)
	binary.LittleEndian.PutUint32(dst[4:8], 0)
	binary.LittleEndian.PutUint32(dst[8:12], sig.Code)
	binary.LittleEndian.PutUint32(dst[12:16], 0)
	binary.LittleEndian.PutUint32(dst[16:20], sig.PID)
	binary.LittleEndian.PutUint32(dst[20:24], sig.UID)
	binary.LittleEndian.PutUint32(dst[24:28], 0)
	binary.LittleEndian.PutUint32(dst[28:32], 0)
	clear(dst[32:RecordSize])
}
