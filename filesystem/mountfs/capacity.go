package mountfs

import (
	"context"
	"errors"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func (m *Mounts) Space(ctx context.Context, name string) (vmfs.Space, error) {
	entry, err := m.resolve(name)
	if err != nil {
		return vmfs.Space{}, err
	}
	capacity, ok := entry.sys.(vmfs.Capacity)
	if !ok {

		return vmfs.Space{}, errno.Wrap(errno.NotSupported, "挂载 "+entry.point+" 的文件系统没有容量能力")
	}

	space, err := capacity.Space(ctx, m.relative(entry, name))
	if err != nil {
		return vmfs.Space{}, err
	}
	space.Name, space.Root = entry.point, entry.point
	return space, nil
}

func (m *Mounts) Filesystems(ctx context.Context) ([]vmfs.Space, error) {
	entries := make([]vmfs.Space, 0, len(m.mounts))
	for _, entry := range m.mounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		space := vmfs.Space{Name: entry.point, Root: entry.point}
		capacity, ok := entry.sys.(vmfs.Capacity)
		if !ok {
			entries = append(entries, space)
			continue
		}
		reported, err := capacity.Space(ctx, "/")
		if err != nil {
			// /dev 这类聚合文件系统本身没有容量语义,但可写子目录
			// (/dev/shm)有;用 Filesystems 展开后按挂载点加前缀。
			if errors.Is(err, errno.NotSupported) {
				nested, nestedErr := capacity.Filesystems(ctx)
				if nestedErr != nil {
					return nil, nestedErr
				}
				for _, child := range nested {
					child.Name = path.Join(entry.point, child.Name)
					child.Root = path.Join(entry.point, child.Root)
					entries = append(entries, child)
				}
				continue
			}
			return nil, err
		}
		space.Bytes, space.Inodes = reported.Bytes, reported.Inodes
		entries = append(entries, space)
	}
	return entries, nil
}
