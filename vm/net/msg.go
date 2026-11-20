package net

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// WriteMsg 带控制消息写入(M08):数据按缓冲序列拼接;SCM_RIGHTS
// 附着于本次写入的起始流位置。非 AF_UNIX 或无控制消息时与普通写
// 等价。仅 AF_UNIX 支持 SCM_RIGHTS(Linux 语义)。
func (c *streamConn) WriteMsg(ctx context.Context, buffers [][]byte, rights *ControlRights) (int, error) {
	if rights != nil {
		if c.local.Domain == AFUnix || c.remote.Domain == AFUnix {
			// 控制消息登记在锁内与写入同批完成。
			return c.writeWithControl(ctx, buffers, rights)
		}
		return 0, errno.Invalid
	}
	total := 0
	for _, buffer := range buffers {
		n, err := c.WriteContext(ctx, buffer)
		total += n
		if err != nil {
			return total, err
		}
		if n < len(buffer) {
			break
		}
	}
	return total, nil
}

// writeWithControl 在锁内原子完成"控制登记+数据写入"。
func (c *streamConn) writeWithControl(ctx context.Context, buffers [][]byte, rights *ControlRights) (int, error) {
	c.tx.mu.Lock()
	position := c.tx.written
	c.tx.mu.Unlock()
	handles := make([]any, len(rights.Handles))
	copy(handles, rights.Handles)
	total := 0
	for _, buffer := range buffers {
		n, err := c.WriteContext(ctx, buffer)
		total += n
		if err != nil {
			releaseRights(rights)
			return total, err
		}
		if n < len(buffer) {
			releaseRights(rights)
			break
		}
	}
	if total > 0 {
		c.tx.mu.Lock()
		c.tx.controls = append(c.tx.controls, attachedControl{position: position, rights: ControlRights{Handles: handles, Release: rights.Release}})
		c.tx.mu.Unlock()
	} else {
		releaseRights(rights)
	}
	return total, nil
}

// ReadMsg 读取并交付流位置覆盖的控制消息(M08):读取区间
// [consumed, consumed+n) 内附着的 SCM_RIGHTS 随本次读取交付;
// 零长度读取交付位置恰为当前读取位置的排头控制。
func (c *streamConn) ReadMsg(ctx context.Context, buffer []byte) (int, *ControlRights, error) {
	c.mu.Lock()
	closed := c.closed || c.readOff
	c.mu.Unlock()
	if closed {
		return 0, nil, errno.BadHandle
	}
	c.rx.mu.Lock()
	delivered := c.rx.takeControlsLocked(uint64(len(buffer)))
	c.rx.mu.Unlock()
	n, err := c.ReadContext(ctx, buffer)
	if err != nil && n == 0 {
		for index := range delivered {
			releaseRights(&delivered[index])
		}
		return 0, nil, err
	}
	var rights *ControlRights
	if len(delivered) > 0 {
		handles := make([]any, 0)
		for _, control := range delivered {
			handles = append(handles, control.Handles...)
		}
		rights = &ControlRights{Handles: handles}
	}
	return n, rights, err
}

// takeControlsLocked 取出读取区间覆盖的控制消息;调用方持有 rx 锁。
func (c *streamCore) takeControlsLocked(length uint64) []ControlRights {
	if len(c.controls) == 0 {
		return nil
	}
	start := c.consumed
	end := c.consumed + length
	var out []ControlRights
	remaining := c.controls[:0]
	for _, control := range c.controls {
		covered := control.position >= start && control.position < end
		if !covered && length == 0 && control.position == start {
			covered = true
		}
		if covered {
			out = append(out, control.rights)
		} else {
			remaining = append(remaining, control)
		}
	}
	c.controls = remaining
	return out
}
