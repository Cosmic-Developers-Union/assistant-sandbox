package clock

import (
	"context"
)

// Real 是生产时钟来源(L01):实时时钟取系统时间,单调时钟取宿主单调
// 读数——只增不减,系统时间调整既不会让单调时间回退,也不会改变
// 相对等待期限(期限一律以单调时钟计算)。
type Real struct{}

// NewReal 创建生产时钟来源。
func NewReal() *Real { return &Real{} }

// Wall 返回系统实时时钟的纳秒读数;系统时间调整会使其前后跳变。
func (r *Real) Wall(context.Context) (int64, error) { return hostWallNanos(), nil }

// Monotonic 返回单调时钟的纳秒读数,保证不小于上一次读数。
func (r *Real) Monotonic(context.Context) (uint64, error) { return hostMonotonicNanos(), nil }

// Sleep 按单调时钟等待给定纳秒;上下文取消即返回。
func (r *Real) Sleep(ctx context.Context, nanoseconds uint64) error {
	return hostSleep(ctx, nanoseconds)
}

var _ Source = (*Real)(nil)
