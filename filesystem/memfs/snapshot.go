package memfs

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type snapshot struct {
	root *inode
}

func (f *FS) Snapshot(ctx context.Context) (vmfs.Snapshotted, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return &snapshot{root: cloneTree(f.root)}, nil
}

func (s *snapshot) Restore(ctx context.Context, target vmfs.System) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dst, ok := target.(*FS)
	if !ok {
		return errno.NotSupported
	}
	copied := cloneTree(s.root)
	dst.mu.Lock()
	defer dst.mu.Unlock()

	stale := map[*inode]struct{}{}
	for node := range dst.live {
		if node.opened == 0 {
			continue
		}
		node.nlink = 0
		stale[node] = struct{}{}
	}
	dst.root = copied
	fixParents(copied)
	dst.nodes = countNodes(copied) + uint64(len(stale))
	dst.live = liveSet(copied)
	for node := range stale {
		dst.live[node] = struct{}{}
	}
	return nil
}

func cloneTree(node *inode) *inode {
	copied := *node
	copied.opened = 0
	if node.children != nil {
		children := make(map[string]*inode, len(node.children))
		for name, child := range node.children {
			children[name] = cloneTree(child)
		}
		copied.children = children
	}
	if node.data != nil {
		copied.data = append([]byte(nil), node.data...)
	}
	return &copied
}

func fixParents(node *inode) {
	for name, child := range node.children {
		child.parent = node
		child.name = name
		fixParents(child)
	}
}

func countNodes(node *inode) uint64 {
	total := uint64(1)
	for _, child := range node.children {
		total += countNodes(child)
	}
	return total
}
