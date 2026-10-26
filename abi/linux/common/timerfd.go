package common

import (
	"context"
	"encoding/binary"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/timerfd"
)

// timerfd(L10):把到期转化为可读事件的描述符。
//
//   - timerfd_create(clockid, flags):CLOCK_REALTIME/CLOCK_MONOTONIC,
//     其余 EINVAL;TFD_NONBLOCK/TFD_CLOEXEC;
//   - timerfd_settime(fd, flags, new, old):TFD_TIMER_ABSTIME 按所选
//     时钟取绝对时刻(已过期立即到期一次);it_value 全零解除武装;
//     返回经 old(可空)写回旧武装状态;过期计数随重新武装清零;
//   - timerfd_gettime(fd, curr):it_value 是距下次到期的剩余时长
//     (解除武装为 0),it_interval 是周期区间;
//   - read():8 字节过期计数(宿主序小端),读清零;无到期阻塞
//     (非阻塞 EAGAIN);到期通知经 poll/epoll 就绪图(poll.Source)。

// asm-generic 的 timerfd 标志。
const (
	tfdNonBlock    = 0o4000
	tfdCloexec     = 0o2000000
	tfdAbstime     = 1
	tfdKnownFlags  = tfdNonBlock | tfdCloexec
	tfdSettimeMask = tfdAbstime
)

// itimerspec 的字节大小(两个 timespec)。
const itimerspecSize = 32

// TimerFDs 是 timerfd 的描述符端口(L10);ProcessDescriptors 实现。
type TimerFDs interface {
	TimerFDCreate(ctx context.Context, clockID, flags uint64) (uint64, error)

	TimerFDSetTime(ctx context.Context, descriptor, flags uint64, spec timerfd.Spec) (timerfd.Spec, error)

	TimerFDGetTime(ctx context.Context, descriptor uint64) (*timerfd.Spec, error)

	TimerFDAt(ctx context.Context, descriptor uint64) (*timerfd.FD, error)
}

func (s Syscalls) timerfdCreate(ctx context.Context, call Call) Reply {
	if s.TimerSource == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != timerfd.ClockRealtime && call.Args[0] != timerfd.ClockMonotonic {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[1]&^tfdKnownFlags != 0 {
		return Reply{Errno: errno.Invalid}
	}
	descriptor, err := s.TimerSource.TimerFDCreate(ctx, call.Args[0], call.Args[1])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: descriptor}
}

func (s Syscalls) timerfdSettime(ctx context.Context, call Call) Reply {
	if s.TimerSource == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[1]&^tfdSettimeMask != 0 {
		return Reply{Errno: errno.Invalid}
	}
	spec, code := s.readITimerspec(ctx, call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	old, err := s.TimerSource.TimerFDSetTime(ctx, call.Args[0], call.Args[1], spec)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if call.Args[3] != 0 {
		if err := s.writeITimerspec(ctx, call.Args[3], old); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{}
}

func (s Syscalls) timerfdGettime(ctx context.Context, call Call) Reply {
	if s.TimerSource == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	spec, err := s.TimerSource.TimerFDGetTime(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if err := s.writeITimerspec(ctx, call.Args[1], *spec); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

// readITimerspec 读入 struct itimerspec;负值或纳秒越界 EINVAL。
func (s Syscalls) readITimerspec(ctx context.Context, address uint64) (timerfd.Spec, errno.Code) {
	if address == 0 {
		return timerfd.Spec{}, errno.Invalid
	}
	var raw [itimerspecSize]byte
	if err := s.Memory.Read(ctx, address, raw[:]); err != nil {
		return timerfd.Spec{}, errno.Fault
	}
	intervalSec := int64(binary.LittleEndian.Uint64(raw[0:]))
	intervalNs := int64(binary.LittleEndian.Uint64(raw[8:]))
	valueSec := int64(binary.LittleEndian.Uint64(raw[16:]))
	valueNs := int64(binary.LittleEndian.Uint64(raw[24:]))
	for _, part := range [][2]int64{{intervalSec, intervalNs}, {valueSec, valueNs}} {
		if part[0] < 0 || part[1] < 0 || part[1] >= 1_000_000_000 {
			return timerfd.Spec{}, errno.Invalid
		}
	}
	return timerfd.Spec{
		Interval: uint64(intervalSec)*1_000_000_000 + uint64(intervalNs),
		Value:    uint64(valueSec)*1_000_000_000 + uint64(valueNs),
	}, ""
}

func (s Syscalls) writeITimerspec(ctx context.Context, address uint64, spec timerfd.Spec) error {
	var raw [itimerspecSize]byte
	binary.LittleEndian.PutUint64(raw[0:], spec.Interval/1_000_000_000)
	binary.LittleEndian.PutUint64(raw[8:], spec.Interval%1_000_000_000)
	binary.LittleEndian.PutUint64(raw[16:], spec.Value/1_000_000_000)
	binary.LittleEndian.PutUint64(raw[24:], spec.Value%1_000_000_000)
	if err := s.Memory.Write(ctx, address, raw[:]); err != nil {
		return err
	}
	return nil
}
