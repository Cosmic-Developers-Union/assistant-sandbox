package entropy

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestDeterministicContinuousBytesAndCancellation(t *testing.T) {
	var one, two Counter
	whole := make([]byte, 1024)
	split := make([]byte, 1024)
	if n, err := one.Read(t.Context(), whole); n != len(whole) || err != nil {
		t.Fatal(n, err)
	}
	if _, err := two.Read(t.Context(), split[:513]); err != nil {
		t.Error(err)
	}
	if _, err := two.Read(t.Context(), split[513:]); err != nil {
		t.Error(err)
	}
	if !bytes.Equal(whole, split) {
		t.Fatal("读取分段改变字节序列")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if n, err := two.Read(ctx, split); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal(n, err)
	}
}
