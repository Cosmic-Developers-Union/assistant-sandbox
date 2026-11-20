package tty

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

var errInvalidStop = errno.Wrap(errno.Invalid, "制表位列号必须为正数")

const (
	DefaultTabStop = 8

	maxTabStopValue = 1 << 12
)

type screen struct {
	rows, cols int
	cells      [][]string
	row, col   int

	stops map[int]bool

	savedRow, savedCol int

	// 解析器状态跨 Write 调用保持:转义序列与多字节 UTF-8 字符可能
	// 被拆分到两次输出(P11)。
	state         parserState
	params        strings.Builder
	intermediates strings.Builder
	pending       []byte
}

func newScreen(rows, cols int) *screen {
	s := &screen{rows: rows, cols: cols, stops: map[int]bool{}}
	s.clear()
	return s
}

func (s *screen) clear() {
	s.cells = make([][]string, s.rows)
	for i := range s.cells {
		s.cells[i] = blankCells(s.cols)
	}
	s.row, s.col = 0, 0
}

func blankCells(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = " "
	}
	return out
}

func (s *screen) parse(p []byte) {
	data := append([]byte(nil), s.pending...)
	data = append(data, p...)
	s.pending = nil
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(data) {
			// 不完整的多字节序列:留给下一次 Write。
			s.pending = append([]byte(nil), data...)
			return
		}
		data = data[size:]
		s.advance(r)
	}
}

func (s *screen) advance(r rune) {
	switch s.state {
	case parserGround:
		switch {
		case r == 0x1B:
			s.state = parserEscape
		case r < 0x20:
			s.control(byte(r))
		case r == 0x7F:
			// DEL 不可见。
		default:
			s.putRune(r)
		}
	case parserEscape:
		switch r {
		case '[':
			s.params.Reset()
			s.intermediates.Reset()
			s.state = parserCSI
		case ']':
			s.state = parserOSC
		case 'c':
			s.clear()
			s.resetStops()
			s.state = parserGround
		case '7':
			s.savedRow, s.savedCol = s.row, s.col
			s.state = parserGround
		case '8':
			s.row, s.col = s.savedRow, s.savedCol
			s.clampCursor()
			s.state = parserGround
		case '(', ')':
			s.intermediates.WriteRune(r)
			s.state = parserCharset
		default:
			s.state = parserGround
		}
	case parserCharset:
		s.state = parserGround
	case parserOSC:
		switch r {
		case 0x07:
			s.state = parserGround
		case 0x1B:
			s.state = parserOSCEscape
		}
	case parserOSCEscape:
		if r == '\\' {
			s.state = parserGround
		} else {
			s.state = parserOSC
		}
	case parserCSI:
		switch {
		case r >= '0' && r <= '9' || r == ';' || r == '?' || r == ':' || r == '<' || r == '=' || r == '>':
			s.params.WriteRune(r)
		case r >= 0x20 && r <= 0x2F:
			s.intermediates.WriteRune(r)
		case r >= 0x40 && r <= 0x7E:
			s.csi(s.params.String(), s.intermediates.String(), byte(r))
			s.state = parserGround
		default:
			s.state = parserGround
		}
	}
}

type parserState uint8

const (
	parserGround parserState = iota
	parserEscape
	parserCSI
	parserOSC
	parserOSCEscape
	parserCharset
)

func (s *screen) control(b byte) {
	switch b {
	case 0x07:
	case 0x08:
		if s.col > 0 {
			s.col--
		}
	case 0x09:
		s.col = s.nextStop(s.col)
	case 0x0A:
		s.lineFeed()
	case 0x0C:
		s.col = 0
		s.lineFeed()
	case 0x0D:
		s.col = 0
	}
}

func (s *screen) putRune(r rune) {
	width := runewidth.RuneWidth(r)
	switch {
	case width == 0:
		s.combine(r)
		return
	case width < 0:
		width = 1
	case r == utf8.RuneError && width <= 0:
		width = 1
	}
	if s.col+width > s.cols {
		s.col = 0
		s.lineFeed()
	}
	if width > s.cols {
		width = s.cols
	}
	s.cells[s.row][s.col] = string(r)
	for offset := 1; offset < width && s.col+offset < s.cols; offset++ {
		s.cells[s.row][s.col+offset] = ""
	}
	s.col += width
}

// combine 把宽度为 0 的组合字符附加到前一个可见单元格(P11):宽字符
// 的续格为空串,需回退到其起始格。
func (s *screen) combine(r rune) {
	column := s.col - 1
	for column >= 0 && s.cells[s.row][column] == "" {
		column--
	}
	if column < 0 {
		return
	}
	s.cells[s.row][column] += string(r)
}

func (s *screen) lineFeed() {
	if s.row+1 >= s.rows {
		copy(s.cells, s.cells[1:])
		s.cells[s.rows-1] = blankCells(s.cols)
		s.row = s.rows - 1
		return
	}
	s.row++
}

