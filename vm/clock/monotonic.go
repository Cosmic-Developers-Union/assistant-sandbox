package clock

import (
	"context"
	"math"
	"time"
)

// processStart 是进程内单调计时的起点。
var processStart = time.Now()

// hostWallNanos 返回系统实时时钟的纳秒读数。实时时钟可能因系统时间
// 调整前后跳变,因此只用于客体的 CLOCK_REALTIME 语义,绝不参与相对
// 等待期限的计算。
func hostWallNanos() int64 { return time.Now().UnixNano() }

// hostMonotonicNanos 返回宿主单调时钟的纳秒读数:只增不减,不受系统
// 时间调整影响,用于相对等待期限。
func hostMonotonicNanos() uint64 { return uint64(time.Since(processStart)) }

// hostSleep 按纳秒等待,可被上下文取消打断。
func hostSleep(ctx context.Context, nanoseconds uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if nanoseconds == 0 {
		return nil
	}
	if nanoseconds > uint64(math.MaxInt64) {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(time.Duration(nanoseconds))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// MonotonicNanos 返回宿主单调时钟的纳秒读数,仅用于测量耗时
// (如 G08 的系统调用轨迹)。它不是客体可见时间:客体的时钟语义
// 一律经 Clock 端口注入的来源提供(见 Clock 接口),此处读数不参与
// 任何客体可观察行为,也不进入确定性重放记录。
func MonotonicNanos() uint64 { return hostMonotonicNanos() }
