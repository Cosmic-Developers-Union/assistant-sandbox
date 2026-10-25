package common

import (
	"context"
	"encoding/binary"
	"errors"
	"math"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/epoll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// epoll(L07,水平触发):登记项在等待时按当前就绪状态重查,仍就绪
// 的项每次都报告;读空后不再报告。边缘触发与 ONESHOT 随 L08。
//
//   - epoll_create1(flags):仅 EPOLL_CLOEXEC,未知位 EINVAL;
//   - epoll_ctl(epfd, op, fd, event):ADD/MOD/DEL;目标描述符必须
//     存在(EBADF);普通文件与目录恒就绪(包装为静态就绪源);
//   - epoll_pwait(epfd, events, maxevents, timeout_ms, sigmask):
//     超时 -1 无限、0 立即;等待期间替换信号屏蔽集(结构同
//     pselect6);被待投递信号打断返回 EINTR。

// asm-generic 的 epoll 事件位与操作码。
const (
	epollIn      = 0x001
	epollOut     = 0x004
	epollErr     = 0x008
	epollHup     = 0x010
	epollRdHup   = 0x2000
	epollCloexec = 0o2000000

	epollCtlAdd = 1
	epollCtlDel = 2
	epollCtlMod = 3

	// epollEventSize 是 struct epoll_event 的字节大小
	// (events 4B + 填充 4B + data 8B)。
	epollEventSize = 16

	// epollMaxEvents 是单次等待的报告上限(防客体耗尽宿主内存)。
	epollMaxEvents = 4096
)

// epollWant 把客体事件位映射为内部就绪位。
func epollWant(events uint32) poll.Readiness {
	var want poll.Readiness
	if events&epollIn != 0 {
		want |= poll.Readable
	}
	if events&epollOut != 0 {
		want |= poll.Writable
	}
	if events&(epollErr|epollHup|epollRdHup) != 0 {
		want |= poll.Hangup | poll.Error
	}
	return want
}

// epollEvents 把内部就绪位映射回客体事件位。
func epollEvents(received poll.Readiness) uint32 {
	var events uint32
	if received&poll.Readable != 0 {
		events |= epollIn
	}
	if received&poll.Writable != 0 {
		events |= epollOut
	}
	if received&poll.Hangup != 0 {
		events |= epollHup | epollRdHup
	}
	if received&poll.Error != 0 {
		events |= epollErr
	}
	return events
}

func (s Syscalls) epollCreate1(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0]&^uint64(epollCloexec) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	descriptor, err := s.Descriptors.Epoll(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: descriptor}
}

