package sh

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/bash"
)

type Command struct {
	NewRunner bash.RunnerFactory
}

func (c Command) Run(ctx context.Context, args []string) error {
	return bash.Command{NewRunner: c.NewRunner}.Run(ctx, args)
}
