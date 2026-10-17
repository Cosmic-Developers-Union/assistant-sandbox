package ipc

import (
	"context"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Kind uint8

const (
	Socket Kind = iota
	NamedPipe
)

type IPC interface {
	Open(context.Context, Kind, string) (io.ReadWriteCloser, error)
	Map(context.Context, string, uint64) ([]byte, error)
}

type Denied struct{}

func (Denied) Open(context.Context, Kind, string) (io.ReadWriteCloser, error) {
	return nil, errno.NotImplemented
}

func (Denied) Map(context.Context, string, uint64) ([]byte, error) { return nil, errno.NotImplemented }
