package fd

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type statefulStream struct {
	rows, columns uint16
	state         TerminalState
}

func (s *statefulStream) Read([]byte) (int, error)    { return 0, nil }
func (s *statefulStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *statefulStream) Close() error                { return nil }
func (s *statefulStream) Dimensions() (uint16, uint16) {
	return s.rows, s.columns
}
func (s *statefulStream) State() TerminalState { return s.state }
func (s *statefulStream) SetState(state TerminalState) error {
	s.state = state
	return nil
}

func TestTerminalStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	table := New()
	table.Attach(1, Streams{})
	terminal, err := table.InstallNext(1, &statefulStream{rows: 40, columns: 120, state: TerminalState{Canonical: true, Echo: true, ISIG: true, Mesg: true}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := table.Terminal(ctx, 1, terminal)
	if err != nil || !info.TTY || !info.State.Canonical || !info.State.Mesg {
		t.Fatalf("状态查询 = %+v err=%v", info, err)
	}
	if err := table.SetTerminalState(ctx, 1, terminal, TerminalState{Canonical: false, Echo: false, ISIG: false, Mesg: false}); err != nil {
		t.Fatalf("设置状态: %v", err)
	}
	info, _ = table.Terminal(ctx, 1, terminal)
	if info.State.Mesg || info.State.Canonical {
		t.Fatalf("设置未生效: %+v", info.State)
	}

	plain, err := table.InstallNext(1, &sizedStream{rows: 24, columns: 80})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := table.Terminal(ctx, 1, plain); err != nil || !info.TTY || info.State != (TerminalState{}) {
		t.Fatalf("无状态终端 = %+v err=%v", info, err)
	}
	if err := table.SetTerminalState(ctx, 1, plain, TerminalState{}); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("无状态终端的设置应报 ENOTSUP: %v", err)
	}

	stream, err := table.InstallNext(1, &counted{})
	if err != nil {
		t.Fatal(err)
	}
	if err := table.SetTerminalState(ctx, 1, stream, TerminalState{}); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("非终端设置应报 ENOTSUP: %v", err)
	}
	if err := table.SetTerminalState(ctx, 1, 42, TerminalState{}); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("坏描述符应报 EBADF: %v", err)
	}
}
