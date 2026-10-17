package commandtest

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

func WithTerminal(t *testing.T, run commandio.Handler) commandio.Handler {
	t.Helper()
	return WithTerminalFlags(t, tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80}, run)
}

func WithTerminalFlags(t *testing.T, flags tty.Flags, size tty.Size, run commandio.Handler) commandio.Handler {
	t.Helper()
	terminals, err := tty.New(flags, size, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	return WithTerminalHandle(t, terminals, run)
}

func WithTerminalHandle(t *testing.T, terminals *tty.Terminal, run commandio.Handler) commandio.Handler {
	t.Helper()
	return func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		original, ok := set.FD.(*fd.Table)
		if !ok {
			t.Fatalf("描述符表类型: %T", set.FD)
		}
		stdout, err := original.Get(ctx, uint64(p.ID), 1)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := original.Get(ctx, uint64(p.ID), 2)
		if err != nil {
			t.Fatal(err)
		}
		table := fd.New()
		table.Attach(1, fd.Streams{Output: stdout, Error: stderr})
		if err := table.Close(ctx, 1, 0); err != nil {
			t.Fatal(err)
		}
		if err := table.Install(1, 0, terminals); err != nil {
			t.Fatal(err)
		}
		set.FD = table
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
}
