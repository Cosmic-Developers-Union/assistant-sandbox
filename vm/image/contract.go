package image

import (
	"context"
	"io"
)

type Manifest struct {
	Digest, ABI, Entry string

	Registry string

	Language string
}

type Image interface {
	Manifest(context.Context) (Manifest, error)
	Open(context.Context, string) (io.ReadCloser, error)

	DigestFor(context.Context, string) (string, error)
}
