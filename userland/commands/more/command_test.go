package more

import (
	"context"
	"io"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

func TestMoreCopiesWithoutTerminal(t *testing.T) {
	dir := t.TempDir()

	if r := commandtest.Run(t, Run, dir, nil, "alpha\nbeta\n"); r.Code != 0 || r.Stdout != "alpha\nbeta\n" {
		t.Fatalf("stdin = %+v", r)
	}

	r := commandtest.Run(t, Run, dir, []string{"f.txt"}, "")
	_ = r

	if r := commandtest.Run(t, Run, dir, []string{"missing.txt"}, ""); r.Code != 1 || !strings.Contains(r.Stderr, "missing.txt") {
		t.Fatalf("缺失文件 = %+v", r)
	}
	if r := commandtest.Run(t, Run, dir, []string{"--bogus"}, ""); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func withTerminalStdout(t *testing.T, rows uint16, run commandio.Handler) (*tty.Terminal, commandio.Handler) {
	t.Helper()
	terminals, err := tty.New(tty.Flags{Canonical: true, Echo: false, ISIG: true}, tty.Size{Rows: rows, Cols: 80}, 65536, nil)
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

func TestMorePagesOnTerminal(t *testing.T) {
	dir := t.TempDir()

	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "x"+itoa(i))
	}
	input := strings.Join(lines, "\n") + "\n"
	runPaged := func(rows uint16, stdin string, args ...string) string {
		t.Helper()
		terminals, run := withTerminalStdout(t, rows, Run)
		r := commandtest.Run(t, run, dir, args, stdin)
		if r.Code != 0 {
			t.Fatalf("分页 = %+v", r)
		}
		out := string(terminals.Drain())
		if r.Stdout != "" {
			t.Fatalf("终端输出不应落在别处: %q", r.Stdout)
		}
		return out
	}
	out := runPaged(6, input+"\n\n")
	if !strings.Contains(out, "--More--") || strings.Count(out, "--More--") != 2 {
		t.Fatalf("提示次数错: %q", out)
	}

	out = runPaged(6, "a1\na2\na3\na4\na5\nq\nnever\n")
	if strings.Count(out, "--More--") != 1 || strings.Contains(out, "never") {
		t.Fatalf("q 退出 = %q", out)
	}

	out = runPaged(24, input+"\n\n\n\n", "-n", "3")
	if strings.Count(out, "--More--") != 5 {
		t.Fatalf("-n 3 = %q", out)
	}

	_, run := withTerminalStdout(t, 24, Run)
	if r := commandtest.Run(t, run, dir, []string{"-s"}, ""); r.Code != 2 {
		t.Fatalf("-s = %+v", r)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func TestMoreMultiFileAndFaults(t *testing.T) {
	dir := t.TempDir()

	r := commandtest.Run(t, Run, dir, []string{"missing.txt", "-"}, "tail\n")
	if r.Code != 1 || !strings.Contains(r.Stderr, "missing.txt") || !strings.Contains(r.Stdout, "tail\n") {
		t.Fatalf("多文件拷贝 = %+v", r)
	}

	_, run := withTerminalStdout(t, 24, Run)
	r = commandtest.Run(t, run, dir, []string{"missing.txt"}, "paged\n")
	if r.Code != 0 {
		t.Fatalf("分页继续 = %+v", r)
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
	if r := commandtest.Run(t, withoutFD, dir, nil, ""); r.Code != 2 {
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
	if r := commandtest.Run(t, cancelled, dir, nil, ""); r.Code == 0 {
		t.Fatalf("取消应失败: %+v", r)
	}
}

func TestMoreReportsWriteFailures(t *testing.T) {
	tiny, err := tty.New(tty.Flags{}, tty.Size{}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	tinyStream := failStream{}
	wrap := func(target fd.Stream, run commandio.Handler) commandio.Handler {
		return func(ctx context.Context, args []string) error {
			p, err := proc.FromContext(ctx)
			if err != nil {
				return err
			}
			set := p.Resources.(capability.Set)
			table := fd.New()
			table.Attach(1, fd.Streams{Input: strings.NewReader("data\n"), Error: io.Discard})
			if err := table.Close(ctx, 1, 1); err != nil {
				t.Fatal(err)
			}
			if err := table.Install(1, 1, target); err != nil {
				t.Fatal(err)
			}
			set.FD = table
			p.Resources = set
			return run(proc.WithContext(ctx, p), args)
		}
	}

	if r := commandtest.Run(t, wrap(tinyStream, Run), t.TempDir(), nil, ""); r.Code != 1 {
		t.Fatalf("拷贝写失败 = %+v", r)
	}

	if r := commandtest.Run(t, wrap(tiny, Run), t.TempDir(), []string{"-n", "2"}, ""); r.Code != 2 {
		t.Fatalf("分页写失败 = %+v", r)
	}
}

type failStream struct{}

func (failStream) Read([]byte) (int, error)  { return 0, io.EOF }
func (failStream) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (failStream) Close() error              { return nil }
