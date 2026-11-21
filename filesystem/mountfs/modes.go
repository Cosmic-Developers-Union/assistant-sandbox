package mountfs

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// Chmod 把权限更新委托给目标挂载的文件系统(I13)。
func (m *Mounts) Chmod(ctx context.Context, name string, mode stdfs.FileMode) error {
	entry, err := m.resolve(name)
	if err != nil {
		return err
	}
	modes, ok := entry.sys.(vmfs.Modes)
	if !ok {
		return errno.Wrap(errno.NotSupported, "挂载 "+entry.point+" 的文件系统不支持 chmod")
	}
	return modes.Chmod(ctx, m.relative(entry, name), mode)
}
