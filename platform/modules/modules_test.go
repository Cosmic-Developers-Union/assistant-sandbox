package modules

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestModulesRejectUnregisteredAndUnsupportedOperations(t *testing.T) {
	modules := Modules{}
	if _, err := modules.Call(t.Context(), 1, "other", nil); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if _, err := modules.Call(t.Context(), 1, "exec", make([]byte, 2<<20+1)); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := modules.Call(t.Context(), 1, "exec", nil); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := modules.Call(ctx, 1, "exec", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestModuleResponseAndOutputBudget(t *testing.T) {
	output, err := call(t.Context(), "exec", []byte("request"), func(input, output []byte) uint32 {
		if string(input) != "request" {
			t.Fatal(string(input))
		}
		return uint32(copy(output, "response"))
	})
	if err != nil || string(output) != "response" {
		t.Fatal(string(output), err)
	}
	if _, err := call(t.Context(), "exec", nil, func(_ []byte, output []byte) uint32 { return uint32(len(output) + 1) }); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
}
