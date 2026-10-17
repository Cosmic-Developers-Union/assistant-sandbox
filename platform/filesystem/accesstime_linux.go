//go:build !wasip1 && linux

package filesystem

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var _ vmfs.AccessTimes = (*Filesystem)(nil)

func (f *Filesystem) AccessTime(ctx context.Context, name string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	p, err := f.resolve(name)
	if err != nil {
		return 0, err
	}
	info, err := f.stat(p)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {

		return 0, fmt.Errorf("宿主 stat 缺少访问时间: %w", errors.ErrUnsupported)
	}
	return stat.Atim.Sec*1_000_000_000 + stat.Atim.Nsec, nil
}
