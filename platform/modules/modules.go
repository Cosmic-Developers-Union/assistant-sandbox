package modules

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Modules struct{}

func (Modules) Call(ctx context.Context, _ uint64, operation string, input []byte) ([]byte, error) {
	return call(ctx, operation, input, exchangeModule)
}

func call(ctx context.Context, operation string, input []byte, exchange func([]byte, []byte) uint32) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if operation != "exec" {
		return nil, errno.NotImplemented
	}
	if len(input) > 2<<20 {
		return nil, errno.Limit
	}
	output := make([]byte, 1<<20)
	n := exchange(input, output)
	if n == 0 {
		return nil, errno.NotFound
	}
	if uint64(n) > uint64(len(output)) {
		return nil, errno.Limit
	}
	return output[:n], nil
}
