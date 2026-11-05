package memfs

import (
	"context"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func (f *FS) Space(ctx context.Context, p string) (vmfs.Space, error) {
	if err := ctx.Err(); err != nil {
		return vmfs.Space{}, err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	if _, _, _, err := f.lookup(p, true); err != nil {
		return vmfs.Space{}, err
	}
	return f.space(), nil
}

func (f *FS) Filesystems(ctx context.Context) ([]vmfs.Space, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return []vmfs.Space{f.space()}, nil
}

func (f *FS) space() vmfs.Space {
	space := vmfs.Space{
		Name: "/",
		Root: "/",
		Inodes: vmfs.Quota{
			Declared: true,
			Total:    uint64(f.maxNodes),
			Used:     f.st.nodes,
			Free:     uint64(f.maxNodes) - min(f.st.nodes, uint64(f.maxNodes)),
		},
	}

	used := f.usedBytes()
	space.Bytes = vmfs.Quota{Used: uint64(used)}
	if f.maxTotalBytes > 0 {

		space.Bytes.Declared = true
		space.Bytes.Total = uint64(f.maxTotalBytes)
		space.Bytes.Free = uint64(max(f.maxTotalBytes-used, 0))
	}
	return space
}

func (f *FS) usedBytes() int64 {
	var total int64
	for node := range f.st.live {
		total += int64(len(node.data))
	}
	return total
}

func liveSet(root *inode) map[*inode]struct{} {
	live := map[*inode]struct{}{}
	var walk func(*inode)
	walk = func(node *inode) {
		live[node] = struct{}{}
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
	return live
}
