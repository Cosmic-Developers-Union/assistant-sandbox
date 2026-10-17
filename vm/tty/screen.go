package tty

import (
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

var errInvalidStop = errno.Wrap(errno.Invalid, "制表位列号必须为正数")

const (
	DefaultTabStop = 8

	maxTabStopValue = 1 << 12
)

type screen struct {
	rows, cols int
	cells      [][]byte
	row, col   int

	stops map[int]bool

	savedRow, savedCol int
}

func newScreen(rows, cols int) *screen {
	s := &screen{rows: rows, cols: cols, stops: map[int]bool{}}
	s.clear()
	return s
}

func (s *screen) clear() {
	s.cells = make([][]byte, s.rows)
	for i := range s.cells {
		s.cells[i] = bytesOf(' ', s.cols)
	}
	s.row, s.col = 0, 0
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func (s *screen) parse(p []byte) {
	state := parserGround
	var params strings.Builder
	var intermediates strings.Builder
	var final byte
	for _, b := range p {
		switch state {
		case parserGround:
			switch {
			case b == 0x1B:
				state = parserEscape
			case b < 0x20:
				s.control(b)
			default:
				s.put(b)
			}
		case parserEscape:
			switch b {
			case '[':
				params.Reset()
				intermediates.Reset()
				state = parserCSI
			case ']':
				state = parserOSC
			case 'c':
				s.clear()
				s.resetStops()
				state = parserGround
			case '7':
				s.savedRow, s.savedCol = s.row, s.col
				state = parserGround
			case '8':
				s.row, s.col = s.savedRow, s.savedCol
				s.clampCursor()
				state = parserGround
			case '(', ')':
				intermediates.WriteByte(b)
				state = parserCharset
			default:

				state = parserGround
			}
		case parserCharset:

			state = parserGround
		case parserOSC:

			switch b {
			case 0x07:
				state = parserGround
			case 0x1B:
				state = parserOSCEscape
			}
		case parserOSCEscape:
			if b == '\\' {
				state = parserGround
			} else {
				state = parserOSC
			}
		case parserCSI:
			switch {
			case b >= '0' && b <= '9' || b == ';' || b == '?' || b == ':' || b == '<' || b == '=' || b == '>':
				params.WriteByte(b)
			case b >= 0x20 && b <= 0x2F:
				intermediates.WriteByte(b)
			case b >= 0x40 && b <= 0x7E:
				final = b
				s.csi(params.String(), intermediates.String(), final)
				state = parserGround
			default:

				state = parserGround
			}
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

func (s *screen) put(b byte) {
	if s.col >= s.cols {
		s.col = 0
		s.lineFeed()
	}
	s.cells[s.row][s.col] = b
	s.col++
}

func (s *screen) lineFeed() {
	if s.row+1 >= s.rows {
		copy(s.cells, s.cells[1:])
		s.cells[s.rows-1] = bytesOf(' ', s.cols)
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
				s.cells[i] = bytesOf(' ', s.cols)
			}
		case 1:
			for i := 0; i < s.row; i++ {
				s.cells[i] = bytesOf(' ', s.cols)
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
			s.cells[s.row] = bytesOf(' ', s.cols)
		}
	}
}

func (s *screen) eraseLineRight() {
	for i := s.col; i < s.cols; i++ {
		s.cells[s.row][i] = ' '
	}
}

func (s *screen) eraseLineLeft() {
	for i := 0; i <= s.col && i < s.cols; i++ {
		s.cells[s.row][i] = ' '
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
		lines[i] = string(row)
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
