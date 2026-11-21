package image

import (
	"context"
	"fmt"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// Catalog 是显式镜像选择入口(R03):只返回已登记的镜像并先做完整性
// 与兼容性校验;不隐式下载,缺失镜像在启动前报错。
type Catalog struct {
	mu     sync.RWMutex
	images map[string]Image
}

func NewCatalog() *Catalog { return &Catalog{images: map[string]Image{}} }

// Add 登记一个命名镜像(不做 I/O;校验发生在 Select)。
func (c *Catalog) Add(name string, img Image) error {
	if name == "" || img == nil {
		return errno.Wrap(errno.Invalid, "镜像名称与内容不能为空")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.images[name]; exists {
		return errno.Wrap(errno.Exist, "镜像已登记: "+name)
	}
	c.images[name] = img
	return nil
}

// Select 按名称选择镜像并在返回前校验版本/架构/ABI/入口与摘要。
func (c *Catalog) Select(ctx context.Context, name string, requirements Requirements) (Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	img := c.images[name]
	c.mu.RUnlock()
	if img == nil {
		return nil, fmt.Errorf("镜像 %q 未登记: %w", name, errno.NotFound)
	}
	if err := Verify(ctx, img, requirements); err != nil {
		return nil, fmt.Errorf("镜像 %q 不可用: %w", name, err)
	}
	return img, nil
}
