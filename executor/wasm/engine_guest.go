//go:build wasip1

package wasm

import "context"

type guestEngine struct{}

func NewGuestEngine() (Engine, error) { return nil, ErrNoEngine }

var _ Engine = (*guestEngine)(nil)

func (guestEngine) Instantiate(context.Context, *Module, []byte, Config) (Instance, error) {
	return nil, ErrNoEngine
}

func (guestEngine) Close() error { return nil }
