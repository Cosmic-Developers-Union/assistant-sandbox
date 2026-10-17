package write

import (
	"context"
	"io"
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

func withDevTTY(t *testing.T, run commandio.Handler) (*tty.Terminal, commandio.Handler) {
	t.Helper()
	terminals, err := tty.New(tty.Flags{Canonical: true, Echo: false, ISIG: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
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
		stdin, err := original.Get(ctx, uint64(p.ID), 0)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := original.Get(ctx, uint64(p.ID), 2)
		if err != nil {
			t.Fatal(err)
		}
		table := fd.New()
		table.Attach(1, fd.Streams{Input: stdin, Error: stderr})
		if err := table.Close(ctx, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := table.Install(1, 1, terminals); err != nil {
			t.Fatal(err)
		}
		set.FD = table
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
	return terminals, wrapped
}

func TestWriteDeliversToOwnTerminal(t *testing.T) {
	terminals, run := withDevTTY(t, Run)
	r := commandtest.RunWithIdentity(t, run, t.TempDir(), []string{"sandbox"}, "hello\nworld\n",
		nil, identity.SingleUser{})
	if r.Code != 0 {
		t.Fatalf("write = %+v", r)
	}
	out := string(terminals.Drain())
	if !strings.Contains(out, "Message from sandbox on /dev/tty...") || !strings.Contains(out, "hello\nworld\n") {
		t.Fatalf("终端输出 = %q", out)
	}
}

func TestWriteRejectsOtherUsersAndPaths(t *testing.T) {
	_, run := withDevTTY(t, Run)
	r := commandtest.RunWithIdentity(t, run, t.TempDir(), []string{"nobody"}, "", nil, identity.SingleUser{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有登录") {
		t.Fatalf("其它用户 = %+v", r)
	}
	r = commandtest.RunWithIdentity(t, run, t.TempDir(), []string{"sandbox", "/dev/pts/1"}, "", nil, identity.SingleUser{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "终端路径") {
		t.Fatalf("其它终端 = %+v", r)
	}
	r = commandtest.RunWithIdentity(t, run, t.TempDir(), nil, "", nil, identity.SingleUser{})
	if r.Code != 2 {
		t.Fatalf("缺操作数 = %+v", r)
	}
	r = commandtest.RunWithIdentity(t, run, t.TempDir(), []string{"--bogus"}, "", nil, identity.SingleUser{})
	if r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func TestWriteReportsUnavailableTerminal(t *testing.T) {
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), []string{"sandbox"}, "", nil, identity.SingleUser{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "终端不可用") {
		t.Fatalf("无终端 = %+v", r)
	}
}

func TestWriteReportsDescriptorGaps(t *testing.T) {
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
	r := commandtest.RunWithIdentity(t, withoutFD, t.TempDir(), []string{"sandbox"}, "", nil, identity.SingleUser{})
	if r.Code != 2 || r.Stdout != "" {
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
	r = commandtest.RunWithIdentity(t, cancelled, t.TempDir(), []string{"sandbox"}, "", nil, identity.SingleUser{})
	if r.Code != 2 {
		t.Fatalf("取消 = %+v", r)
	}

	failTerminals, err := tty.New(tty.Flags{}, tty.Size{}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	failing := func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set := p.Resources.(capability.Set)
		table := fd.New()
		table.Attach(1, fd.Streams{})
		if err := table.Close(ctx, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := table.Install(1, 1, failTerminals); err != nil {
			t.Fatal(err)
		}
		set.FD = table
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
	r = commandtest.RunWithIdentity(t, failing, t.TempDir(), []string{"sandbox"}, "", nil, identity.SingleUser{})
	if r.Code != 2 {
		t.Fatalf("写失败 = %+v", r)
	}
}

type missingCredentials struct{}

func (missingCredentials) Lookup(ctx context.Context, pid uint64) (identity.Identity, error) {
	return identity.Identity{}, errno.NotImplemented
}

func TestWriteEarlyFailurePaths(t *testing.T) {

	r := commandtest.Run(t, Run, t.TempDir(), []string{"sandbox"}, "")
	if r.Code != 2 {
		t.Fatalf("无身份能力 = %+v", r)
	}

	r = commandtest.RunWithIdentity(t, Run, t.TempDir(), []string{"sandbox"}, "", nil, missingCredentials{})
	if r.Code != 2 {
		t.Fatalf("Lookup 失败 = %+v", r)
	}

	cancelled := func(ctx context.Context, args []string) error {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set := p.Resources.(capability.Set)
		set.FD = nil
		p.Resources = set

		set.FD = fd.New()
		set.FD.(*fd.Table).Attach(1, fd.Streams{})
		return Run(proc.WithContext(ctx, p), args)
	}
	r = commandtest.RunWithIdentity(t, cancelled, t.TempDir(), []string{"sandbox"}, "", nil, identity.SingleUser{})
	if r.Code == 0 {
		t.Fatalf("取消应失败: %+v", r)
	}

	_, failingInput := withDevTTY(t, Run)
	_ = failingInput
	badInput := func(ctx context.Context, args []string) error {

		table := fd.New()
		table.Attach(1, fd.Streams{Input: errReader{}, Error: io.Discard})
		if err := table.Close(ctx, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := table.Install(1, 1, mustTerminal(t)); err != nil {
			t.Fatal(err)
		}
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set := p.Resources.(capability.Set)
		set.FD = table
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
	r = commandtest.RunWithIdentity(t, badInput, t.TempDir(), []string{"sandbox"}, "", nil, identity.SingleUser{})
	if r.Code != 2 {
		t.Fatalf("拷贝失败 = %+v", r)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func mustTerminal(t *testing.T) *tty.Terminal {
	t.Helper()
	terminals, err := tty.New(tty.Flags{}, tty.Size{}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	return terminals
}
