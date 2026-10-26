package timerfd

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const waitTimeout = 5 * time.Second

// realClock 是真实时间时钟:与宿主 clock.Real 同语义,供计时测试
// 使用(虚拟时钟会立即推进到最早到期,不适合验证真实时长)。
type realClock struct{ start time.Time }

func newRealClock() realClock { return realClock{start: time.Now()} }

func (c realClock) Monotonic(context.Context) (uint64, error) {
	return uint64(time.Since(c.start)), nil
}

func (c realClock) Wall(context.Context) (int64, error) { return time.Now().UnixNano(), nil }

func (c realClock) Sleep(ctx context.Context, nanoseconds uint64) error {
	timer := time.NewTimer(time.Duration(nanoseconds))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestTimerfdOneShotExpires 验证 L10:一次性计时——settime 后到期,
// 读取返回 1 并清零;再读(阻塞)前被 GetTime 报告解除武装。
func TestTimerfdOneShotExpires(t *testing.T) {
	clk := newRealClock()
	fd, err := New(ClockMonotonic, Options{NonBlock: true}, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	if _, err := fd.Arm(context.Background(), false, Spec{Value: 50 * 1e6}); err != nil {
		t.Fatal(err)
	}
	// 到期前:GetTime 报剩余,Ready 无,非阻塞读 EAGAIN。
	spec, err := fd.GetTime(context.Background())
	if err != nil || spec.Value == 0 || spec.Value > 50*1e6 || spec.Interval != 0 {
		t.Fatalf("剩余=%+v err=%v", spec, err)
	}
	if fd.Ready() != 0 {
		t.Fatal("到期前不应就绪")
	}
	if _, err := fd.Read(make([]byte, 8)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("非阻塞读 err=%v want EAGAIN", err)
	}
	// 到期:Ready 置位,读取 1,读后清零。
	deadline := time.After(waitTimeout)
	for fd.Ready() == 0 {
		select {
		case <-deadline:
			t.Fatal("到期未发生")
		case <-time.After(time.Millisecond):
		}
	}
	buffer := make([]byte, 8)
	n, err := fd.Read(buffer)
	if err != nil || n != 8 || binary.LittleEndian.Uint64(buffer) != 1 {
		t.Fatalf("读取 n=%d value=%d err=%v want 1", n, binary.LittleEndian.Uint64(buffer), err)
	}
	if fd.Ready() != 0 {
		t.Fatal("读取后不应再就绪")
	}
	// 一次性到期后 GetTime 报解除武装。
	spec, err = fd.GetTime(context.Background())
	if err != nil || spec.Value != 0 || spec.Interval != 0 {
		t.Fatalf("解除武装=%+v err=%v", spec, err)
	}
}

// TestTimerfdPeriodicRearms 验证 L10:周期计时——区间重复到期,
// 单次读取返回累计过期次数(读清零)。
func TestTimerfdPeriodicRearms(t *testing.T) {
	clk := newRealClock()
	fd, err := New(ClockMonotonic, Options{}, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	// 40ms 首次,20ms 周期;等 110ms 应有 4 次过期(40/60/80/100)。
	if _, err := fd.Arm(context.Background(), false, Spec{Value: 40 * 1e6, Interval: 20 * 1e6}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(110 * time.Millisecond)
	buffer := make([]byte, 8)
	if _, err := fd.Read(buffer); err != nil || binary.LittleEndian.Uint64(buffer) != 4 {
		t.Fatalf("周期过期=%d err=%v want 4", binary.LittleEndian.Uint64(buffer), err)
	}
	if fd.Ready() != 0 {
		t.Fatal("读后不应再就绪")
	}
}

// TestTimerfdAbsoluteTime 验证 L10:ABSTIME 语义——绝对时刻已过期
// 立即到期一次;未来时刻按差值计时。
func TestTimerfdAbsoluteTime(t *testing.T) {
	clk := newRealClock()
	fd, err := New(ClockMonotonic, Options{}, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	ctx := context.Background()
	now, err := clk.Monotonic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 过去时刻:立即到期。
	if _, err := fd.Arm(ctx, true, Spec{Value: now - 1}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	if _, err := fd.Read(buffer); err != nil || binary.LittleEndian.Uint64(buffer) != 1 {
		t.Fatalf("过期绝对时刻=%d err=%v want 1", binary.LittleEndian.Uint64(buffer), err)
	}
	// 未来时刻:相对差值到期。
	now, _ = clk.Monotonic(ctx)
	if _, err := fd.Arm(ctx, true, Spec{Value: now + 20*1e6}); err != nil {
		t.Fatal(err)
	}
	spec, err := fd.GetTime(ctx)
	if err != nil || spec.Value == 0 || spec.Value > 20*1e6 {
		t.Fatalf("绝对剩余=%+v err=%v", spec, err)
	}
	deadline := time.After(waitTimeout)
	for fd.Ready() == 0 {
		select {
		case <-deadline:
			t.Fatal("绝对时刻未到期")
		case <-time.After(time.Millisecond):
		}
	}
}

// TestTimerfdDisarmAndClose 验证 L10:全零 value 解除武装;关闭唤醒
// 阻塞读并使后续操作报 EBADF。
func TestTimerfdDisarmAndClose(t *testing.T) {
	clk := newRealClock()
	fd, err := New(ClockMonotonic, Options{}, clk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fd.Arm(context.Background(), false, Spec{Value: 50 * 1e6}); err != nil {
		t.Fatal(err)
	}
	if _, err := fd.Arm(context.Background(), false, Spec{}); err != nil {
		t.Fatal(err)
	}
	spec, err := fd.GetTime(context.Background())
	if err != nil || spec.Value != 0 || spec.Interval != 0 {
		t.Fatalf("解除武装=%+v err=%v", spec, err)
	}
	// 阻塞读被关闭唤醒。
	errs := make(chan error, 1)
	go func() {
		_, err := fd.Read(make([]byte, 8))
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := fd.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, errno.BadHandle) {
			t.Fatalf("关闭后读 err=%v want EBADF", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("关闭未唤醒阻塞读")
	}
	if err := fd.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("重复关闭 err=%v", err)
	}
	if _, err := fd.Arm(context.Background(), false, Spec{Value: 1}); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后武装 err=%v", err)
	}
	if _, err := fd.GetTime(context.Background()); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后查询 err=%v", err)
	}
}

// TestTimerfdOnChangeNotifies 验证 L10:就绪变化通知(过期时与关闭时)。
func TestTimerfdOnChangeNotifies(t *testing.T) {
	clk := newRealClock()
	fd, err := New(ClockMonotonic, Options{}, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	changes := make(chan struct{}, 4)
	cancel := fd.OnChange(func() { changes <- struct{}{} })
	defer cancel()
	if _, err := fd.Arm(context.Background(), false, Spec{Value: 10 * 1e6}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(waitTimeout):
		t.Fatal("过期未通知观察者")
	}
}

// TestTimerfdValidatesClock 验证 L10:未知时钟标识 EINVAL;短缓冲
// 读取 EINVAL。
func TestTimerfdValidatesClock(t *testing.T) {
	clk := newRealClock()
	if _, err := New(7, Options{}, clk); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知时钟 err=%v want EINVAL", err)
	}
	if _, err := New(ClockRealtime, Options{}, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺时钟 err=%v want EINVAL", err)
	}
	fd, err := New(ClockRealtime, Options{}, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	if _, err := fd.Read(make([]byte, 4)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("短缓冲 err=%v want EINVAL", err)
	}
}
