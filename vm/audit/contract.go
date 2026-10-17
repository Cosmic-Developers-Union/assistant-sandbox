package audit

import "context"

type Event struct {
	Sequence, PID uint64
	Kind          string
	Payload       []byte
}

type Sink interface {
	Append(context.Context, Event) error
}
