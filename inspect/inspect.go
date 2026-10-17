package inspect

import (
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type Process struct {
	ID         uint64
	Parent     uint64
	Generation uint64
	Program    string
	State      string
	ExitCode   uint8
	ExitSignal uint8
	PGID       uint64
	SID        uint64
	OpenFDs    []uint32
}

type ControlEvent struct {
	Seq    uint64
	PID    uint64
	Kind   string
	Detail string
}

type Snapshot struct {
	Init      uint64
	Closed    bool
	Processes []Process

	Control []ControlEvent
}

func Kernel(k *vm.Kernel) (Snapshot, error) {
	snapshot := Snapshot{Init: uint64(k.Init())}
	var err error
	snapshot.Closed, err = k.Closed()
	if err != nil {
		return Snapshot{}, err
	}
	for _, p := range k.Processes() {
		view := Process{
			ID:         uint64(p.ID),
			Parent:     uint64(p.Parent),
			Generation: p.Generation,
			Program:    p.Program,
			State:      p.State.String(),
			ExitCode:   p.ExitCode,
			ExitSignal: p.ExitSignal,
			PGID:       uint64(p.PGID),
			SID:        uint64(p.SID),
		}
		if view.OpenFDs, err = k.OpenFDs(p.ID); err != nil {
			return Snapshot{}, err
		}
		snapshot.Processes = append(snapshot.Processes, view)
	}
	snapshot.Control = Control(k)
	return snapshot, nil
}

func Control(k *vm.Kernel) []ControlEvent {
	events := k.Control()
	if len(events) == 0 {
		return nil
	}
	out := make([]ControlEvent, 0, len(events))
	for _, event := range events {
		out = append(out, ControlEvent{
			Seq:    event.Seq,
			PID:    event.PID,
			Kind:   event.Kind,
			Detail: event.Detail,
		})
	}
	return out
}

func (s Snapshot) Running() bool {
	for _, p := range s.Processes {
		if p.ID != s.Init && (p.State == proc.Ready.String() || p.State == proc.Running.String()) {
			return true
		}
	}
	return false
}
