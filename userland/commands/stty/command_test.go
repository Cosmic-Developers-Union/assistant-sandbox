package stty

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

func sharedTable(t *testing.T, flags tty.Flags, size tty.Size) *fd.Table {
	t.Helper()
	terminals, err := tty.New(flags, size, 4096, nil)
	if err != nil {
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
	return table
}

func withTable(t *testing.T, table *fd.Table, run commandio.Handler) commandio.Handler {
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
		set.FD = table
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
}

func TestSTTYReportsRealState(t *testing.T) {
	table := sharedTable(t, tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80})
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, withTable(t, table, Run), t.TempDir(), args, "")
	}
	if r := run(); r.Code != 0 ||
		r.Stdout != "speed 0 baud; rows 24; columns 80; icanon echo isig mesg\n" {
		t.Fatalf("缺省 = %+v", r)
	}
	if r := run("-a"); r.Code != 0 || !strings.Contains(r.Stdout, "icanon") {
		t.Fatalf("-a = %+v", r)
	}
	if r := run("-g"); r.Code != 0 || r.Stdout != ":true:true:true:true:24:80\n" {
		t.Fatalf("-g = %+v", r)
	}
	if r := run("size"); r.Code != 0 || r.Stdout != "24 80\n" {
		t.Fatalf("size = %+v", r)
	}
}

func TestSTTYSetsDeclaredOperands(t *testing.T) {
	table := sharedTable(t, tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80})
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, withTable(t, table, Run), t.TempDir(), args, "")
	}

	if r := run("-icanon", "-echo", "rows", "40", "cols", "120"); r.Code != 0 {
		t.Fatalf("设置 = %+v", r)
	}
	if r := run("-g"); r.Code != 0 || r.Stdout != ":false:false:true:true:40:120\n" {
		t.Fatalf("设置后 -g = %+v", r)
	}

	if r := run("sane"); r.Code != 0 {
		t.Fatalf("sane = %+v", r)
	}
	if r := run("-g"); r.Code != 0 || r.Stdout != ":true:true:true:true:40:120\n" {
		t.Fatalf("sane 后 -g = %+v", r)
	}
}

func TestSTTYReportsTerminalGaps(t *testing.T) {
	table := sharedTable(t, tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80})

	if r := commandtest.Run(t, Run, t.TempDir(), nil, ""); r.Code != 1 || !strings.Contains(r.Stderr, "不是终端") {
		t.Fatalf("非终端 = %+v", r)
	}
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, withTable(t, table, Run), t.TempDir(), args, "")
	}

	if r := run("ixon"); r.Code != 1 || !strings.Contains(r.Stderr, "ixon") {
		t.Fatalf("未知操作数 = %+v", r)
	}

	if r := run("rows"); r.Code != 1 {
		t.Fatalf("rows 缺参数 = %+v", r)
	}

	if r := run("--bogus"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

type descriptorsOnly struct{ fd.Descriptors }

func TestSTTYRejectsUnsettableTerminals(t *testing.T) {
	table := sharedTable(t, tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80})
	wrap := func(run commandio.Handler) commandio.Handler {
		return func(ctx context.Context, args []string) error {
			p, err := proc.FromContext(ctx)
			if err != nil {
				return err
			}
			set := p.Resources.(capability.Set)
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
			set.FD = descriptorsOnly{table}
			p.Resources = set
			return run(proc.WithContext(ctx, p), args)
		}
	}
	if r := commandtest.Run(t, wrap(Run), t.TempDir(), []string{"sane"}, ""); r.Code != 1 || !strings.Contains(r.Stderr, "终端设置能力") {
		t.Fatalf("无 Setter = %+v", r)
	}
}

func TestSTTYMoreOperands(t *testing.T) {
	table := sharedTable(t, tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80})
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, withTable(t, table, Run), t.TempDir(), args, "")
	}

	if r := run("columns", "100"); r.Code != 0 {
		t.Fatalf("columns = %+v", r)
	}
	if r := run("-mesg"); r.Code != 0 {
		t.Fatalf("-mesg = %+v", r)
	}
	if r := run("rows", "abc"); r.Code != 1 || !strings.Contains(r.Stderr, "不是有效数字") {
		t.Fatalf("坏数字 = %+v", r)
	}

	if r := commandtest.Run(t, Run, t.TempDir(), nil, ""); r.Code != 1 {
		t.Fatalf("非终端 = %+v", r)
	}
}

func TestSTTYFaultPaths(t *testing.T) {

	table := sharedTable(t, tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80})
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, withTable(t, table, Run), t.TempDir(), args, "")
	}
	if r := run("-a", "-g"); r.Code != 2 || !strings.Contains(r.Stderr, "互斥") {
		t.Fatalf("互斥 = %+v", r)
	}

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
	if r := commandtest.Run(t, withoutFD, t.TempDir(), nil, ""); r.Code != 2 {
		t.Fatalf("无描述符能力 = %+v", r)
	}

	tiny, err := tty.New(tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	tinyWrap := func(target *tty.Terminal, args []string) commandtest.Result {
		return commandtest.Run(t, func(ctx context.Context, args []string) error {
			p, err := proc.FromContext(ctx)
			if err != nil {
				return err
			}
			set := p.Resources.(capability.Set)
			table := fd.New()
			table.Attach(1, fd.Streams{})
			if err := table.Close(ctx, 1, 0); err != nil {
				t.Fatal(err)
			}
			if err := table.Install(1, 0, target); err != nil {
				t.Fatal(err)
			}
			set.FD = table
			p.Resources = set
			return Run(proc.WithContext(ctx, p), args)
		}, t.TempDir(), args, "")
	}
	if r := tinyWrap(tiny, nil); r.Code != 2 {
		t.Fatalf("报告写失败 = %+v", r)
	}
	if r := tinyWrap(tiny, []string{"size"}); r.Code != 1 {
		t.Fatalf("size 写失败 = %+v", r)
	}

	readonly := func(ctx context.Context, args []string) error {
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
		set.FD = descriptorsOnly{table}
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
	if r := commandtest.Run(t, readonly, t.TempDir(), []string{"sane"}, ""); r.Code != 1 || !strings.Contains(r.Stderr, "终端设置能力") {
		t.Fatalf("只读视图 = %+v", r)
	}
}

func TestSTTYEarlyFailurePaths(t *testing.T) {

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
	if r := commandtest.Run(t, withoutFD, t.TempDir(), nil, ""); r.Code != 2 {
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
