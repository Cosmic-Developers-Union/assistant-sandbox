package entropy

import "context"

type Source interface {
	Read(context.Context, []byte) (int, error)
}
