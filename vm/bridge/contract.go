package bridge

import "context"

type Bridge interface {
	Call(context.Context, uint64, string, []byte) ([]byte, error)
}
