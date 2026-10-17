package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestSingleIdentityHasNoHostInheritance(t *testing.T) {
	source := SingleUser{}
	identity, err := source.Lookup(t.Context(), 2)
	if err != nil || identity.Name != "sandbox" || identity.UID != 0 || identity.GID != 0 {
		t.Fatal(identity, err)
	}
	if _, err := source.Lookup(t.Context(), 0); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := source.Lookup(ctx, 2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
