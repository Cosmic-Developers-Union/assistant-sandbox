//go:build !wasip1

package filesystem

import (
	"context"
	"io/fs"
	"os"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var _ vmfs.Modes = (*Filesystem)(nil)

func (f *Filesystem) Chmod(ctx context.Context, name string, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := f.resolve(name)
	if err != nil {
		return err
	}
	_, err = withRoot(f, func(root *os.Root) (struct{}, error) {
		return struct{}{}, root.Chmod(f.relative(p), vmfs.ModeBits(mode))
	})
	return err
}
