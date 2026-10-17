package mountfs

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func (m *Mounts) Mkfifo(ctx context.Context, name string, mode stdfs.FileMode) error {
	entry, err := m.resolve(name)
	if err != nil {
		return err
	}
	fifos, ok := entry.sys.(vmfs.Fifos)
	if !ok {
		return errno.Wrap(errno.NotSupported, "挂载 "+entry.point+" 的文件系统不能创建命名管道")
	}
	return fifos.Mkfifo(ctx, m.relative(entry, name), mode)
}

var _ vmfs.Fifos = (*Mounts)(nil)
