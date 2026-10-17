package mesg

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

type descriptorsOnly struct{ fd.Descriptors }

func withSharedTerminal(t *testing.T, mesgOn bool, only bool, run commandio.Handler) (*fd.Table, commandio.Handler) {
	t.Helper()
	terminals, err := tty.New(tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := terminals.SetMesg(mesgOn); err != nil {
		t.Fatal(err)
	}
	table := fd.New()
	table.Attach(1, fd.Streams{})
	if err := table.Close(context.Background(), 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := table.Install(1, 0, terminals); err != nil {
		t.Fatal(err)
	}
	wrapped := func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		original := set.FD.(*fd.Table)
		stdout, err := original.Get(ctx, uint64(p.ID), 1)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := original.Get(ctx, uint64(p.ID), 2)
		if err != nil {
			t.Fatal(err)
		}
		if err := table.Close(context.Background(), 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := table.Close(context.Background(), 1, 2); err != nil {
			t.Fatal(err)
		}
		if err := table.Install(1, 1, stdout); err != nil {
			t.Fatal(err)
		}
		if err := table.Install(1, 2, stderr); err != nil {
			t.Fatal(err)
		}
		if only {
			set.FD = descriptorsOnly{table}
		} else {
			set.FD = table
		}
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
	return table, wrapped
}

func TestMesgQueriesAndSets(t *testing.T) {

	table, run := withSharedTerminal(t, true, false, Run)
	if r := commandtest.Run(t, run, t.TempDir(), nil, ""); r.Code != 0 || r.Stdout != "is y\n" {
		t.Fatalf("查询 = %+v", r)
	}
	if r := commandtest.Run(t, run, t.TempDir(), []string{"n"}, ""); r.Code != 0 {
		t.Fatalf("设置 n = %+v", r)
	}
	if r := commandtest.Run(t, run, t.TempDir(), nil, ""); r.Code != 0 || r.Stdout != "is n\n" {
		t.Fatalf("设置后查询 = %+v", r)
	}
	info, _ := table.Terminal(context.Background(), 1, 0)
	if info.State.Mesg {
		t.Fatal("mesg 位应为 false")
	}

	if r := commandtest.Run(t, run, t.TempDir(), []string{"y"}, ""); r.Code != 0 {
		t.Fatalf("设置 y = %+v", r)
	}
}

func TestMesgRejectsBadSituations(t *testing.T) {

	if r := commandtest.Run(t, Run, t.TempDir(), nil, ""); r.Code != 1 || !strings.Contains(r.Stderr, "不是终端") {
		t.Fatalf("无终端 = %+v", r)
	}

	_, run := withSharedTerminal(t, true, false, Run)
	if r := commandtest.Run(t, run, t.TempDir(), []string{"maybe"}, ""); r.Code != 2 {
		t.Fatalf("坏操作数 = %+v", r)
	}
	if r := commandtest.Run(t, run, t.TempDir(), []string{"y", "n"}, ""); r.Code != 2 {
		t.Fatalf("多操作数 = %+v", r)
	}
	if r := commandtest.Run(t, run, t.TempDir(), []string{"--bogus"}, ""); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}

	_, readonly := withSharedTerminal(t, true, true, Run)
	if r := commandtest.Run(t, readonly, t.TempDir(), []string{"n"}, ""); r.Code != 1 || !strings.Contains(r.Stderr, "终端设置能力") {
		t.Fatalf("无 Setter = %+v", r)
	}
}
