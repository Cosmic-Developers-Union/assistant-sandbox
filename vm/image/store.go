// store.go 实现镜像发布槽(N12):更新前校验版本/架构/ABI/入口与
// 文件摘要,失败不替换当前可用镜像;执行前可再次校验活动镜像。
package image

import (
	"context"
	"fmt"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// Store 是原子镜像发布槽:Active 始终指向最近一次校验通过的镜像。
type Store struct {
	mu     sync.RWMutex
	active Image
}

// NewStore 以已校验镜像初始化发布槽。
func NewStore(ctx context.Context, initial Image, requirements Requirements) (*Store, error) {
	if err := Verify(ctx, initial, requirements); err != nil {
		return nil, fmt.Errorf("初始镜像不可用: %w", err)
	}
	return &Store{active: initial}, nil
}

// Active 返回当前可用镜像。
func (s *Store) Active() Image {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// Update 校验候选镜像并原子替换;校验失败时现有镜像保持不变。
func (s *Store) Update(ctx context.Context, candidate Image, requirements Requirements) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if candidate == nil {
		return fmt.Errorf("缺少候选镜像: %w", errno.Invalid)
	}
	if err := Verify(ctx, candidate, requirements); err != nil {
		return fmt.Errorf("拒绝镜像更新: %w", err)
	}
	s.mu.Lock()
	s.active = candidate
	s.mu.Unlock()
	return nil
}

// VerifyActive 在执行前复核当前镜像摘要与兼容性。
func (s *Store) VerifyActive(ctx context.Context, requirements Requirements) error {
	if s == nil || s.Active() == nil {
		return fmt.Errorf("没有可用镜像: %w", errno.Invalid)
	}
	return Verify(ctx, s.Active(), requirements)
}
