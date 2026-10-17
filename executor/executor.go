package executor

import (
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
)

type (
	Reason = executable.Reason

	Result = executable.Result

	Instance = executable.Instance

	Executor = executable.Executor

	Program = executable.Program

	Builtin = executable.Builtin
)

const (
	Exited   = executable.Exited
	Signaled = executable.Signaled
	Fault    = executable.Fault
	Quota    = executable.Quota
)

func New() *Builtin { return executable.NewBuiltin() }
