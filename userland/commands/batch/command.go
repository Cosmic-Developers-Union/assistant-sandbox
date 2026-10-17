package batch

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/at"
)

func Run(ctx context.Context, args []string) error {
	return at.RunBatch(ctx, args)
}