func (s Syscalls) epollCtl(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	epfd, target, op := call.Args[0], call.Args[2], call.Args[1]
	if epfd > math.MaxUint32 || target > math.MaxUint32 {
		return Reply{Errno: errno.BadHandle}
	}
	instance, err := s.Descriptors.EpollInstance(ctx, epfd)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	switch op {
	case epollCtlDel:
		if err := instance.Remove(uint32(target)); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		return Reply{}
	case epollCtlAdd, epollCtlMod:
	default:
		return Reply{Errno: errno.Invalid}
	}
	// ADD/MOD 需要事件结构;目标描述符必须存在且非 epoll 实例自身。
	var raw [epollEventSize]byte
	if err := s.Memory.Read(ctx, call.Args[3], raw[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	events := binary.LittleEndian.Uint32(raw[0:])
	data := binary.LittleEndian.Uint64(raw[8:])
	if events == 0 {
		return Reply{Errno: errno.Invalid}
	}
	handle, err := s.Descriptors.Get(ctx, target)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	source, ok := handle.(pollSource)
	if !ok {
		// 普通文件与目录恒就绪:包装为静态就绪源。
		source = staticSource{readiness: poll.Readable | poll.Writable}
	}
	want := epollWant(events)
	if op == epollCtlAdd {
		if err := instance.Add(uint32(target), source, want, data); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		return Reply{}
	}
	if err := instance.Modify(uint32(target), want, data); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) epollPwait(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] > math.MaxUint32 {
		return Reply{Errno: errno.BadHandle}
	}
	instance, err := s.Descriptors.EpollInstance(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	maxEvents := call.Args[2]
	if maxEvents == 0 || maxEvents > epollMaxEvents {
		return Reply{Errno: errno.Invalid}
	}
	timeout := int64(call.Args[3])
	if timeout < -1 {
		return Reply{Errno: errno.Invalid}
	}
	// 立即路径:timeout 0 或已就绪,不进入等待。
	if events, _ := instance.ReadyNow(int(maxEvents)); len(events) > 0 {
		return s.writeEpollEvents(ctx, call.Args[1], events, maxEvents)
	}
	if timeout == 0 {
		return Reply{}
	}
	// 等待期间替换信号屏蔽集(结构同 pselect6)。
	restore, err := s.applyPselectMask(ctx, call.Args[4])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if restore != nil {
		defer func() { _, _ = s.Signals.ApplyMask(sigSetMask, *restore) }()
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		events []epoll.Event
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		events, err := instance.Wait(waitCtx, int(maxEvents))
		done <- outcome{events, err}
	}()
	// 超时切片轮询:有限超时到期返回 0;无限(-1)阻塞到就绪/取消;
	// 待投递信号到达返回 EINTR(J09 契约)。
	var deadline uint64
	if timeout > 0 {
		if s.Sleeper == nil {
			cancel()
			<-done
			return Reply{Errno: errno.Interrupted}
		}
		deadline = uint64(timeout) * 1_000_000
	}
	remaining := deadline
	for {
		if s.Interrupts != nil {
			if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
				cancel()
				<-done
				return Reply{Errno: errno.Interrupted}
			}
		}
		if timeout > 0 {
			step := sleepSlice
			if remaining < step {
				step = remaining
			}
			if err := s.Sleeper.Sleep(ctx, step); err != nil && ctx.Err() == nil {
				// 睡眠端口非取消错误:阻塞等待就绪或取消,不再计时。
				result := <-done
				return s.epollOutcome(ctx, call.Args[1], result, maxEvents)
			}
			if ctx.Err() != nil {
				cancel()
				result := <-done
				_ = result
				return Reply{Errno: errno.Interrupted}
			}
			remaining -= step
			if remaining == 0 {
				cancel()
				result := <-done
				if result.err != nil && !errors.Is(result.err, errno.Interrupted) {
					return Reply{Errno: errnoOf(result.err)}
				}
				if len(result.events) > 0 {
					return s.writeEpollEvents(ctx, call.Args[1], result.events, maxEvents)
				}
				return Reply{}
			}
			continue
		}
		// 无限等待:阻塞到就绪或上下文结束。
		result := <-done
		return s.epollOutcome(ctx, call.Args[1], result, maxEvents)
	}
}

// epollOutcome 归并等待结果为应答。
func (s Syscalls) epollOutcome(ctx context.Context, address uint64, result struct {
	events []epoll.Event
	err    error
}, maxEvents uint64) Reply {
	if result.err != nil {
		if errors.Is(result.err, errno.Interrupted) {
			return Reply{Errno: errno.Interrupted}
		}
		return Reply{Errno: errnoOf(result.err)}
	}
	return s.writeEpollEvents(ctx, address, result.events, maxEvents)
}

func (s Syscalls) writeEpollEvents(ctx context.Context, address uint64, events []epoll.Event, maxEvents uint64) Reply {
	if len(events) == 0 {
		return Reply{}
	}
	if uint64(len(events)) > maxEvents {
		events = events[:maxEvents]
	}
	raw := make([]byte, len(events)*epollEventSize)
	for index, event := range events {
		offset := index * epollEventSize
		binary.LittleEndian.PutUint32(raw[offset:], epollEvents(event.Readiness))
		binary.LittleEndian.PutUint64(raw[offset+8:], event.Data)
	}
	if err := s.Memory.Write(ctx, address, raw); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(len(events))}
}

// staticSource 是恒就绪的就绪源(普通文件与目录)。
type staticSource struct{ readiness poll.Readiness }

func (s staticSource) Ready() poll.Readiness           { return s.readiness }
func (s staticSource) OnChange(func()) (cancel func()) { return func() {} }
