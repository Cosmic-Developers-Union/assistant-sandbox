package mountfs

import (
	"context"

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
			return nil, err
		}
		space.Bytes, space.Inodes = reported.Bytes, reported.Inodes
		entries = append(entries, space)
	}
	return entries, nil
}
