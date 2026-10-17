package identity

import "context"

type Identity struct {
	UID, GID uint32
	Name     string
}

type Source interface {
	Lookup(context.Context, uint64) (Identity, error)
}
