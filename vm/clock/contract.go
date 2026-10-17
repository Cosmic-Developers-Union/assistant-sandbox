package clock

import "context"

type Source interface {
	Wall(context.Context) (int64, error)
	Monotonic(context.Context) (uint64, error)
	Sleep(context.Context, uint64) error
}
