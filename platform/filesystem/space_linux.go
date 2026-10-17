//go:build !wasip1 && linux

package filesystem

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var _ vmfs.Capacity = (*Filesystem)(nil)

func (f *Filesystem) Space(ctx context.Context, name string) (vmfs.Space, error) {
	if err := ctx.Err(); err != nil {
		return vmfs.Space{}, err
	}
	p, err := f.resolve(name)
	if err != nil {
		return vmfs.Space{}, err
	}

	if _, err := f.stat(p); err != nil {
		return vmfs.Space{}, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(p, &stat); err != nil {
		return vmfs.Space{}, fmt.Errorf("宿主 statfs: %w", err)
	}
	return hostSpace(stat, f.base)
}

func (f *Filesystem) Filesystems(ctx context.Context) ([]vmfs.Space, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	space, err := f.Space(ctx, f.base)
	if err != nil {
		return nil, err
	}
	return []vmfs.Space{space}, nil
}

func hostSpace(stat syscall.Statfs_t, root string) (vmfs.Space, error) {
	if stat.Bsize <= 0 {

		return vmfs.Space{}, errors.New("宿主 statfs 返回的块大小无效")
	}
	blocks, free, available := stat.Blocks, stat.Bfree, stat.Bavail
	blockSize := uint64(stat.Bsize)
	used := uint64(0)
	if blocks > free {
		used = (blocks - free) * blockSize
	}
	space := vmfs.Space{

		Name: root,
		Root: root,
		Bytes: vmfs.Quota{
			Declared: true,
			Total:    blocks * blockSize,
			Used:     used,
			Free:     available * blockSize,
		},
	}
	if stat.Files > 0 {
		space.Inodes = vmfs.Quota{
			Declared: true,
			Total:    stat.Files,
			Used:     stat.Files - stat.Ffree,
			Free:     stat.Ffree,
		}
	}
	return space, nil
}
