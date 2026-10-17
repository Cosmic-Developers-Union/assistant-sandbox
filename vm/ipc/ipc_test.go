package ipc

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestIPCIsExplicitlyUnimplemented(t *testing.T) {
	for _, kind := range []Kind{Socket, NamedPipe, 99} {
		stream, err := (Denied{}).Open(t.Context(), kind, "test")
		if stream != nil || !errors.Is(err, errno.NotImplemented) {
			t.Fatal(stream, err)
		}
	}
	memory, err := (Denied{}).Map(t.Context(), "shared", 4096)
	if memory != nil || !errors.Is(err, errno.NotImplemented) {
		t.Fatal(memory, err)
	}
}
