package net

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestNoHostNetworkFallback(t *testing.T) {
	for _, network := range []string{"tcp", "udp", "unix", "unknown"} {
		stream, err := (Denied{}).Dial(t.Context(), network, "127.0.0.1:9931")
		if stream != nil || !errors.Is(err, errno.NetworkUnreachable) {
			t.Fatal(stream, err)
		}
	}
}
