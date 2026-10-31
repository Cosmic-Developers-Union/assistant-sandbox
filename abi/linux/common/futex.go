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
//   - FUTEX_PRIVATE_FLAG 被接受:等待表按线程组键控,两种形式同语义
//     (跨地址空间共享随 E08/E09 共享映射落地);
//   - 其余操作(requeue、bitset、PI、waitv)随 K06 逐项接入,
//     当前按未知操作报 EINVAL;FUTEX_CLOCK_REALTIME 只用于
//     WAIT_BITSET 族,WAIT/WAKE 携带时报 EINVAL(与内核一致)。

// asm-generic 的 futex 操作与标志。
const (
	futexOpWait        = 0
	futexOpWake        = 1
	futexOpRequeue     = 3
	futexOpCmpRequeue  = 4
	futexOpWakeBitset  = 10
	futexOpWaitBitset  = 9
	futexPrivateFlag   = 128
	futexClockRealtime = 256

	// futexBitsetMask 是操作号的低 6 位(位集操作共享编号空间)。
	futexOpMask = 0x3F
)

// Futexes 是 futex 的同步端口(K05/K06):Wait/WaitBitset 原子地
// 校验并阻塞,Wake/WakeBitset 按先进先出唤醒,Requeue 迁移等待者。
// *futex.Table 原生实现。
type Futexes interface {
	Wait(ctx context.Context, key futex.Key, expected uint32, load func() (uint32, bool)) error

	WaitBitset(ctx context.Context, key futex.Key, expected uint32, load func() (uint32, bool), bitset uint32) error

	Wake(key futex.Key, count int) int

	WakeBitset(key futex.Key, count int, bitset uint32) int

	Requeue(src, dst futex.Key, wakeCount, requeueCount int) futex.RequeueOutcome
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

// futexKey 构造 futex 等待键(K02/K10):私有地址按线程组键控
// (同组互唤醒,跨地址空间隔离);共享后备区间(文件共享映射)按
// 后备标识加盐——fork 父子持同一后备,跨进程互唤醒。
func (s Syscalls) futexKey(address uint64) futex.Key {
	if shared, ok := s.Memory.(SharedKeyer); ok {
		if salt, has := shared.SharedKeyAt(address); has {
			return futex.Key{Owner: salt, Address: address}
		}
	}
	owner := s.Identity.PID
	if s.Identity.TGID != 0 {
		owner = s.Identity.TGID
	}
	return futex.Key{Owner: owner, Address: address}
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
	key := s.futexKey(call.Args[0])
	switch operation & futexOpMask {
	case futexOpWake:
		count := call.Args[2]
		if count > math.MaxInt32 {
			count = math.MaxInt32 // glibc 以 INT_MAX 表达全部唤醒
		}
		return Reply{Value: uint64(s.Futexes.Wake(key, int(count)))}
	case futexOpWait:
		return s.futexWait(ctx, call, key)
	case futexOpRequeue, futexOpCmpRequeue:
		return s.futexRequeue(ctx, call, key, operation&futexOpMask == futexOpCmpRequeue)
	case futexOpWaitBitset:
		return s.futexWaitBitset(ctx, call, key)
	case futexOpWakeBitset:
		count := call.Args[2]
		if count > math.MaxInt32 {
			count = math.MaxInt32
		}
		bitset := uint32(call.Args[4])
		if bitset == 0 {
			// 位集 0 无意义,与内核一致 EINVAL。
			return Reply{Errno: errno.Invalid}
		}
		return Reply{Value: uint64(s.Futexes.WakeBitset(key, int(count), bitset))}
	default:
		// PI 族/等待向量等随 K07 及后续逐项登记;未知操作与内核一致 EINVAL。
		return Reply{Errno: errno.Invalid}
	}
}

// futexRequeue 实现 FUTEX_REQUEUE/CMP_REQUEUE:唤醒 val(args[2])个,
// 其余至多 val2(args[3],经 timeout 槽传递)个迁移到 uaddr2(args[4]);
// CMP 变体先校验 *uaddr == val3(args[5])。
func (s Syscalls) futexRequeue(ctx context.Context, call Call, key futex.Key, compare bool) Reply {
	target := s.futexKey(call.Args[4])
	if compare {
		expected := uint32(call.Args[5])
		var raw [4]byte
		if err := s.Memory.Read(ctx, key.Address, raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		if binary.LittleEndian.Uint32(raw[:]) != expected {
			return Reply{Errno: errno.WouldBlock}
		}
	}
	wakeCount := call.Args[2]
	if wakeCount > math.MaxInt32 {
		wakeCount = math.MaxInt32
	}
	requeueCount := call.Args[3]
	if requeueCount > math.MaxInt32 {
		requeueCount = math.MaxInt32
	}
	outcome := s.Futexes.Requeue(key, target, int(wakeCount), int(requeueCount))
	return Reply{Value: uint64(outcome.Woken + outcome.Requeued)}
}

// futexWaitBitset 实现 FUTEX_WAIT_BITSET:超时是所选时钟的绝对时刻
// (无 FUTEX_CLOCK_REALTIME 时为 CLOCK_MONOTONIC);已过期立即
// ETIMEDOUT;args[4] 是位集,0 EINVAL。
func (s Syscalls) futexWaitBitset(ctx context.Context, call Call, key futex.Key) Reply {
	bitset := uint32(call.Args[4])
	if bitset == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[3] == 0 {
		// 无超时:无限等待。
		return s.futexAwait(ctx, call, key, bitset, func() (uint64, bool, errno.Code) { return 0, false, "" })
	}
	deadline, _, code := s.pselectTimeout(ctx, call.Args[3])
	if code != "" {
		return Reply{Errno: code}
	}
	if s.Clock == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var now uint64
	var err error
	if call.Args[1]&futexClockRealtime != 0 {
		var wall int64
		wall, err = s.Clock.Wall(ctx)
		if err == nil && wall >= 0 {
			now = uint64(wall)
		}
	} else {
		now, err = s.Clock.Monotonic(ctx)
	}
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if deadline <= now {
		return Reply{Errno: errno.TimedOut}
	}
	return s.futexAwait(ctx, call, key, bitset, func() (uint64, bool, errno.Code) {
		var current uint64
		var cerr error
		if call.Args[1]&futexClockRealtime != 0 {
			var wall int64
			wall, cerr = s.Clock.Wall(ctx)
			if cerr == nil && wall >= 0 {
				current = uint64(wall)
			}
		} else {
			current, cerr = s.Clock.Monotonic(ctx)
		}
		if cerr != nil {
			return 0, false, errnoOf(cerr)
		}
		if current >= deadline {
			return 0, true, ""
		}
		return deadline - current, true, ""
	})
}

// futexWait 执行 FUTEX_WAIT:立即校验值(不匹配 EAGAIN),随后
// 阻塞到唤醒、超时或待投递信号。超时切片轮询与 epoll_pwait 同型。
func (s Syscalls) futexWait(ctx context.Context, call Call, key futex.Key) Reply {
	timeout, forever, code := s.futexTimeout(ctx, call.Args[3])
	if code != "" {
		return Reply{Errno: code}
	}
	return s.futexAwait(ctx, call, key, futex.BitsetMatchAny, func() (uint64, bool, errno.Code) {
		if forever {
			return 0, false, ""
		}
		return timeout, true, ""
	})
}

// futexAwait 是 WAIT/WAIT_BITSET 的公共阻塞路径:立即校验值(不匹配
// EAGAIN、不可读 EFAULT),随后阻塞到唤醒、剩余时长(remaining 回调
// 返回剩余纳秒;expired 为真表示已到期;code 非空是查询失败)耗尽或
// 待投递信号。bitset 透传核心。
func (s Syscalls) futexAwait(ctx context.Context, call Call, key futex.Key, bitset uint32, remaining func() (uint64, bool, errno.Code)) Reply {
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
		done <- s.Futexes.WaitBitset(waitCtx, key, uint32(call.Args[2]), load, bitset)
	}()
	// 立即结果先行:值不匹配(EAGAIN)或不可读(EFAULT)不进入轮询。
	select {
	case err := <-done:
		return futexOutcome(err)
	default:
	}
	leftover, timed, queryCode := remaining()
	if queryCode != "" {
		cancel()
		<-done
		return Reply{Errno: queryCode}
	}
	if timed && leftover == 0 {
		// 已到期(WAIT_BITSET 过去时刻):立即超时,等待者取消。
		cancel()
		if err := <-done; err == nil {
			return Reply{} // 唤醒与到期竞态,唤醒优先
		}
		return Reply{Errno: errno.TimedOut}
	}
	if !timed && s.Interrupts == nil {
		// 无信号端口且无限等待:阻塞到唤醒或上下文结束。
		return futexOutcome(<-done)
	}
	if timed && s.Sleeper == nil {
		cancel()
		<-done
		return Reply{Errno: errno.Interrupted}
	}
	timeout := leftover
	forever := !timed
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
		if timeout < step {
			step = timeout
		}
		if err := s.Sleeper.Sleep(ctx, step); err != nil && ctx.Err() == nil {
			// 睡眠端口非取消错误:阻塞等待唤醒或取消,不再计时。
			return futexOutcome(<-done)
		}
		timeout -= step
		select {
		case err := <-done:
			return Reply{Errno: errnoOf(err)}
		default:
		}
		if timeout == 0 {
			cancel()
			if err := <-done; err == nil {
				return Reply{} // 唤醒与到期竞态,唤醒优先
			}
			return Reply{Errno: errno.TimedOut}
		}
	}
}
