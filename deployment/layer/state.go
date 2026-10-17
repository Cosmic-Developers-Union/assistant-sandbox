package layer

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type KernelState struct {
	Kernel *vm.Kernel
}

func (s KernelState) QuietPoint() error {
	if s.Kernel == nil {
		return errno.Wrap(errno.Invalid, "缺少内核")
	}
	return s.Kernel.EnsureQuietPoint()
}

func (s KernelState) ProcTable(ctx context.Context) ([]byte, error) {
	if s.Kernel == nil {
		return nil, errno.Wrap(errno.Invalid, "缺少内核")
	}
	return s.Kernel.SnapshotTable(ctx)
}

func (s KernelState) RestoreProcTable(ctx context.Context, data []byte) error {
	if s.Kernel == nil {
		return errno.Wrap(errno.Invalid, "缺少内核")
	}
	return s.Kernel.RestoreTable(ctx, data)
}

var _ interface {
	QuietPoint() error
	ProcTable(context.Context) ([]byte, error)
	RestoreProcTable(context.Context, []byte) error
} = KernelState{}
