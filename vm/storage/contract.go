package storage

import "context"

type Sessions interface {
	Load(context.Context, string) ([]byte, error)
	Save(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

type Snapshots interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, []byte) (string, error)
}
