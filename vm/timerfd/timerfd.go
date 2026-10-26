// Package timerfd 实现 Linux timerfd(L10):把到期转化为可读事件
// 的描述符——一次与周期计时、过期计数、poll/epoll 就绪通知与
// 关闭唤醒,供事件循环使用。
//
// 到期由睡眠端口驱动:武装时启动后台 goroutine 按剩余时长睡眠,
// 到期计数加一并通知观察者;周期定时器按区间继续。虚拟时钟下
// 睡眠协作推进时间,与 clock_nanosleep 共享同一时间线。
package timerfd

import (
	"context"
	"encoding/binary"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// 时钟标识(clockid_t,asm-generic)。
const (
	ClockRealtime  = 0
	ClockMonotonic = 1
)

// Spec 是 itimerspec 的宿主形态(纳秒)。
type Spec struct {
	// Interval 是周期区间;0 表示一次性。
	Interval uint64
	// Value 是相对当前时刻的首次到期时长(arm 内换算)。
	Value uint64
}

// Options 是 timerfd_create 的标志。
type Options struct {
	NonBlock bool
}

// FD 是一个 timerfd:读取返回 8 字节过期计数(读清零)。
type FD struct {
	mu       sync.Mutex
	wake     wait.Queue
	clock    clock.Source
	clockID  uint64
	nonblock bool
	closed   bool

	// expiries 是未读的到期次数。
	expiries uint64
	// interval 是周期区间纳秒;0 = 一次性。
	interval uint64
	// deadline 是下次到期的单调时刻(纳秒);disarmed 时无意义。
	deadline uint64
	disarmed bool

	// cancel 停止当前到期 goroutine(重新武装或关闭时)。
	cancel context.CancelFunc

	// generation 随每次武装与到期清算递增:后台 ticker 捕获自己
	// 所属的代号,醒来发现代号已变(读到/查询已同步清算过期)即
	// 静默退出,避免双重计数。
	generation uint64

	// watchers 是就绪变化观察者(经 watcher 包装以便注销比对)。
	watchers []*watcher
}

type watcher struct{ run func() }

// New 创建 timerfd:clock 提供当前时刻与到期睡眠,必须非空。
func New(clockID uint64, options Options, clk clock.Source) (*FD, error) {
	if clk == nil {
		return nil, errno.Wrap(errno.Invalid, "timerfd 需要时钟端口")
	}
	if clockID != ClockRealtime && clockID != ClockMonotonic {
		return nil, errno.Wrap(errno.Invalid, "未知时钟标识")
	}
	return &FD{
		clock:    clk,
		clockID:  clockID,
		nonblock: options.NonBlock,
		disarmed: true,
	}, nil
}

// Arm 武装定时器:absolute 为真时 value 是所选时钟的绝对时刻
// (TFD_TIMER_ABSTIME),否则是相对时长;已过期或为零的绝对时刻
// 立即到期一次。Interval 为 0 表示一次性。返回旧武装状态。
func (f *FD) Arm(ctx context.Context, absolute bool, spec Spec) (Spec, error) {
	if err := ctx.Err(); err != nil {
		return Spec{}, err
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return Spec{}, errno.Wrap(errno.BadHandle, "timerfd 已关闭")
	}
	old, err := f.specLocked(ctx)
	if err != nil {
		f.mu.Unlock()
		return Spec{}, err
	}
	// 换算相对时长:绝对时刻减当前时刻;已过期立即到期一次
	// (与内核一致,ABSTIME 的零值即纪元=已过期)。解除武装只由
	// 相对模式的全零 value 表达。
	delay := spec.Value
	if absolute {
		now, err := f.nowLocked(ctx)
		if err != nil {
			f.mu.Unlock()
			return Spec{}, err
		}
		if spec.Value <= now {
			delay = 0
		} else {
			delay = spec.Value - now
		}
	}
	f.interval = spec.Interval
	f.disarmed = !absolute && spec.Value == 0
	f.expiries = 0
	f.rearmLocked(ctx, delay)
	f.mu.Unlock()
	f.wake.WakeAll(wait.Data)
	return old, nil
}

// rearmLocked 重启到期 goroutine;调用方持有锁。delay 为 0 表示
// 立即到期(同步计数,不启动 goroutine)。
func (f *FD) rearmLocked(ctx context.Context, delay uint64) {
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	if f.disarmed {
		return
	}
	now, err := f.nowLocked(ctx)
	if err != nil {
		return
	}
	f.deadline = now + delay
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	f.cancel = cancel
	if delay == 0 {
		// 立即到期:计数与通知在此完成(不派生 goroutine)。
		f.expiries++
		if f.interval > 0 {
			f.deadline = now + f.interval
			f.generation++
			go f.ticker(runCtx, f.generation, f.interval)
			return
		}
		f.disarmed = true
		f.notifyLocked()
		return
	}
	f.generation++
	go f.ticker(runCtx, f.generation, delay)
}

// reconcileLocked 按当前时刻同步清算已到期的期限:读取/就绪查询
// 因此是确定性的(到期一旦成为过去,查询必然看到),后台 ticker
// 迟到时经代号失效退出,不重复计数。周期定时器按整区间合并累计
// (与内核丢失周期合并语义一致),下次期限推进到未来。返回是否
// 发生到期;唤醒在锁内完成,观察者通知由调用方在锁外执行。
// 调用方持有锁。
func (f *FD) reconcileLocked(ctx context.Context) bool {
	if f.disarmed {
		return false
	}
	now, err := f.nowLocked(ctx)
	if err != nil || now < f.deadline {
		return false
	}
	defer f.wake.WakeAll(wait.Data)
	if f.interval == 0 {
		f.expiries++
		f.disarmed = true
		f.generation++
		return true
	}
	// periods:含本次在内已到期的区间数;下次期限严格落在未来。
	periods := (now-f.deadline)/f.interval + 1
	if f.expiries > ^uint64(0)-periods {
		f.expiries = ^uint64(0)
	} else {
		f.expiries += periods
	}
	f.deadline += periods * f.interval
	if f.deadline <= now {
		f.deadline = now + f.interval
	}
	f.generation++
	return true
}

// ticker 是到期循环:首段睡眠 delay,此后按区间重复;睡眠错误
// (关闭/取消)即退出。醒来时若代号已变(查询同步清算过期)则
// 静默退出。
func (f *FD) ticker(ctx context.Context, generation uint64, delay uint64) {
	for {
		if err := f.clock.Sleep(ctx, delay); err != nil {
			return
		}
		f.mu.Lock()
		if f.closed || f.generation != generation {
			f.mu.Unlock()
			return
		}
		f.expiries++
		if f.interval > 0 {
			delay = f.interval
			now, err := f.nowLocked(ctx)
			if err != nil {
				f.mu.Unlock()
				return
			}
			f.deadline = now + f.interval
			f.mu.Unlock()
			f.wake.WakeAll(wait.Data)
			f.notify()
			continue
		}
		f.disarmed = true
		f.mu.Unlock()
		f.wake.WakeAll(wait.Data)
		f.notify()
		return
	}
}

// Read 读取 8 字节过期计数并清零;无到期时阻塞(非阻塞 EAGAIN),
// 关闭返回 EBADF 语义错误。
func (f *FD) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) < 8 {
		return 0, errno.Invalid
	}
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, errno.Wrap(errno.BadHandle, "timerfd 已关闭")
		}
		f.reconcileLocked(context.Background())
		if f.expiries > 0 {
			count := f.expiries
			f.expiries = 0
			f.mu.Unlock()
			binary.LittleEndian.PutUint64(p[0:], count)
			return 8, nil
		}
		if f.nonblock {
			f.mu.Unlock()
			return 0, errno.WouldBlock
		}
		f.mu.Unlock()
		_, err := f.wake.Wait(context.Background(), func() (bool, wait.Reason, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.expiries > 0 || f.closed, wait.Data, nil
		})
		if err != nil {
			return 0, errno.Wrap(errno.Interrupted, "timerfd 等待被打断")
		}
	}
}

