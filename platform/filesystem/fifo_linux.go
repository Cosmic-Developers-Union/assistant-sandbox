//go:build !wasip1 && linux

package filesystem

import (
	"context"
	"io/fs"

	"syscall"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var _ vmfs.Fifos = (*Filesystem)(nil)

func (f *Filesystem) Mkfifo(ctx context.Context, name string, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := f.resolve(name)
	if err != nil {
		return err
	}
	return syscall.Mkfifo(p, uint32(vmfs.ModeBits(mode)))
}
