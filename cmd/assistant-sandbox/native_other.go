//go:build !linux || !amd64

package main

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
)

func runNativeCLI(ctx context.Context, backend instance.BackendInstance, config nativeCLIConfig) (bool, int, error) {
	return false, 0, nil
}