func (s *screen) nextStop(col int) int {
	for candidate := col + 1; candidate < s.cols; candidate++ {
		if s.isStop(candidate) {
			return candidate
		}
	}
	return s.cols - 1
}

func (s *screen) isStop(col int) bool {
	if col <= 0 {
		return false
	}
	if len(s.stops) == 0 {
		return col%DefaultTabStop == 0
	}
	return s.stops[col]
}

func (s *screen) csi(params, intermediates string, final byte) {
	if intermediates != "" {
		return
	}
	fields := strings.Split(params, ";")
	num := func(index int, fallback int) int {
		if index >= len(fields) || fields[index] == "" {
			return fallback
		}
		value := 0
		for _, digit := range []byte(fields[index]) {
			if digit < '0' || digit > '9' {
				return fallback
			}
			value = value*10 + int(digit-'0')
			if value > 1<<20 {
				return fallback
			}
		}
		return value
	}
	one := func(index int) int {
		value := num(index, 1)
		if value == 0 {
			return 1
		}
		return value
	}
	switch final {
	case 'H', 'f':
		row, col := num(0, 1), num(1, 1)
		s.row = clamp(row-1, 0, s.rows-1)
		s.col = clamp(col-1, 0, s.cols-1)
	case 'A':
		s.row = clamp(s.row-one(0), 0, s.rows-1)
	case 'B':
		s.row = clamp(s.row+one(0), 0, s.rows-1)
	case 'C':
		s.col = clamp(s.col+one(0), 0, s.cols-1)
	case 'D':
		s.col = clamp(s.col-one(0), 0, s.cols-1)
	case 'G':
		s.col = clamp(one(0)-1, 0, s.cols-1)
	case 'd':
		s.row = clamp(one(0)-1, 0, s.rows-1)
	case 'J':
		switch num(0, 0) {
		case 0:
			s.eraseLineRight()
			for i := s.row + 1; i < s.rows; i++ {
				s.cells[i] = blankCells(s.cols)
			}
		case 1:
			for i := 0; i < s.row; i++ {
				s.cells[i] = blankCells(s.cols)
			}
			s.eraseLineLeft()
		default:
			s.clear()
		}
	case 'K':
		switch num(0, 0) {
		case 0:
			s.eraseLineRight()
		case 1:
			s.eraseLineLeft()
		default:
			s.cells[s.row] = blankCells(s.cols)
		}
	}
}

func (s *screen) eraseLineRight() {
	for column := s.col; column < s.cols; {
		content := s.cells[s.row][column]
		s.clearCell(s.row, column)
		if content == "" {
			column++
			continue
		}
		width := runewidth.StringWidth(content)
		if width < 1 {
			width = 1
		}
		column += width
	}
}

func (s *screen) eraseLineLeft() {
	for column := 0; column <= s.col && column < s.cols; column++ {
		s.clearCell(s.row, column)
	}
}

// clearCell 清除一个完整字符格(P11):宽字符的前导格与空续格一起
// 清除,避免擦除后留下半个宽字符。
func (s *screen) clearCell(row, column int) {
	start := column
	for start > 0 && s.cells[row][start] == "" {
		start--
	}
	width := runewidth.StringWidth(s.cells[row][start])
	if width < 1 {
		width = 1
	}
	for offset := 0; offset < width && start+offset < s.cols; offset++ {
		s.cells[row][start+offset] = " "
	}
}

func clamp(value, low, high int) int {
	switch {
	case value < low:
		return low
	case value > high:
		return high
	default:
		return value
	}
}

func (s *screen) clampCursor() {
	s.row = clamp(s.row, 0, s.rows-1)
	s.col = clamp(s.col, 0, s.cols-1)
}

func (s *screen) View() ([]string, int, int) {
	lines := make([]string, s.rows)
	for i, row := range s.cells {
		lines[i] = strings.Join(row, "")
	}
	return lines, s.row, s.col
}

func (s *screen) TabStops() []uint16 {
	var out []uint16
	if len(s.stops) == 0 {
		for col := DefaultTabStop; col < s.cols; col += DefaultTabStop {
			out = append(out, uint16(col+1))
		}
		return out
	}
	for col := 0; col <= maxTabStopValue; col++ {
		if s.stops[col] {
			out = append(out, uint16(col+1))
		}
	}
	return out
}

func (s *screen) SetTabStops(stops []uint16) error {
	if len(stops) == 0 {
		s.resetStops()
		return nil
	}
	next := map[int]bool{}
	for _, stop := range stops {
		if stop == 0 || int(stop) > maxTabStopValue {
			return errInvalidStop
		}

		next[int(stop)-1] = true
	}
	s.stops = next
	return nil
}

func (s *screen) resetStops() {
	s.stops = map[int]bool{}
}
