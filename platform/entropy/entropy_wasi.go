//go:build wasip1

package entropy

import (
	"context"
	"crypto/rand"
)

// Production 在 WASI 客体内经 random_get 主机调用读取
// 宿主密码学随机源。它不使用固定种子;确定性场景应显式
// 选择 vm/entropy 的 Seeded 或 Recording 源。
type Production struct{}

// NewProduction 返回 WASI 随机源。
func NewProduction() *Production { return &Production{} }

func (Production) Read(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	return rand.Read(p)
}
