package tty

import (
	"context"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

func withTerminal(t *testing.T, run commandio.Handler) commandio.Handler {
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
		stdin, err := original.Get(ctx, uint64(p.ID), 0)
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := original.Get(ctx, uint64(p.ID), 1)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := original.Get(ctx, uint64(p.ID), 2)
		if err != nil {
			t.Fatal(err)
		}
		terminals, err := tty.New(tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
		if err != nil {
			t.Fatal(err)
		}
		table := fd.New()
		table.Attach(1, fd.Streams{Input: stdin, Output: stdout, Error: stderr})
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

func TestTTYAnswersWithDeclaredPath(t *testing.T) {

	if r := commandtest.Run(t, Run, t.TempDir(), nil, ""); r.Code != 1 || r.Stdout != "" || !strings.Contains(r.Stderr, "不是终端") {
		t.Fatalf("非终端 = %+v", r)
	}

	if r := commandtest.Run(t, Run, t.TempDir(), []string{"-s"}, ""); r.Code != 1 || r.Stdout != "" || r.Stderr != "" {
		t.Fatalf("-s 非终端 = %+v", r)
	}

	if r := commandtest.Run(t, withTerminal(t, Run), t.TempDir(), nil, ""); r.Code != 0 || r.Stdout != "/dev/tty\n" {
		t.Fatalf("终端 = %+v", r)
	}
	if r := commandtest.Run(t, withTerminal(t, Run), t.TempDir(), []string{"-s"}, ""); r.Code != 0 || r.Stdout != "" {
		t.Fatalf("终端 -s = %+v", r)
	}

	if r := commandtest.Run(t, Run, t.TempDir(), []string{"--bogus"}, ""); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func TestTTYReportsCapabilityGaps(t *testing.T) {

	withoutFD := func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set := p.Resources.(capability.Set)
		set.FD = nil
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
	if r := commandtest.Run(t, withoutFD, t.TempDir(), nil, ""); r.Code != 2 || r.Stdout != "" {
		t.Fatalf("无描述符能力 = %+v", r)
	}

	cancelled := func(ctx context.Context, args []string) error {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		return Run(proc.WithContext(ctx, p), args)
	}
	if r := commandtest.Run(t, cancelled, t.TempDir(), nil, ""); r.Code != 2 {
		t.Fatalf("取消 = %+v", r)
	}
}
