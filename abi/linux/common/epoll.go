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

// epoll(L07 水平触发,L08 边缘触发与一次性):登记项在等待时按
// 当前就绪状态重查;边缘触发(EPOLLET)只在登记/重新武装后的新
// 活动产生边沿时报告一次;一次性(EPOLLONESHOT)报告一次后禁用,
// MOD 重新武装。
//
//   - epoll_create1(flags):仅 EPOLL_CLOEXEC,未知位 EINVAL;
//   - epoll_ctl(epfd, op, fd, event):ADD/MOD/DEL;目标描述符必须
//     存在(EBADF);普通文件与目录恒就绪(包装为静态就绪源);
//   - epoll_pwait(epfd, events, maxevents, timeout_ms, sigmask, size):
//     超时 -1 无限、0 立即;sigmask 是直指针(非 pselect6 的
//     {指针,长度} 结构),非空时 size 必须为 8;
//   - epoll_pwait2(同上,超时为 timespec 指针):NULL 表示无限,
//     负值或纳秒越界 EINVAL;等待期间替换信号屏蔽集;被待投递
//     信号打断返回 EINTR。

// asm-generic 的 epoll 事件位与操作码。
const (
	epollIn      = 0x001
	epollOut     = 0x004
	epollErr     = 0x008
	epollHup     = 0x010
	epollRdHup   = 0x2000
	epollEt      = 0x80000000 // EPOLLET(1 << 31)
	epollOneShot = 0x40000000 // EPOLLONESHOT(1 << 30)
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

// epollFlags 提取触发模式位(EPOLLET/EPOLLONESHOT)。
func epollFlags(events uint32) epoll.Flags {
	var flags epoll.Flags
	if events&epollEt != 0 {
		flags |= epoll.EdgeTrigger
	}
	if events&epollOneShot != 0 {
		flags |= epoll.OneShot
	}
	return flags
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
	flags := epollFlags(events)
	if op == epollCtlAdd {
		if err := instance.Add(uint32(target), source, want, data, flags); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		return Reply{}
	}
	if err := instance.Modify(uint32(target), want, data, flags); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// epollPwait 是 epoll_pwait(22):超时为 int 毫秒(-1 无限,0 立即),
// sigmask 为直指针 + 独立长度参数。
func (s Syscalls) epollPwait(ctx context.Context, call Call) Reply {
	timeout := int64(call.Args[3])
	if timeout < -1 {
		return Reply{Errno: errno.Invalid}
	}
	var nanoseconds uint64
	if timeout > 0 {
		nanoseconds = uint64(timeout) * 1_000_000
	}
	return s.epollWaitReady(ctx, call, nanoseconds, timeout < 0)
}

// epollPwait2 是 epoll_pwait2(441):超时为 timespec 指针(NULL 表示
// 无限);负值或纳秒越界 EINVAL。
func (s Syscalls) epollPwait2(ctx context.Context, call Call) Reply {
	nanoseconds, forever, code := s.pselectTimeout(ctx, call.Args[3])
	if code != "" {
		return Reply{Errno: code}
	}
	return s.epollWaitReady(ctx, call, nanoseconds, forever)
}

// epollWaitReady 是 epoll_pwait/epoll_pwait2 的公共等待路径:
// timeoutNanos 为 0 表示无限等待(forever);sigmask(直指针,
// args[4])非空时 sigsetsize(args[5])必须为 8,等待期间替换
// 信号屏蔽集,恢复在返回前完成。
func (s Syscalls) epollWaitReady(ctx context.Context, call Call, timeoutNanos uint64, forever bool) Reply {
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
	// 立即路径:已可报告或超时 0,不进入等待。
	if events, _ := instance.ReadyNow(int(maxEvents)); len(events) > 0 {
		return s.writeEpollEvents(ctx, call.Args[1], events, maxEvents)
	}
	if !forever && timeoutNanos == 0 {
		return Reply{}
	}
	// 等待期间替换信号屏蔽集(epoll_pwait 族:sigmask 是直指针,
	// 长度独立传参——区别于 pselect6 的 {指针,长度} 结构)。
	restore, err := s.applyEpollMask(ctx, call.Args[4], call.Args[5])
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
	// 超时切片轮询:有限超时到期返回 0;无限阻塞到就绪/取消;
	// 待投递信号到达返回 EINTR(J09 契约)。
	var deadline uint64
	if !forever {
		if s.Sleeper == nil {
			cancel()
			<-done
			return Reply{Errno: errno.Interrupted}
		}
		deadline = timeoutNanos
	}
	remaining := deadline
	for {
		// 有限超时按 1ms 切片轮询信号,但等待内核已就绪时必须立即
		// 返回:切片只是信号检查周期,不是就绪延迟上限。
		select {
		case result := <-done:
			cancel()
			return s.epollOutcome(ctx, call.Args[1], result, maxEvents)
		default:
		}
		if s.Interrupts != nil {
			if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
				cancel()
				<-done
				return Reply{Errno: errno.Interrupted}
			}
		}
		if !forever {
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

// applyEpollMask 在等待期间替换信号屏蔽集:epoll_pwait 族的 sigmask
// 是直指针,长度独立传参;非空时长度必须为 8(sigset_t)。
func (s Syscalls) applyEpollMask(ctx context.Context, maskAddress, size uint64) (*uint64, error) {
	if maskAddress == 0 {
		return nil, nil
	}
	if size != sigsetSize {
		return nil, errno.Wrap(errno.Invalid, "sigsetsize 必须为 8")
	}
	if s.Signals == nil {
		return nil, errno.Wrap(errno.NotImplemented, "信号端口未装配")
	}
	var set [8]byte
	if err := s.Memory.Read(ctx, maskAddress, set[:]); err != nil {
		return nil, errno.Fault
	}
	old, err := s.Signals.ApplyMask(sigSetMask, binary.LittleEndian.Uint64(set[:]))
	if err != nil {
		return nil, err
	}
	return &old, nil
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
