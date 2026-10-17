package tty

import (
	"errors"
	"slices"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func newTestScreen(t *testing.T, rows, cols int) *screen {
	t.Helper()
	return newScreen(rows, cols)
}

func TestScreenRendersPlainBytesAndWrap(t *testing.T) {
	s := newTestScreen(t, 3, 4)
	s.parse([]byte("abcdefg"))
	lines, row, col := s.View()
	if lines[0] != "abcd" || lines[1] != "efg " {
		t.Fatalf("换行渲染不对: %q %q", lines[0], lines[1])
	}
	if row != 1 || col != 3 {
		t.Fatalf("光标 = (%d,%d), want (1,3)", row, col)
	}
}

func TestScreenControls(t *testing.T) {
	s := newTestScreen(t, 4, 10)
	s.parse([]byte("ab\rCD\ndef"))
	lines, _, _ := s.View()
	if lines[0] != "CD        " {
		t.Fatalf("CR 后覆盖: %q", lines[0])
	}
	if lines[1] != "  def     " {
		t.Fatalf("LF 后新行: %q", lines[1])
	}
	s.parse([]byte{0x08})
	_, row, col := s.View()
	if row != 1 || col != 4 {
		t.Fatalf("BS 光标 = (%d,%d), want (1,4)", row, col)
	}
}

func TestScreenScrollsAtBottom(t *testing.T) {
	s := newTestScreen(t, 3, 4)
	s.parse([]byte("1\r\n2\r\n3\r\n4"))
	lines, row, _ := s.View()
	if lines[0] != "2   " || lines[2] != "4   " {
		t.Fatalf("滚动后内容不对: %q", lines)
	}
	if row != 2 {
		t.Fatalf("滚动后光标行 = %d", row)
	}
}

func TestScreenCUPAndErase(t *testing.T) {
	s := newTestScreen(t, 3, 10)
	s.parse([]byte("hello"))

	s.parse([]byte("\x1b[2;1H\x1b[K"))
	lines, row, col := s.View()
	if lines[1] != "          " || lines[0] != "hello     " {
		t.Fatalf("ED 渲染不对: %q", lines)
	}
	if row != 1 || col != 0 {
		t.Fatalf("CUP 光标 = (%d,%d), want (1,0)", row, col)
	}

	s.parse([]byte("X\x1b[2K\x1b[2J"))
	lines, row, col = s.View()
	for i, line := range lines {
		if line != "          " {
			t.Fatalf("ED2 后第 %d 行应为空: %q", i, line)
		}
	}
	if row != 0 || col != 0 {
		t.Fatalf("ED2 后光标应归位: (%d,%d)", row, col)
	}
}

func TestScreenRelativeMoves(t *testing.T) {
	s := newTestScreen(t, 5, 10)
	s.parse([]byte("\x1b[3;4H\x1b[1A\x1b[2C\x1b[1D"))
	_, row, col := s.View()
	if row != 1 || col != 4 {
		t.Fatalf("相对移动后光标 = (%d,%d), want (1,4)", row, col)
	}
}

func TestScreenTabStops(t *testing.T) {
	s := newTestScreen(t, 2, 20)
	s.parse([]byte("a\tb"))
	_, _, col := s.View()
	if col != 9 {
		t.Fatalf("缺省制表位光标 = %d, want 9", col)
	}
	lines, _, _ := s.View()
	if lines[0][:9] != "a       b" {
		t.Fatalf("制表位渲染不对: %q", lines[0][:9])
	}

	small := newTestScreen(t, 1, 5)
	small.parse([]byte("ab\t"))
	_, _, col = small.View()
	if col != 4 {
		t.Fatalf("无下一站时光标 = %d, want 4", col)
	}
}

func TestScreenTabStopSetter(t *testing.T) {
	s := newTestScreen(t, 2, 20)
	if err := s.SetTabStops([]uint16{1, 5, 10}); err != nil {
		t.Fatal(err)
	}
	stops := s.TabStops()
	if !slices.Equal(stops, []uint16{1, 5, 10}) {
		t.Fatalf("制表位 = %v", stops)
	}
	s.parse([]byte("ab\t"))
	_, _, col := s.View()
	if col != 4 {
		t.Fatalf("自定义制表位光标 = %d, want 4", col)
	}

	if err := s.SetTabStops([]uint16{100}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.TabStops(), []uint16{100}) {
		t.Fatalf("超宽制表位 = %v", s.TabStops())
	}
	s.parse([]byte("a\t"))
	_, _, col = s.View()
	if col != 19 {
		t.Fatalf("超宽制表位应落到行末: %d", col)
	}
	if err := s.SetTabStops([]uint16{0}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("零列号应拒绝: %v", err)
	}

	if err := s.SetTabStops(nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.TabStops(), []uint16{9, 17}) {
		t.Fatalf("恢复缺省 = %v, want [9 17]", s.TabStops())
	}
}

func TestScreenSaveRestoreCursor(t *testing.T) {
	s := newTestScreen(t, 3, 10)
	s.parse([]byte("\x1b[2;3H\x1b7\x1b[3;1H\x1b8"))
	_, row, col := s.View()
	if row != 1 || col != 2 {
		t.Fatalf("恢复光标 = (%d,%d), want (1,2)", row, col)
	}
}

func TestScreenIgnoresUnrecognizedSequences(t *testing.T) {
	s := newTestScreen(t, 2, 10)

	s.parse([]byte("\x1b[31;1mRED\x1b[?25l\x1b]0;title\x07\x1b(Bok"))
	lines, _, _ := s.View()
	if lines[0] != "REDok     " {
		t.Fatalf("忽略序列后的文本: %q", lines[0])
	}
}

func TestTerminalScreenViewAndTabStops(t *testing.T) {
	terminal, err := New(Flags{Canonical: true, Echo: true}, Size{Rows: 4, Cols: 20}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.Write([]byte("hi\tthere")); err != nil {
		t.Fatal(err)
	}
	lines, _, col := terminal.ScreenView()

	if lines[0] != "hi      there       " {
		t.Fatalf("终端屏幕渲染: %q", lines[0])
	}
	if col != 13 {
		t.Fatalf("光标列 = %d, want 13", col)
	}

	var setter interface {
		TabStops() []uint16
		SetTabStops([]uint16) error
	} = terminal
	if err := setter.SetTabStops([]uint16{3}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(setter.TabStops(), []uint16{3}) {
		t.Fatalf("终端制表位 = %v", setter.TabStops())
	}

	if err := terminal.SetSize(Size{Rows: 2, Cols: 10}); err != nil {
		t.Fatal(err)
	}
	lines, _, _ = terminal.ScreenView()
	if len(lines) != 2 || lines[0] != "          " {
		t.Fatalf("resize 后应清屏: %q", lines)
	}
}

func TestScreenEraseLeftAndMesgAccessors(t *testing.T) {
	s := newTestScreen(t, 2, 10)
	s.parse([]byte("hello"))

	s.parse([]byte("\x1b[1;3H\x1b[1K"))
	lines, _, _ := s.View()
	if lines[0][:3] != "   " || lines[0][3:] != "lo        "[:7] {
		t.Fatalf("EL1 渲染 = %q", lines[0])
	}

	terminal, err := New(Flags{Canonical: true}, Size{Rows: 4, Cols: 20}, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !terminal.Mesg() {
		t.Fatal("缺省许可位应为 true")
	}
	if err := terminal.SetMesg(false); err != nil {
		t.Fatal(err)
	}
	if terminal.Mesg() {
		t.Fatal("SetMesg(false) 未生效")
	}
}
