package proc

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestExecutionContextRequiresExplicitResources(t *testing.T) {
	if _, err := FromContext(t.Context()); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if _, err := FromContext(WithContext(t.Context(), Proc{})); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	p := Proc{ID: 2, Resources: capability.Set{}}
	got, err := FromContext(WithContext(context.Background(), p))
	if err != nil || got.ID != 2 {
		t.Fatal(got, err)
	}
}
