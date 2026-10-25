package common

import (
	"context"
	"encoding/binary"
	"math"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/futex"
)

// futex(K05):WAIT/WAKE 两个基础操作。
//
//   - futex(uaddr, WAIT, expected, timeout, …):客体字与 expected
//     不等立即 EAGAIN;相等阻塞到唤醒(0)、超时(ETIMEDOUT)或
//     待投递信号(EINTR,J09 契约);uaddr 必须 4 字节对齐;
//   - futex(uaddr, WAKE, count, …):唤醒至多 count 个等待者并返回
//     实际唤醒数,无等待者返回 0;
//   - FUTEX_PRIVATE_FLAG 被接受:等待表按进程键控,两种形式同语义
//     (跨地址空间共享随 E08/E09 共享映射落地);
//   - 其余操作(requeue、bitset、PI、waitv)随 K06 逐项接入,
//     当前按未知操作报 EINVAL;FUTEX_CLOCK_REALTIME 只用于
//     WAIT_BITSET 族,WAIT/WAKE 携带时报 EINVAL(与内核一致)。

// asm-generic 的 futex 操作与标志。
const (
	futexOpWait        = 0
	futexOpWake        = 1
	futexPrivateFlag   = 128
	futexClockRealtime = 256
)

// Futexes 是 futex 的同步端口(K05):Wait 原子地校验并阻塞,
// Wake 按先进先出唤醒。*futex.Table 原生实现。
type Futexes interface {
	Wait(ctx context.Context, key futex.Key, expected uint32, load func() (uint32, bool)) error

	Wake(key futex.Key, count int) int
}

// futexTimeout 解析 WAIT 的相对超时:NULL 表示无限;负值或纳秒
// 越界 EINVAL。
func (s Syscalls) futexTimeout(ctx context.Context, address uint64) (uint64, bool, errno.Code) {
	if address == 0 {
		return 0, true, ""
	}
	nanoseconds, _, code := s.pselectTimeout(ctx, address)
	if code != "" {
		return 0, false, code
	}
	return nanoseconds, false, ""
}

func (s Syscalls) futex(ctx context.Context, call Call) Reply {
	if s.Futexes == nil || s.Memory == nil || s.Identity == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0]&3 != 0 {
		// futex 字要求 4 字节对齐(内核语义)。
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[1]&futexClockRealtime != 0 {
		// 实时钟只与 WAIT_BITSET 族组合(WAIT/WAKE 拒绝,内核一致)。
		return Reply{Errno: errno.Invalid}
	}
	operation := call.Args[1] &^ futexPrivateFlag
	key := futex.Key{Owner: s.Identity.PID, Address: call.Args[0]}
	switch operation {
	case futexOpWake:
		count := call.Args[2]
		if count > math.MaxInt32 {
			count = math.MaxInt32 // glibc 以 INT_MAX 表达全部唤醒
		}
		return Reply{Value: uint64(s.Futexes.Wake(key, int(count)))}
	case futexOpWait:
		return s.futexWait(ctx, call, key)
	default:
		// requeue/bitset/PI/waitv 随 K06;未知操作与内核一致 EINVAL。
		return Reply{Errno: errno.Invalid}
	}
}

// futexWait 执行 FUTEX_WAIT:立即校验值(不匹配 EAGAIN),随后
// 阻塞到唤醒、超时或待投递信号。超时切片轮询与 epoll_pwait 同型。
func (s Syscalls) futexWait(ctx context.Context, call Call, key futex.Key) Reply {
	timeout, forever, code := s.futexTimeout(ctx, call.Args[3])
	if code != "" {
		return Reply{Errno: code}
	}
	// futexOutcome 归并等待结果:唤醒(nil)成功,错误转 errno。
	futexOutcome := func(err error) Reply {
		if err == nil {
			return Reply{}
		}
		return Reply{Errno: errnoOf(err)}
	}
	load := func() (uint32, bool) {
		var raw [4]byte
		if err := s.Memory.Read(ctx, key.Address, raw[:]); err != nil {
			return 0, false
		}
		return binary.LittleEndian.Uint32(raw[:]), true
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- s.Futexes.Wait(waitCtx, key, uint32(call.Args[2]), load)
	}()
	// 立即结果先行:值不匹配(EAGAIN)或不可读(EFAULT)不进入轮询。
	select {
	case err := <-done:
		return futexOutcome(err)
	default:
	}
	if forever && s.Interrupts == nil {
		// 无信号端口且无限等待:阻塞到唤醒或上下文结束。
		return futexOutcome(<-done)
	}
	if !forever && s.Sleeper == nil {
		cancel()
		<-done
		return Reply{Errno: errno.Interrupted}
	}
	remaining := timeout
	for {
		if s.Interrupts != nil {
			if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
				cancel()
				<-done
				return Reply{Errno: errno.Interrupted}
			}
		}
		if forever {
			// 无限等待:按切片睡眠感知信号,不忙循环。
			if err := s.Sleeper.Sleep(ctx, sleepSlice); err != nil {
				cancel()
				if woken := <-done; woken == nil {
					return Reply{}
				}
				return Reply{Errno: errno.Interrupted}
			}
			select {
			case err := <-done:
				return futexOutcome(err)
			default:
			}
			continue
		}
		step := sleepSlice
		if remaining < step {
			step = remaining
		}
		if err := s.Sleeper.Sleep(ctx, step); err != nil && ctx.Err() == nil {
			// 睡眠端口非取消错误:阻塞等待唤醒或取消,不再计时。
			return futexOutcome(<-done)
		}
		remaining -= step
		select {
		case err := <-done:
			return Reply{Errno: errnoOf(err)}
		default:
		}
		if remaining == 0 {
			cancel()
			if err := <-done; err == nil {
				return Reply{} // 唤醒与到期竞态,唤醒优先
			}
			return Reply{Errno: errno.TimedOut}
		}
	}
}
