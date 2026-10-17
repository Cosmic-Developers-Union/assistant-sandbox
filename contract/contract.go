package contract

import (
	"context"
	"io"
)

const (
	ReasonExit     = "exit"
	ReasonSignaled = "signal"
	ReasonFault    = "fault"
	ReasonQuota    = "quota"
)

type ExecSpec struct {
	Program string
	Args    []string
	Env     map[string]string

	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type ExitState struct {
	Code   uint8
	Signal string
	Reason string
}

type Process interface {
	ID() uint64
	Wait(ctx context.Context) (ExitState, error)

	Signal(ctx context.Context, name string) error
}

type Session interface {
	Exec(ctx context.Context, spec ExecSpec) (Process, error)

	Inspect(ctx context.Context) (Snapshot, error)
	Close(ctx context.Context) error
}

type ProcessView struct {
	ID      uint64
	Parent  uint64
	Program string
	State   string
	OpenFDs int
}

type Snapshot struct {
	Processes []ProcessView
}
