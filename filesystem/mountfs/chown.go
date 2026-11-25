package mountfs

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// Chown 把属主更新委托给目标挂载的文件系统。
func (m *Mounts) Chown(ctx context.Context, name string, uid, gid int64) error {
	entry, err := m.resolve(name)
	if err != nil {
		return err
	}
	ownership, ok := entry.sys.(vmfs.Ownership)
	if !ok {
		return errno.Wrap(errno.NotSupported, "挂载 "+entry.point+" 的文件系统不支持 chown")
	}
	return ownership.Chown(ctx, m.relative(entry, name), uid, gid)
}

var _ vmfs.Ownership = (*Mounts)(nil)
