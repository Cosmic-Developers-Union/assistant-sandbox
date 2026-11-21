package devfs

import (
	"context"
	stdfs "io/fs"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// Chmod 把可写子目录的权限更新委托给子目录文件系统(I13)。
func (f *FS) Chmod(ctx context.Context, name string, mode stdfs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	segments, err := splitPath(name)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return errno.Wrap(errno.NotSupported, "设备目录不支持 chmod")
	}
	system, ok := f.subdirs[segments[0]]
	if !ok {
		return errno.Wrap(errno.NotSupported, "设备节点不支持 chmod: "+name)
	}
	modes, ok := system.(vmfs.Modes)
	if !ok {
		return errno.Wrap(errno.NotSupported, "子目录不支持 chmod: "+name)
	}
	return modes.Chmod(ctx, "/"+strings.Join(segments[1:], "/"), mode)
}
