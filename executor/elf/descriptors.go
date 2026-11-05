package elf

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// descriptorsHandle 是描述符查询视图:socket 端口经此回查既有 fd 上的
// 端点对象。
type descriptorsHandle struct {
	table fd.Descriptors
	pid   uint64
}

func (d descriptorsHandle) Get(ctx context.Context, fd uint64) (any, error) {
	if fd > 0xFFFFFFFF {
		return nil, errno.BadHandle
	}
	return d.table.Get(ctx, d.pid, uint32(fd))
}
