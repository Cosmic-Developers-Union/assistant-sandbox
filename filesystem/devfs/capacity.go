package devfs

import (
	"context"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// Space 报告可写子目录(如 /dev/shm)的容量:委托给子目录文件系统。
// 设备节点与 /dev 本身没有容量语义,明确 ENOTSUP。
func (f *FS) Space(ctx context.Context, name string) (vmfs.Space, error) {
	if err := ctx.Err(); err != nil {
		return vmfs.Space{}, err
	}
	segments, err := splitPath(name)
	if err != nil {
		return vmfs.Space{}, err
	}
	if len(segments) == 0 {
		return vmfs.Space{}, errno.Wrap(errno.NotSupported, "/dev 没有容量信息")
	}
	system, ok := f.subdirs[segments[0]]
	if !ok {
		return vmfs.Space{}, errno.Wrap(errno.NotSupported, "设备节点没有容量信息: "+name)
	}
	capacity, ok := system.(vmfs.Capacity)
	if !ok {
		return vmfs.Space{}, errno.Wrap(errno.NotSupported, "子目录不支持容量查询: "+name)
	}
	space, err := capacity.Space(ctx, "/"+strings.Join(segments[1:], "/"))
	if err != nil {
		return vmfs.Space{}, err
	}
	space.Name, space.Root = "/"+segments[0], "/"+segments[0]
	return space, nil
}

// Filesystems 枚举可写子目录的容量视图(/proc/mounts 等使用)。
func (f *FS) Filesystems(ctx context.Context) ([]vmfs.Space, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]vmfs.Space, 0, len(f.subdirs))
	for name, system := range f.subdirs {
		space := vmfs.Space{Name: "/" + name, Root: "/" + name}
		if capacity, ok := system.(vmfs.Capacity); ok {
			reported, err := capacity.Space(ctx, "/")
			if err != nil {
				return nil, err
			}
			space.Bytes, space.Inodes = reported.Bytes, reported.Inodes
		}
		out = append(out, space)
	}
	return out, nil
}
