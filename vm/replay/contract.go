package replay

import "context"

type Fact struct {
	Sequence, PID uint64
	Operation     string
	Input, Output []byte
	ErrorCode     string
}

type Journal interface {
	Record(context.Context, Fact) error
	Next(context.Context, Fact) (Fact, error)
}
