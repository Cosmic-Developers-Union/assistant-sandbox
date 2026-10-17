package bridge

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

type Recording struct {
	base    Bridge
	journal *replay.Memory
}

func NewRecording(base Bridge, journal *replay.Memory) *Recording {
	return &Recording{base: base, journal: journal}
}

func (r *Recording) Call(ctx context.Context, pid uint64, operation string, input []byte) ([]byte, error) {
	if r.journal == nil {
		if r.base == nil {
			return nil, errno.NotImplemented
		}
		return r.base.Call(ctx, pid, operation, input)
	}
	return replay.Run(ctx, r.journal, pid, replay.OpBridgeCall, input, func(ctx context.Context) ([]byte, string, error) {
		if r.base == nil {
			return nil, "no-bridge", errno.NotImplemented
		}
		result, callErr := r.base.Call(ctx, pid, operation, input)
		if callErr != nil {
			return result, "call", callErr
		}
		return result, "", nil
	})
}