// GetTime 报告当前武装状态:Value 是距下次到期的剩余时长(已解除
// 武装为 0),Interval 是周期区间。
func (f *FD) GetTime(ctx context.Context) (Spec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return Spec{}, errno.Wrap(errno.BadHandle, "timerfd 已关闭")
	}
	f.reconcileLocked(ctx)
	return f.specLocked(ctx)
}

// specLocked 计算宿主形态的武装状态;调用方持有锁。
func (f *FD) specLocked(ctx context.Context) (Spec, error) {
	if f.disarmed {
		return Spec{}, nil
	}
	now, err := f.nowLocked(ctx)
	if err != nil {
		return Spec{}, err
	}
	remaining := uint64(0)
	if now < f.deadline {
		remaining = f.deadline - now
	}
	return Spec{Interval: f.interval, Value: remaining}, nil
}

// nowLocked 按所选时钟取当前时刻;调用方持有锁。
func (f *FD) nowLocked(ctx context.Context) (uint64, error) {
	if f.clockID == ClockMonotonic {
		return f.clock.Monotonic(ctx)
	}
	wall, err := f.clock.Wall(ctx)
	if err != nil {
		return 0, err
	}
	if wall < 0 {
		return 0, errno.Overflow
	}
	return uint64(wall), nil
}

// Close 停止到期循环并唤醒等待者;之后全部操作报 EBADF。
func (f *FD) Close() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errno.Wrap(errno.BadHandle, "timerfd 已关闭")
	}
	f.closed = true
	cancel := f.cancel
	f.cancel = nil
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	f.wake.WakeAll(wait.Hangup)
	return nil
}

// Ready 实现 poll.Source:有过期计数即可读;先按当前时刻清算
// 已到期期限,到期的读取因此确定可见。
func (f *FD) Ready() poll.Readiness {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0
	}
	f.reconcileLocked(context.Background())
	if f.expiries == 0 {
		return 0
	}
	return poll.Readable
}

// OnChange 订阅就绪变化(过期计数从无到有、关闭)。
func (f *FD) OnChange(fn func()) (cancel func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry := &watcher{run: fn}
	f.watchers = append(f.watchers, entry)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := f.watchers[:0]
		for _, candidate := range f.watchers {
			if candidate != entry {
				out = append(out, candidate)
			}
		}
		f.watchers = out
	}
}

// notifyLocked 在立即到期路径通知观察者(调用方持有锁,同步回调)。
func (f *FD) notifyLocked() {
	for _, entry := range f.watchers {
		entry.run()
	}
}

// notify 异步到期路径通知观察者(锁外调用)。
func (f *FD) notify() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range f.watchers {
		entry.run()
	}
}

// Write 实现 fd.Stream:timerfd 不支持写,报 EINVAL。
func (f *FD) Write([]byte) (int, error) { return 0, errno.Invalid }
