//go:build !wasip1

package entropy

import (
	"context"
	"crypto/rand"
)

// Production 从宿主操作系统的密码学随机源读取字节。
// 它不使用固定种子,跨 VM 实例与跨进程都不可预测;
// 确定性场景(录制重放、回归测试)应显式选择 vm/entropy 的
// Seeded 或 Recording 源。
type Production struct{}

// NewProduction 返回宿主密码学随机源。
func NewProduction() *Production { return &Production{} }

func (Production) Read(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	// crypto/rand.Read 保证填满或报错;开始后不可取消,
	// 与内核阻塞读随机数的语义一致。
	return rand.Read(p)
}
