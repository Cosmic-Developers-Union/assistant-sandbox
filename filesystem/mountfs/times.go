package mountfs

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func (m *Mounts) SetTimes(ctx context.Context, name string, atime, mtime int64) error {
	return m.SetTimesFollow(ctx, name, atime, mtime, true)
}

// SetTimesFollow 把时间更新委托给目标挂载;不跟随符号链接时要求
// 底层文件系统实现 LinkTimestamps。
func (m *Mounts) SetTimesFollow(ctx context.Context, name string, atime, mtime int64, follow bool) error {
	entry, err := m.resolve(name)
	if err != nil {
		return err
	}
	target := m.relative(entry, name)
	if setter, ok := entry.sys.(vmfs.LinkTimestamps); ok {
		return setter.SetTimesFollow(ctx, target, atime, mtime, follow)
	}
	if follow {
		if setter, ok := entry.sys.(vmfs.Timestamps); ok {
			return setter.SetTimes(ctx, target, atime, mtime)
		}
	}
	return errno.Wrap(errno.NotSupported, "挂载 "+entry.point+" 的文件系统不支持时间更新")
}

var (
	_ vmfs.Timestamps     = (*Mounts)(nil)
	_ vmfs.LinkTimestamps = (*Mounts)(nil)
)
