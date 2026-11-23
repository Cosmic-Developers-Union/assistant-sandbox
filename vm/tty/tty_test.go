package tty

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func recorder() (*[]signal.Number, Raise) {
	var got []signal.Number
	return &got, func(pgid proc.PID, n signal.Number) {
		got = append(got, n)
	}
}

func TestNewRejectsInvalidOutputLimit(t *testing.T) {
	if _, err := New(Flags{}, Size{}, 0, nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if _, err := New(Flags{}, Size{Rows: 5}, 64, nil); err != nil {
		t.Fatal(err)
	}

	if term, err := New(Flags{}, Size{}, 64, nil); err != nil || term.Size() != (Size{Rows: 24, Cols: 80}) {
		t.Fatalf("size=%+v err=%v", term.Size(), err)
	}
}

func TestCanonicalLineDiscipline(t *testing.T) {
	term, err := New(Flags{Canonical: true, Echo: true}, Size{}, 256, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := term.Read(make([]byte, 16)); !errors.Is(err, errno.WouldBlock) {
		t.Fatal(err)
	}
	if _, err := term.Input([]byte("hel")); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Input([]byte{charCR}); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 16)
	n, err := term.Read(buf)
	if err != nil || string(buf[:n]) != "hel\n" {
		t.Fatalf("n=%d buf=%q err=%v", n, buf[:n], err)
	}

	if out := string(term.Drain()); out != "hel\n" {
		t.Fatalf("echo=%q", out)
	}
}

func TestCanonicalEraseAndKill(t *testing.T) {
	term, err := New(Flags{Canonical: true, Echo: true}, Size{}, 256, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.Input([]byte("abc\x7f\x7fd\n")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	n, err := term.Read(buffer)
	if err != nil || string(buffer[:n]) != "ad\n" {
		t.Fatalf("read=%q err=%v", buffer[:n], err)
	}
	if echo := string(term.Drain()); echo != "abc\b \b\b \bd\n" {
		t.Fatalf("echo=%q", echo)
	}
	if _, err := term.Input([]byte("junk\x15ok\n")); err != nil {
		t.Fatal(err)
	}
	n, err = term.Read(buffer)
	if err != nil || string(buffer[:n]) != "ok\n" {
		t.Fatalf("kill read=%q err=%v", buffer[:n], err)
	}
}

func TestISIGRaisesSignalsToForeground(t *testing.T) {
	signals, raise := recorder()
	term, err := New(Flags{Canonical: true, ISIG: true}, Size{}, 256, raise)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.SetForeground(42); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Input([]byte{charINTR, 'x', charQUIT}); err != nil {
		t.Fatal(err)
	}
	if len(*signals) != 2 || (*signals)[0] != signal.INT || (*signals)[1] != signal.QUIT {
		t.Fatalf("signals=%v", *signals)
	}
	if term.Foreground() != 42 {
		t.Fatalf("fg=%d", term.Foreground())
	}
}

func TestEOFSubmitsLine(t *testing.T) {
	term, _ := New(Flags{Canonical: true}, Size{}, 256, nil)
	if _, err := term.Input([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Input([]byte{charEOF}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if n, err := term.Read(buf); err != nil || string(buf[:n]) != "partial" {
		t.Fatalf("n=%d buf=%q err=%v", n, buf[:n], err)
	}
}

func TestRawModeAndFlagSwitch(t *testing.T) {
	term, _ := New(Flags{}, Size{}, 256, nil)
	if _, err := term.Input([]byte("raw1")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if n, err := term.Read(buf); err != nil || string(buf[:n]) != "raw1" {
		t.Fatalf("n=%d buf=%q", n, buf[:n])
	}

	if err := term.SetFlags(Flags{Canonical: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Read(buf); !errors.Is(err, errno.WouldBlock) {
		t.Fatal(err)
	}

	if _, err := term.Input([]byte("r2")); err != nil {
		t.Fatal(err)
	}
}

func TestBlockingReadContext(t *testing.T) {
	term, _ := New(Flags{Canonical: true}, Size{}, 256, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16)
		n, err := term.ReadContext(context.Background(), buf)
		if err != nil || string(buf[:n]) != "go\n" {
			t.Errorf("n=%d buf=%q err=%v", n, buf[:n], err)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("读不应提前返回")
	default:
	}
	if _, err := term.Input([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("读未被唤醒")
	}
}

func TestOutputBackpressure(t *testing.T) {
	term, _ := New(Flags{}, Size{}, 8, nil)
	if n, err := term.Write([]byte("12345678")); n != 8 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := term.Write([]byte("x")); !errors.Is(err, errno.NoSpace) {
		t.Fatal(err)
	}
	if out := string(term.Drain()); out != "12345678" {
		t.Fatalf("out=%q", out)
	}
	if _, err := term.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
}

func TestWindowSizeChangeRaisesWINCH(t *testing.T) {
	signals, raise := recorder()
	term, _ := New(Flags{}, Size{}, 256, raise)
	if err := term.SetSize(Size{Rows: 0, Cols: 80}); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := term.SetSize(Size{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	if term.Size() != (Size{Rows: 40, Cols: 120}) {
		t.Fatal(term.Size())
	}
	if len(*signals) != 1 || (*signals)[0] != signal.WINCH {
		t.Fatalf("signals=%v", *signals)
	}
}

func TestPollReadinessAndOnChange(t *testing.T) {
	term, _ := New(Flags{Canonical: true}, Size{}, 256, nil)
	if term.Ready()&poll.Readable != 0 {
		t.Fatal("空终端不应可读")
	}
	results := make(chan struct{}, 1)
	cancel := term.OnChange(func() { results <- struct{}{} })
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = term.Input([]byte("line\n"))
	}()
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	if _, err := poll.WaitFor(ctx, nil, []poll.Entry{{Source: term, Want: poll.Readable}}, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-results:
	case <-time.After(time.Second):
		t.Fatal("OnChange 未触发")
	}
}

func TestCloseWakeAndIdempotence(t *testing.T) {
	term, _ := New(Flags{Canonical: true}, Size{}, 256, nil)
	errs := make(chan error, 1)
	go func() {
		_, err := term.ReadContext(context.Background(), make([]byte, 8))
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	if err := term.Close(); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, errno.Cancelled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭未唤醒阻塞读")
	}
	if _, err := term.Write([]byte("x")); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	if err := term.SetForeground(1); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
}

func TestClosedTerminalRejectsAllOperations(t *testing.T) {
	term, _ := New(Flags{Canonical: true, Echo: true}, Size{}, 256, nil)
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Input([]byte("x")); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("Input err=%v", err)
	}
	if _, err := term.Read(make([]byte, 8)); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("Read err=%v", err)
	}
	if err := term.SetFlags(Flags{}); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("SetFlags err=%v", err)
	}
	if err := term.SetSize(Size{Rows: 10, Cols: 10}); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("SetSize err=%v", err)
	}
}

func TestPartialLineReadKeepsRemainder(t *testing.T) {
	term, _ := New(Flags{Canonical: true}, Size{}, 256, nil)
	if _, err := term.Input([]byte("abcdef\n")); err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 3)
	n, err := term.Read(first)
	if err != nil || string(first[:n]) != "abc" {
		t.Fatalf("n=%d buf=%q err=%v", n, first[:n], err)
	}
	rest := make([]byte, 16)
	n, err = term.Read(rest)
	if err != nil || string(rest[:n]) != "def\n" {
		t.Fatalf("n=%d buf=%q err=%v", n, rest[:n], err)
	}
	if _, err := term.Read(rest); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("行读完后应无数据: %v", err)
	}
}

func TestRawReadRejectsEmptyBuffer(t *testing.T) {
	term, _ := New(Flags{}, Size{}, 256, nil)
	if n, err := term.Read(nil); n != 0 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := term.Input([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if n, err := term.Read(nil); n != 0 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	buf := make([]byte, 4)
	if n, err := term.Read(buf); err != nil || string(buf[:n]) != "x" {
		t.Fatalf("零长度读不应消费: n=%d buf=%q err=%v", n, buf[:n], err)
	}
}

func TestRawModePartialReadKeepsRemainder(t *testing.T) {
	term, _ := New(Flags{}, Size{}, 256, nil)
	if _, err := term.Input([]byte("xyz")); err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 2)
	n, err := term.Read(first)
	if err != nil || string(first[:n]) != "xy" {
		t.Fatalf("n=%d buf=%q err=%v", n, first[:n], err)
	}
	rest := make([]byte, 4)
	n, err = term.Read(rest)
	if err != nil || string(rest[:n]) != "z" {
		t.Fatalf("n=%d buf=%q err=%v", n, rest[:n], err)
	}
	if _, err := term.Read(rest); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("读完后应无数据: %v", err)
	}
}

func TestEchoRejectedWhenOutputBudgetExceeded(t *testing.T) {
	term, _ := New(Flags{Canonical: true, Echo: true}, Size{}, 4, nil)
	if _, err := term.Input([]byte("ab")); err != nil {
		t.Fatal(err)
	}

	if n, err := term.Input([]byte("cde")); n != 0 || !errors.Is(err, errno.NoSpace) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if out := string(term.Drain()); out != "ab" {
		t.Fatalf("被拒绝的输入不得回显: %q", out)
	}

	if _, err := term.Input([]byte("f")); err != nil {
		t.Fatal(err)
	}
	if out := string(term.Drain()); out != "f" {
		t.Fatalf("out=%q", out)
	}
}

func TestISIGStopCharacter(t *testing.T) {
	signals, raise := recorder()
	term, _ := New(Flags{Canonical: true, ISIG: true}, Size{}, 256, raise)
	if _, err := term.Input([]byte{charTSTP}); err != nil {
		t.Fatal(err)
	}
	if len(*signals) != 1 || (*signals)[0] != signal.TSTP {
		t.Fatalf("signals=%v", *signals)
	}
}

func TestFlagsAccessor(t *testing.T) {
	term, _ := New(Flags{Canonical: true, Echo: true}, Size{}, 64, nil)
	if got := term.Flags(); !got.Canonical || !got.Echo {
		t.Fatalf("flags=%+v", got)
	}
}

func TestSetForegroundRejectsZero(t *testing.T) {
	term, _ := New(Flags{}, Size{}, 64, nil)
	if err := term.SetForeground(0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestTerminalSatisfiesFDQueryContract(t *testing.T) {
	term, err := New(Flags{Canonical: true}, Size{Rows: 30, Cols: 100}, 256, nil)
	if err != nil {
		t.Fatal(err)
	}

	table := fd.New()
	table.Attach(1, fd.Streams{})
	id, err := table.InstallNext(1, term)
	if err != nil {
		t.Fatal(err)
	}
	info, err := table.Terminal(t.Context(), 1, id)
	if err != nil {
		t.Fatal(err)
	}
	rows, columns := info.Rows, info.Columns
	if !info.TTY || rows != 30 || columns != 100 {
		t.Fatalf("经 fd 表查询终端=(%+v) want TTY 30x100", info)
	}

	if err := term.SetSize(Size{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	if rows, columns = term.Dimensions(); rows != 40 || columns != 120 {
		t.Fatalf("Dimensions=(%d,%d) want (40,120)", rows, columns)
	}
}

func TestSetStateAppliesSizeAndRaisesWINCH(t *testing.T) {
	var raised []signal.Number
	terminal, err := New(Flags{Canonical: true, Echo: true, ISIG: true}, Size{Rows: 24, Cols: 80}, 4096,
		func(pgid proc.PID, n signal.Number) { raised = append(raised, n) })
	if err != nil {
		t.Fatal(err)
	}

	if err := terminal.SetState(fd.TerminalState{Canonical: true, Echo: true, ISIG: true, Mesg: true, Rows: 40, Columns: 120}); err != nil {
		t.Fatal(err)
	}
	state := terminal.State()
	if state.Rows != 40 || state.Columns != 120 {
		t.Fatalf("尺寸未生效: %+v", state)
	}
	if len(raised) != 1 || raised[0] != signal.WINCH {
		t.Fatalf("WINCH = %v", raised)
	}

	before := terminal.State()
	if err := terminal.SetState(fd.TerminalState{Canonical: true, Echo: true, ISIG: true, Mesg: true}); err != nil {
		t.Fatal(err)
	}
	after := terminal.State()
	if after.Rows != before.Rows || after.Columns != before.Columns {
		t.Fatalf("0 应保持尺寸: %+v vs %+v", before, after)
	}
	if len(raised) != 1 {
		t.Fatalf("不应再投递 WINCH: %v", raised)
	}
}

// TestScreenSnapshotIsAtomicUnderConcurrentWrites 验证 P12:ScreenView
// 是锁内构建的原子副本,并发写入只会看到写入前或写入后的完整帧;
// 返回的行可被调用方修改而不影响终端。
func TestScreenSnapshotIsAtomicUnderConcurrentWrites(t *testing.T) {
	term, err := New(Flags{}, Size{Rows: 2, Cols: 8}, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 500; index++ {
			frame := "\x1b[1;1HAAAA"
			if index%2 == 1 {
				frame = "\x1b[1;1HBBBB"
			}
			if _, writeErr := term.Write([]byte(frame)); writeErr != nil {
				return
			}
		}
	}()
	for {
		lines, row, column := term.ScreenView()
		if row != 0 || column < 0 || column > 8 {
			t.Fatalf("光标=(%d,%d)", row, column)
		}
		prefix := lines[0][:4]
		if prefix != "AAAA" && prefix != "BBBB" && prefix != "    " {
			t.Fatalf("撕裂的屏幕快照=%q", lines[0])
		}
		lines[0] = "mutated"
		select {
		case <-done:
			// 快照内容与终端状态解耦:再次读取不受修改影响。
			again, _, _ := term.ScreenView()
			if again[0] == "mutated" {
				t.Fatal("ScreenView 返回了终端内部行")
			}
			return
		default:
		}
	}
}

// TestControlSignalsTargetOnlyForeground 验证 P09:Ctrl-C/Ctrl-Z 与
// 挂断只向前台进程组投递,后台组不被误伤。
func TestControlSignalsTargetOnlyForeground(t *testing.T) {
	var raised []struct {
		pgid proc.PID
		n    signal.Number
	}
	raise := func(pgid proc.PID, n signal.Number) {
		raised = append(raised, struct {
			pgid proc.PID
			n    signal.Number
		}{pgid, n})
	}
	term, err := New(Flags{Canonical: true, ISIG: true}, Size{}, 256, raise)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.SetForeground(42); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Input([]byte{charINTR, charTSTP}); err != nil {
		t.Fatal(err)
	}
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	want := []signal.Number{signal.INT, signal.TSTP, signal.HUP}
	if len(raised) != len(want) {
		t.Fatalf("raised=%+v", raised)
	}
	for index, entry := range raised {
		if entry.pgid != 42 || entry.n != want[index] {
			t.Fatalf("signal[%d]=%+v want pgid=42 sig=%v", index, entry, want[index])
		}
	}
}
