package clock

import "time"

// processStart 是进程内单调计时的起点。
var processStart = time.Now()

// MonotonicNanos 返回宿主单调时钟的纳秒读数,仅用于测量耗时
// (如 G08 的系统调用轨迹)。它不是客体可见时间:客体的时钟语义
// 一律经 Clock 端口注入的来源提供(见 Clock 接口),此处读数不参与
// 任何客体可观察行为,也不进入确定性重放记录。
func MonotonicNanos() uint64 { return uint64(time.Since(processStart)) }
