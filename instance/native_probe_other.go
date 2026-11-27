//go:build !linux

package instance

import (
	"context"
	"runtime"
)

func probeNativeBackend(context.Context) (BackendCapabilities, error) {
	return BackendCapabilities{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, ErrNativeArchitecture
}
