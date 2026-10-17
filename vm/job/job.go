package job

import (
	"errors"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func Members(t *proc.Table, pgid proc.PID) []proc.PID {
	var out []proc.PID
	for _, p := range t.List() {
		if p.PGID == pgid {
			out = append(out, p.ID)
		}
	}
	return out
}

func SessionMembers(t *proc.Table, sid proc.PID) []proc.PID {
	var out []proc.PID
	for _, p := range t.List() {
		if p.SID == sid {
			out = append(out, p.ID)
		}
	}
	return out
}

func GroupExists(t *proc.Table, pgid proc.PID) bool {
	return len(Members(t, pgid)) > 0
}

func Leader(t *proc.Table, pgid proc.PID) (proc.PID, bool) {
	for _, p := range t.List() {
		if p.PGID == pgid && p.ID == pgid {
			return p.ID, true
		}
	}
	return 0, false
}

type Dispatch func(pid proc.PID, n signal.Number) error

func SignalGroup(t *proc.Table, pgid proc.PID, n signal.Number, dispatch Dispatch) error {
	if !n.Valid() {
		return errno.Invalid
	}
	members := Members(t, pgid)
	if len(members) == 0 {
		return errno.NoProcess
	}
	var errs []error
	for _, pid := range members {
		if err := dispatch(pid, n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
