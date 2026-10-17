package fd

import (
	"context"
	"io"
)

type Stream interface {
	io.Reader
	io.Writer
	io.Closer
}

type Terminal struct {
	TTY           bool
	Rows, Columns uint16

	State TerminalState
}

type TerminalState struct {
	Canonical bool

	Echo bool

	ISIG bool

	Mesg bool

	Rows, Columns uint16
}

type StatefulTerminal interface {
	State() TerminalState
	SetState(TerminalState) error
}

type TabStopSetter interface {
	TabStops() []uint16
	SetTabStops(stops []uint16) error
}

type Descriptors interface {
	Get(context.Context, uint64, uint32) (Stream, error)
	Dup(context.Context, uint64, uint32, uint32) error
	Close(context.Context, uint64, uint32) error
	Terminal(context.Context, uint64, uint32) (Terminal, error)
}

type TerminalSetter interface {
	SetTerminalState(context.Context, uint64, uint32, TerminalState) error
}

type Installer interface {
	Descriptors
	InstallNext(uint64, Stream) (uint32, error)
	SetCloexec(uint64, uint32, bool) error
}

type Bindings interface {
	Attach(uint64, Streams)
	Detach(uint64)
}
