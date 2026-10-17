package sandbox

import (
	"context"
	"os"
	"testing"
)

var shellArchive, shellArchiveError = os.ReadFile("assistant-bash.wasm.gz")

func newTestRuntime(ctx context.Context, cfg Config) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if shellArchiveError != nil {
		return nil, shellArchiveError
	}
	cfg.ShellArchive = shellArchive
	return New(ctx, cfg)
}

func TestNewRequiresExplicitArchive(t *testing.T) {
	runtime, err := New(t.Context(), Config{})
	if err == nil || runtime != nil {
		t.Fatal("未提供客体仍创建了 runtime", runtime, err)
	}
}
