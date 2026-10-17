package sandbox

import (
	"context"
	"io"
)

type Result struct {
	ExitCode int

	Closed bool
}

type Executor interface {
	Execute(context.Context, string, string, io.Writer, io.Writer) (Result, error)
}
