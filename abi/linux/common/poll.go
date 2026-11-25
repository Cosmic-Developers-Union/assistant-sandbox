package common

import (
	"context"
	"encoding/binary"
	"errors"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// asm-generic 的 poll 事件与返回码。
const (
	pollIn   = 0x001
	pollOut  = 0x004
	pollErr  = 0x008
	pollHup  = 0x010
	pollNVal = 0x020

	// pollfdSize 是 asm-generic 的 struct pollfd 大小(fd/events/revents)。
	pollfdSize = 8

	// pollMaxFDs 是单次 poll 的描述符上限,防止客体以超大数组耗尽
	// 宿主内存。
	pollMaxFDs = 4096
)

// pollSource 是描述符句柄的可选就绪能力:管道、eventfd 等实现它,
// 普通文件不实现(恒为就绪)。
type pollSource interface {
	Ready() poll.Readiness

	OnChange(func()) (cancel func())
}

// poll 等待一组描述符就绪(L05):普通文件恒就绪,管道与 eventfd 按
// 就绪能力判定,关闭的描述符报 POLLNVAL;超时返回 0。
func (s Syscalls) poll(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	fds := call.Args[0]
	count := call.Args[1]
	if count > pollMaxFDs {
		return Reply{Errno: errno.Invalid}
	}
	entries := make([]pollEntry, 0, count)
	for index := uint64(0); index < count; index++ {
		offset := index * uint64(pollfdSize)
		var raw [pollfdSize]byte
		if err := s.Memory.Read(ctx, fds+offset, raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		descriptor := binary.LittleEndian.Uint32(raw[0:])
		events := binary.LittleEndian.Uint16(raw[4:])
		entries = append(entries, pollEntry{descriptor: descriptor, events: events})
	}
	// ppoll 在等待期间替换信号屏蔽集,结束后恢复。
	if call.Syscall == SysPpoll {
		old, err := s.applyPollMask(ctx, call)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		if old != nil {
			defer func() { _, _ = s.Signals.ApplyMask(sigSetMask, *old) }()
		}
	}

	timeout := s.pollTimeout(ctx, call)
	_, err := s.waitPoll(ctx, entries, timeout)
	if err != nil && !errors.Is(err, errno.TimedOut) {
		return Reply{Errno: errnoOf(err)}
	}
	count = uint64(0)
	for index := range entries {
		offset := uint64(index) * uint64(pollfdSize)
		revents := entries[index].revents
		if revents != 0 {
			count++
		}
		var raw [pollfdSize]byte
		binary.LittleEndian.PutUint32(raw[0:], entries[index].descriptor)
		binary.LittleEndian.PutUint16(raw[4:], entries[index].events)
		binary.LittleEndian.PutUint16(raw[6:], revents)
		if err := s.Memory.Write(ctx, fds+offset, raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Value: count}
}

// pollEntry 是一个描述符的等待项。
type pollEntry struct {
	descriptor uint32
	events     uint16
	revents    uint16
}

// pollTimeout 解析超时:poll 以毫秒计(-1 表示无限),ppoll 以时间结构
// 计(绝对或相对,取决于调用方)。返回纳秒数,0 表示无限等待。
// pollTimeout 解析超时:负值/NULL 为无限(poll.PollInfinite),零为
// 立即返回,正值按纳秒。
func (s Syscalls) pollTimeout(ctx context.Context, call Call) uint64 {
	if call.Syscall == SysPoll {
		milliseconds := int64(call.Args[2])
		if milliseconds < 0 {
			return poll.PollInfinite
		}
		if milliseconds == 0 {
			return 0
		}
		return uint64(milliseconds) * uint64(1_000_000)
	}
	if call.Args[2] == 0 {
		return poll.PollInfinite
	}
	var raw [16]byte
	if err := s.Memory.Read(ctx, call.Args[2], raw[:]); err != nil {
		return poll.PollInfinite
	}
	seconds := int64(binary.LittleEndian.Uint64(raw[0:]))
	nanoseconds := int64(binary.LittleEndian.Uint64(raw[8:]))
	if seconds < 0 || nanoseconds < 0 {
		return poll.PollInfinite
	}
	total := seconds*1_000_000_000 + nanoseconds
	if total <= 0 {
		return 0
	}
	return uint64(total)
}

// applyPollMask 在等待期间替换信号屏蔽集(ppoll):返回等待前的屏蔽
// 集,由调用方在等待结束后恢复。sigmask 为 NULL 时不改变屏蔽集。
func (s Syscalls) applyPollMask(ctx context.Context, call Call) (*uint64, error) {
	if s.Signals == nil || call.Args[3] == 0 {
		return nil, nil
	}
	if call.Args[4] != sigsetSize {
		return nil, errno.Invalid
	}
	var set [8]byte
	if err := s.Memory.Read(ctx, call.Args[3], set[:]); err != nil {
		return nil, errno.Fault
	}
	mask := binary.LittleEndian.Uint64(set[:])
	old, err := s.Signals.ApplyMask(sigSetMask, mask)
	if err != nil {
		return nil, err
	}
	return &old, nil
}

// waitPoll 等待描述符就绪:普通文件恒就绪,其余按就绪能力判定;
// 全部未就绪时等待变化或超时。
func (s Syscalls) waitPoll(ctx context.Context, entries []pollEntry, timeout uint64) ([]pollEntry, error) {
	sources := make([]poll.Entry, 0, len(entries))
	sourceIndex := make([]int, 0, len(entries))
	for index := range entries {
		entry := &entries[index]
		if int32(entry.descriptor) < 0 {
			// poll(2):负描述符被忽略,revents=0,不是 POLLNVAL。
			entry.revents = 0
			continue
		}
		handle, err := s.Descriptors.Get(ctx, uint64(entry.descriptor))
		if err != nil {
			// 关闭的描述符:POLLNVAL,且不参与等待。
			entry.revents = pollNVal
			continue
		}
		source, ok := handle.(pollSource)
		if !ok {
			// 普通文件与目录:读写恒就绪。
			entry.revents = entry.events & (pollIn | pollOut)
			continue
		}
		sources = append(sources, poll.Entry{Source: source, Want: s.pollWant(entry.events)})
		sourceIndex = append(sourceIndex, index)
	}
	if len(sources) == 0 {
		return entries, nil
	}
	var clk Sleeper
	if s.Sleeper != nil {
		clk = s.Sleeper
	}
	// J09:阻塞等待按 1ms 切片轮询待投递信号,命中即 EINTR;
	// 零超时(立即)与未装配信号端口时保持原语义。
	if timeout == poll.PollInfinite && s.Interrupts != nil {
		for {
			results, err := poll.WaitFor(ctx, clk, sources, sleepSlice)
			if errors.Is(err, errno.TimedOut) {
				if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
					return entries, errno.Interrupted
				}
				continue
			}
			if err != nil {
				return entries, err
			}
			if len(results) > 0 {
				s.applyPollResults(entries, sourceIndex, results)
				return entries, nil
			}
			if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
				return entries, errno.Interrupted
			}
		}
	}
	results, err := poll.WaitFor(ctx, clk, sources, timeout)
	if err != nil {
		return entries, err
	}
	s.applyPollResults(entries, sourceIndex, results)
	return entries, nil
}

func (s Syscalls) applyPollResults(entries []pollEntry, sourceIndex []int, results []poll.Result) {
	for _, result := range results {
		if result.Index < 0 || result.Index >= len(sourceIndex) {
			continue
		}
		entry := &entries[sourceIndex[result.Index]]
		entry.revents = s.pollRevents(entry.events, result.Received)
	}
}

// pollWant 把客体的事件位映射为内部就绪位。
func (s Syscalls) pollWant(events uint16) poll.Readiness {
	var want poll.Readiness
	if events&pollIn != 0 {
		want |= poll.Readable
	}
	if events&pollOut != 0 {
		want |= poll.Writable
	}
	return want
}

// pollRevents 把内部就绪位映射回客体的返回位。
func (s Syscalls) pollRevents(events uint16, received poll.Readiness) uint16 {
	var revents uint16
	if received&poll.Readable != 0 && events&pollIn != 0 {
		revents |= pollIn
	}
	if received&poll.Writable != 0 && events&pollOut != 0 {
		revents |= pollOut
	}
	if received&poll.Hangup != 0 {
		revents |= pollHup
	}
	if received&poll.Error != 0 {
		revents |= pollErr
	}
	return revents
}
