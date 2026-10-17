package identity

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type SingleUser struct{}

func (SingleUser) Lookup(ctx context.Context, pid uint64) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if pid == 0 {
		return Identity{}, errno.NoProcess
	}
	return Identity{UID: 0, GID: 0, Name: "sandbox"}, nil
}
