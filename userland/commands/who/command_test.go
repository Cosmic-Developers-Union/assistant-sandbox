package who

import (
	"context"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

type configControl struct {
	declared bool
}

func (c configControl) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (c configControl) Finish(proc.PID, proc.PID, uint8) error { return nil }
func (c configControl) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (c configControl) SystemConfiguration() (proc.SystemConfiguration, error) {

	return proc.SystemConfiguration{BootTime: 3600 * 1e9, BootDeclared: c.declared, ChildMax: 8, ArgMax: 131072}, nil
}

func withTerminal(t *testing.T, mesg bool, run commandio.Handler) commandio.Handler {
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
		terminals, err := tty.New(tty.Flags{Canonical: true, Echo: true, ISIG: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := terminals.SetMesg(mesg); err != nil {
			t.Fatal(err)
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

func runWith(t *testing.T, options commandtest.Options, args ...string) commandtest.Result {
	t.Helper()
	return commandtest.RunWith(t, Run, t.TempDir(), args, "", options)
}

func TestWhoReportsDeclaredSession(t *testing.T) {
	options := commandtest.Options{Control: configControl{declared: true}, Identity: identity.SingleUser{}}
	if r := runWith(t, options); r.Code != 0 || r.Stdout != "sandbox  ?        1970-01-01 01:00\n" {
		t.Fatalf("缺省 = %+v", r)
	}

	options.Control = configControl{declared: false}
	if r := runWith(t, options); r.Code != 0 || !strings.Contains(r.Stdout, " -\n") {
		t.Fatalf("未声明时刻 = %+v", r)
	}

	options.Control = configControl{declared: true}
	if r := runWith(t, options, "-H"); r.Code != 0 || !strings.HasPrefix(r.Stdout, "NAME") {
		t.Fatalf("-H = %+v", r)
	}
	if r := runWith(t, options, "-q"); r.Code != 0 || r.Stdout != "sandbox\n# users=1\n" {
		t.Fatalf("-q = %+v", r)
	}
	if r := runWith(t, options, "-s"); r.Code != 0 || r.Stdout == "" {
		t.Fatalf("-s = %+v", r)
	}
	if r := runWith(t, options, "-m"); r.Code != 0 || !strings.Contains(r.Stdout, "sandbox") {
		t.Fatalf("-m = %+v", r)
	}

	if r := runWith(t, options, "-b"); r.Code != 0 || r.Stdout != "system boot  1970-01-01 01:00\n" {
		t.Fatalf("-b = %+v", r)
	}
	options.Control = configControl{declared: false}
	if r := runWith(t, options, "-b"); r.Code != 1 {
		t.Fatalf("未声明 -b = %+v", r)
	}
}

func TestWhoTerminalMarks(t *testing.T) {
	base := func(run commandio.Handler) commandtest.Result {
		return commandtest.RunWith(t, run, t.TempDir(), []string{"-T"}, "",
			commandtest.Options{Control: configControl{declared: true}, Identity: identity.SingleUser{}})
	}
	allowed := base(withTerminal(t, true, Run))
	if allowed.Code != 0 || !strings.Contains(allowed.Stdout, "+ sandbox") || !strings.Contains(allowed.Stdout, terminalPath) {
		t.Fatalf("mesg y = %+v", allowed)
	}
	denied := base(withTerminal(t, false, Run))
	if denied.Code != 0 || !strings.Contains(denied.Stdout, "- sandbox") {
		t.Fatalf("mesg n = %+v", denied)
	}
}

func TestWhoRejectsUnavailableFacts(t *testing.T) {
	options := commandtest.Options{Control: configControl{declared: true}, Identity: identity.SingleUser{}}
	for _, flag := range []string{"-a", "-d", "-l", "-p", "-r", "-u"} {
		if r := runWith(t, options, flag); r.Code == 0 {
			t.Fatalf("%s 应拒绝: %+v", flag, r)
		}
	}
	if r := runWith(t, options, "/var/run/utmp"); r.Code != 2 {
		t.Fatalf("文件操作数 = %+v", r)
	}
	if r := runWith(t, options, "--bogus"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}
