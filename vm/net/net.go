package net

import (
	"context"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Network interface {
	Dial(context.Context, string, string) (io.ReadWriteCloser, error)
}

type Denied struct{}

func (Denied) Dial(context.Context, string, string) (io.ReadWriteCloser, error) {
	return nil, errno.NetworkUnreachable
}
