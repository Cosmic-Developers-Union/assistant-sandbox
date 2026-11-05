package memfs

import (
	"context"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func (f *FS) Mkfifo(ctx context.Context, p string, mode stdfs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	dir, tail, err := f.checkWritable(p)
	if err != nil {
		return err
	}
	if _, exists := dir.children[tail]; exists {
		return errno.Exist
	}

	node, err := f.newNode(kindFifo, stdfs.ModeNamedPipe|mode.Perm(), f.now())
	if err != nil {
		return err
	}
	node.name = tail
	node.parent = dir
	dir.children[tail] = node
	dir.mtime = node.atime
	return nil
}

var _ vmfs.Fifos = (*FS)(nil)
