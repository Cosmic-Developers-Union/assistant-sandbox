package contract

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

type double struct {
	mu     sync.Mutex
	closed bool
	procs  []*doubleProcess
	nextID uint64
}

var fixturePrograms = map[string]bool{
	FixtureEcho:  true,
	FixtureFail:  true,
	FixtureSleep: true,
}

func (d *double) Exec(ctx context.Context, spec ExecSpec) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, errno.Cancelled
	}
	if !fixturePrograms[spec.Program] {

		d.mu.Unlock()
		return nil, errno.NotFound
	}
	d.nextID++
	id := d.nextID
	p := &doubleProcess{session: d, id: id, spec: spec, done: make(chan struct{})}
	d.procs = append(d.procs, p)
	d.mu.Unlock()
	go p.run()
	return p, nil
}

func (d *double) Inspect(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	snapshot := Snapshot{}
	for _, p := range d.procs {
		snapshot.Processes = append(snapshot.Processes, ProcessView{
			ID:      p.id,
			Program: p.spec.Program,
			State:   p.state(),
		})
	}
	return snapshot, nil
}

func (d *double) Close(ctx context.Context) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	procs := make([]*doubleProcess, len(d.procs))
	copy(procs, d.procs)
	d.mu.Unlock()
	for _, p := range procs {
		_ = p.Signal(ctx, "SIGTERM")
	}
	return ctx.Err()
}

func TestSuitePassesAgainstContractDouble(t *testing.T) {
	Suite(t, func() (Session, error) {
		return &double{}, nil
	})
}

type doubleProcess struct {
	session *double
	id      uint64
	spec    ExecSpec
	done    chan struct{}

	mu     sync.Mutex
	final  ExitState
	err    error
	exited bool
}

func (p *doubleProcess) ID() uint64 { return p.id }

func (p *doubleProcess) Wait(ctx context.Context) (ExitState, error) {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.final, p.err
	case <-ctx.Done():
		return ExitState{}, ctx.Err()
	}
}

func (p *doubleProcess) Signal(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := signal.ByName(name)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		return errno.NoProcess
	}
	action, err := signal.Default(n)
	if err != nil {
		return err
	}
	switch action {
	case signal.Terminate, signal.Core:
		p.finish(ExitState{Code: uint8(128 + n), Signal: name, Reason: ReasonSignaled})
	}
	return nil
}

func (p *doubleProcess) run() {
	switch p.spec.Program {
	case FixtureEcho:
		line := strings.Join(p.spec.Args, " ")
		if p.spec.Stdout != nil {
			if _, err := p.spec.Stdout.Write([]byte(line + "\n")); err != nil {
				p.mu.Lock()
				p.err = err
				p.finish(ExitState{Code: 125, Reason: ReasonFault})
				p.mu.Unlock()
				return
			}
		}
		p.mu.Lock()
		p.finish(ExitState{Code: 0, Reason: ReasonExit})
		p.mu.Unlock()
	case FixtureFail:
		p.mu.Lock()
		p.finish(ExitState{Code: 3, Reason: ReasonExit})
		p.mu.Unlock()
	case FixtureSleep:

	}
}

func (p *doubleProcess) finish(state ExitState) {
	if p.exited {
		return
	}
	p.exited = true
	p.final = state
	close(p.done)
}

func (p *doubleProcess) state() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		return "zombie"
	}
	return "running"
}

func TestDoubleRejectsUnknownSignalAndProgram(t *testing.T) {
	d := &double{}
	process, err := d.Exec(context.Background(), ExecSpec{Program: FixtureSleep})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(context.Background(), "SIGNOPE"); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if _, err := d.Exec(context.Background(), ExecSpec{Program: "missing"}); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
}
