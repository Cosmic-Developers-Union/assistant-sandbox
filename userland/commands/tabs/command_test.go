package tabs

import (
	"context"
	"slices"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

func withTerminal(t *testing.T, terminals *tty.Terminal) commandio.Handler {
	t.Helper()
	return commandtest.WithTerminalHandle(t, terminals, Run)
}

func TestDefaultRestoresEvery8(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := terminals.SetTabStops([]uint16{1, 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := terminals.Input([]byte("x\t")); err != nil {
		t.Fatal(err)
	}
	r := commandtest.Run(t, withTerminal(t, terminals), t.TempDir(), nil, "")
	if r.Code != 0 {
		t.Fatalf("缺省设置 = %+v", r)
	}
	stops := terminals.TabStops()
	if !slices.Equal(stops, []uint16{9, 17, 25, 33, 41, 49, 57, 65, 73}) {
		t.Fatalf("恢复缺省 = %v", stops)
	}
}

func TestSetEveryN(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := commandtest.Run(t, withTerminal(t, terminals), t.TempDir(), []string{"-4"}, "")
	if r.Code != 0 {
		t.Fatalf("-4 = %+v", r)
	}
	stops := terminals.TabStops()
	if !slices.Equal(stops[:3], []uint16{4, 8, 12}) {
		t.Fatalf("每 4 列一站 = %v", stops)
	}

	if _, err := terminals.Write([]byte("a\tb")); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := terminals.ScreenView()
	if !strings.HasPrefix(lines[0], "a  b") {
		t.Fatalf("tab 渲染 = %q", lines[0])
	}
}

func TestRejectsMarginAndTypes(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, withTerminal(t, terminals), t.TempDir(), args, "")
	}
	if r := run("+m", "10"); r.Code != 1 || !strings.Contains(r.Stderr, "边距") {
		t.Fatalf("+m[n] 应拒绝: %+v", r)
	}
	if r := run("crt"); r.Code != 2 || !strings.Contains(r.Stderr, "不支持") {
		t.Fatalf("type 名应拒绝: %+v", r)
	}
	if r := run("-63"); r.Code != 2 || !strings.Contains(r.Stderr, "1..62") {
		t.Fatalf("越界间距应拒绝: %+v", r)
	}
	if r := run("-0"); r.Code != 2 {
		t.Fatalf("零间距应拒绝: %+v", r)
	}

	if err := terminals.SetTabStops([]uint16{3}); err != nil {
		t.Fatal(err)
	}
	_ = run("crt")
	if !slices.Equal(terminals.TabStops(), []uint16{3}) {
		t.Fatalf("拒绝后制表位应保持: %v", terminals.TabStops())
	}
}

func TestNoTerminalFails(t *testing.T) {

	r := commandtest.Run(t, Run, t.TempDir(), nil, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "不是终端") {
		t.Fatalf("无终端 = %+v", r)
	}
}

func TestSetterCapabilityMissing(t *testing.T) {

	handler := func(ctx context.Context, args []string) error {
		p, procErr := proc.FromContext(ctx)
		if procErr != nil {
			return procErr
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}

		set.FD = descriptorsOnly{set.FD.(*fd.Table)}
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
	r := commandtest.Run(t, handler, t.TempDir(), []string{"-4"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "不是终端") {
		t.Fatalf("无终端 = %+v", r)
	}
}

type descriptorsOnly struct{ fd.Descriptors }

func TestInvalidSpacing(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := commandtest.Run(t, withTerminal(t, terminals), t.TempDir(), []string{"-x"}, "")
	if r.Code != 2 {
		t.Fatalf("非数字间距 = %+v", r)
	}
}

func withTerminalOn(t *testing.T, terminals *tty.Terminal, id uint32, closed ...uint32) commandio.Handler {
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
		table := fd.New()
		table.Attach(uint64(p.ID), fd.Streams{Input: stdin, Output: stdout, Error: stderr})
		for _, closedID := range closed {
			if err := table.Close(ctx, uint64(p.ID), closedID); err != nil {
				t.Fatal(err)
			}
		}

		if err := table.Close(ctx, uint64(p.ID), id); err != nil {
			t.Fatal(err)
		}
		if err := table.Install(uint64(p.ID), id, terminals); err != nil {
			t.Fatal(err)
		}
		set.FD = table
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
}

func TestTerminalOnStdoutWhenStdinClosed(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := commandtest.Run(t, withTerminalOn(t, terminals, 1, 0), t.TempDir(), []string{"-4"}, "")
	if r.Code != 0 || r.Stderr != "" {
		t.Fatalf("fd0 关闭时应在 fd1 设置成功 = %+v", r)
	}
	if stops := terminals.TabStops(); len(stops) < 3 || !slices.Equal(stops[:3], []uint16{4, 8, 12}) {
		t.Fatalf("每 4 列一站 = %v", terminals.TabStops())
	}
}

func TestClosedTerminalRefusesStopChange(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := terminals.TabStops()
	install := commandtest.WithTerminalHandle(t, terminals, Run)
	run := func(ctx context.Context, args []string) error {
		if closeErr := terminals.Close(); closeErr != nil {
			t.Fatalf("关闭终端: %v", closeErr)
		}
		return install(ctx, args)
	}
	r := commandtest.Run(t, run, t.TempDir(), []string{"-4"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "tabs: ") {
		t.Fatalf("已关闭的终端应拒绝设置 = %+v", r)
	}
	if !slices.Equal(terminals.TabStops(), before) {
		t.Fatalf("失败的设置不得改变制表位: %v", terminals.TabStops())
	}
}

func TestTerminalWithoutSetterCapability(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := terminals.TabStops()
	handler := commandtest.WithTerminalHandle(t, terminals, func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		table, ok := set.FD.(*fd.Table)
		if !ok {
			t.Fatalf("描述符表类型: %T", set.FD)
		}
		set.FD = descriptorsOnly{table}
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	})
	r := commandtest.Run(t, handler, t.TempDir(), []string{"-4"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "tabs: ") {
		t.Fatalf("缺制表位能力应失败 = %+v", r)
	}
	if !slices.Equal(terminals.TabStops(), before) {
		t.Fatalf("能力缺失不得改变制表位: %v", terminals.TabStops())
	}
}

func TestStopsAreNotClampedToWindow(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 10}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := commandtest.Run(t, withTerminal(t, terminals), t.TempDir(), []string{"-4"}, "")
	if r.Code != 0 {
		t.Fatalf("-4 = %+v", r)
	}
	stops := terminals.TabStops()
	if len(stops) != 256 || stops[255] != 1024 {
		t.Fatalf("超宽停靠点应照存: %d 个（末尾 %v）", len(stops), stops[len(stops)-1])
	}

	if _, err := terminals.Write([]byte("a\tb")); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := terminals.ScreenView()
	if !strings.HasPrefix(lines[0], "a  b") {
		t.Fatalf("tab 渲染 = %q", lines[0])
	}
}

func TestRejectsBareDashAndDoubleDash(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, withTerminal(t, terminals), t.TempDir(), args, "")
	}
	if r := run("-"); r.Code != 2 || !strings.Contains(r.Stderr, "类型") {
		t.Fatalf("裸 - = %+v", r)
	}
	if r := run("--"); r.Code != 2 || !strings.Contains(r.Stderr, "类型") {
		t.Fatalf("-- = %+v", r)
	}
}

func TestSpacingBoundaryValues(t *testing.T) {
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{Rows: 24, Cols: 80}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := commandtest.Run(t, withTerminal(t, terminals), t.TempDir(), []string{"-1"}, ""); r.Code != 0 {
		t.Fatalf("-1 = %+v", r)
	}
	stops := terminals.TabStops()
	if len(stops) != 1024 || stops[0] != 1 || stops[1023] != 1024 {
		t.Fatalf("每 1 列一站 = %d 个（%v...%v）", len(stops), stops[0], stops[len(stops)-1])
	}
	if r := commandtest.Run(t, withTerminal(t, terminals), t.TempDir(), []string{"-62"}, ""); r.Code != 0 {
		t.Fatalf("-62 = %+v", r)
	}
	stops = terminals.TabStops()
	if len(stops) != 16 || stops[0] != 62 || stops[15] != 992 {
		t.Fatalf("每 62 列一站 = %v", stops)
	}
}

func TestNoDescriptorCapability(t *testing.T) {
	handler := func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		set.FD = nil
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
	var sink strings.Builder
	err := commandtest.Invoke(context.Background(), handler, t.TempDir(), []string{"-4"}, strings.NewReader(""), &sink, &sink)
	if err == nil {
		t.Fatal("没有描述符能力应失败")
	}
}
