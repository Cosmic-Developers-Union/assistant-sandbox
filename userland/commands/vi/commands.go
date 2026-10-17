package vi

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const (
	keyESC = 0x1B
	keyCR  = 0x0D
	keyNL  = 0x0A
	keyTAB = 0x09
	keyBS  = 0x08
)

func (e *editor) loop(ctx context.Context) error {
	e.render()
	var pending []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var buffer [1]byte
		n, err := e.input.ReadContext(ctx, buffer[:])
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		key := buffer[0]
		if key == keyESC {

			pending = append(pending[:0], key)
			if seq, ok := e.readEscape(ctx, pending); ok {
				e.commandKey(seq)
			}
			e.render()
			continue
		}
		quit := e.commandKey([]byte{key})
		e.render()
		if quit {
			return nil
		}
	}
}

func (e *editor) readEscape(ctx context.Context, pending []byte) ([]byte, bool) {
	for len(pending) < 8 {
		var buffer [1]byte
		n, err := e.input.ReadContext(ctx, buffer[:])
		if err != nil {
			return nil, false
		}
		if n == 0 {
			continue
		}
		pending = append(pending, buffer[0])

		if len(pending) >= 3 && pending[1] == '[' && pending[len(pending)-1] >= 0x40 && pending[len(pending)-1] <= 0x7E {
			return pending, true
		}
		if len(pending) >= 2 && pending[1] != '[' {
			return nil, false
		}
	}
	return nil, false
}

func (e *editor) commandKey(key []byte) bool {
	if len(key) >= 3 && key[0] == keyESC && key[1] == '[' {
		switch key[len(key)-1] {
		case 'A':
			e.moveCursor(0, -1)
		case 'B':
			e.moveCursor(0, 1)
		case 'C':
			e.moveCursor(1, 0)
		case 'D':
			e.moveCursor(-1, 0)
		}
		return false
	}
	switch key[0] {
	case ':':
		return e.colonCommand()
	case 'h':
		e.moveCursor(-1, 0)
	case 'l':
		e.moveCursor(1, 0)
	case 'j', keyNL, keyCR:
		e.moveCursor(0, 1)
	case 'k':
		e.moveCursor(0, -1)
	case '0':
		if e.pending > 0 {
			e.pending *= 10
			return false
		}
		e.col = 0
	case '$':
		e.col = len(e.current())
	case '^':
		e.col = firstNonBlank(e.current())
	case 'w':
		e.nextWord()
	case 'b':
		e.prevWord()
	case 'G':
		e.gotoLine()
	case 'x':
		e.deleteChars(e.count(1))
	case 'X':
		if e.col > 0 {
			e.col--
			e.deleteChars(e.count(1))
		}
	case 'D':
		e.deleteChars(len(e.current()) - e.col)
	case 'd':
		if key, ok := e.readOne(); ok && key == 'd' {
			e.deleteLines(e.count(1))
		}
	case 'i':
		e.insertMode(false)
	case 'a':
		if e.col < len(e.current()) {
			e.col++
		}
		e.insertMode(false)
	case 'A':
		e.col = len(e.current())
		e.insertMode(false)
	case 'I':
		e.col = firstNonBlank(e.current())
		e.insertMode(false)
	case 'o':
		e.openLine(true)
		e.insertMode(false)
	case 'O':
		e.openLine(false)
		e.insertMode(false)
	case 's':
		e.deleteChars(e.count(1))
		e.insertMode(false)
	case 'S':
		e.deleteLines(e.count(1))
		e.openLine(false)
		e.insertMode(false)
	case 'p':
		e.paste(true)
	case 'P':
		e.paste(false)
	case 'u':
		e.undoEdit()
	case '/':
		e.search(1)
	case '?':
		e.search(-1)
	case 'n':
		e.repeatSearch()
	case 'N':
		e.lastDirection = -e.lastDirection
		e.repeatSearch()
		e.lastDirection = -e.lastDirection
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		e.pending = e.pending*10 + int(key[0]-'0')
		return false
	}
	return false
}

func (e *editor) readOne() (byte, bool) {
	ctx := context.Background()
	var buffer [1]byte
	n, err := e.input.ReadContext(ctx, buffer[:])
	if err != nil || n == 0 {
		return 0, false
	}
	return buffer[0], true
}

func (e *editor) count(fallback int) int {
	if e.pending > 0 {
		value := e.pending
		e.pending = 0
		return value
	}
	return fallback
}

func (e *editor) current() string { return e.lines[e.row] }

func (e *editor) moveCursor(dCol, dRow int) {
	e.row = clamp(e.row+dRow, 0, len(e.lines)-1)
	e.col = clamp(e.col+dCol, 0, len(e.current()))
	e.scrollIfNeeded()
}

func (e *editor) gotoLine() {
	target := len(e.lines) - 1
	if e.pending > 0 {
		target = clamp(e.count(1)-1, 0, len(e.lines)-1)
	}
	e.row = target
	e.col = firstNonBlank(e.current())
	e.scrollIfNeeded()
}

func firstNonBlank(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return i
		}
	}
	return 0
}

func wordClass(b byte) int {
	switch {
	case b == ' ' || b == '\t':
		return 0
	case b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_':
		return 1
	default:
		return 2
	}
}

func (e *editor) nextWord() {
	steps := e.count(1)
	for ; steps > 0; steps-- {
		row, col := e.row, e.col
		cls := func(r, c int) int {
			if r >= len(e.lines) || c >= len(e.lines[r]) {
				return -1
			}
			return wordClass(e.lines[r][c])
		}

		current := cls(row, col)
		if current > 0 {
			for col < len(e.lines[row]) && cls(row, col) == current {
				col++
			}
		}
		for {
			if row >= len(e.lines)-1 && col >= len(e.lines[row]) {
				e.row, e.col = row, len(e.lines[row])
				return
			}
			if col >= len(e.lines[row]) {
				row, col = row+1, 0
			}
			if cls(row, col) > 0 {
				e.row, e.col = row, col
				break
			}
			col++
		}
	}
}

func (e *editor) prevWord() {
	steps := e.count(1)
	for ; steps > 0; steps-- {
		row, col := e.row, e.col
		cls := func(r, c int) int {
			if r < 0 || c < 0 || c >= len(e.lines[r]) {
				return -1
			}
			return wordClass(e.lines[r][c])
		}

		col--
		if col < 0 {
			if row == 0 {
				return
			}
			row, col = row-1, len(e.lines[row-1])-1
		}
		for col >= 0 && cls(row, col) == 0 {
			col--
		}
		if col < 0 {
			for row > 0 {
				row--
				if len(e.lines[row]) > 0 && cls(row, len(e.lines[row])-1) > 0 {
					col = len(e.lines[row]) - 1
					break
				}
			}
		}

		if col < 0 {
			col = 0
		}
		for col > 0 && cls(row, col-1) == cls(row, col) && cls(row, col) > 0 {
			col--
		}
		e.row, e.col = row, col
	}
}

func (e *editor) insertMode(replace bool) {
	line := e.current()
	split, inserted := e.readInsertion()
	if split {

		e.lines[e.row] = e.current()[:e.col] + inserted + e.current()[e.col:]
		e.col += len(inserted)
	} else {

		e.lines[e.row] = line[:e.col] + inserted + line[e.col:]
		e.col += len(inserted)
	}
	e.markDirty()
}

func (e *editor) readInsertion() (bool, string) {
	ctx := context.Background()
	var inserted []byte
	split := false
	for {
		if err := ctx.Err(); err != nil {
			break
		}
		var buffer [1]byte
		n, err := e.input.ReadContext(ctx, buffer[:])
		if err != nil || n == 0 {
			break
		}
		key := buffer[0]
		if key == keyESC {
			break
		}
		switch key {
		case keyBS:
			if len(inserted) > 0 {
				inserted = inserted[:len(inserted)-1]
			}
		case keyCR, keyNL:

			original := e.current()
			head := original[:e.col] + string(inserted)
			rest := original[e.col:]
			e.lines[e.row] = head
			e.lines = append(e.lines[:e.row+1], append([]string{rest}, e.lines[e.row+1:]...)...)
			e.row++
			e.col = 0
			inserted = inserted[:0]
			split = true
			e.markDirty()
			e.render()
		default:
			inserted = append(inserted, key)
			e.markDirty()
		}
	}
	return split, string(inserted)
}

func (e *editor) openLine(below bool) {
	at := e.row
	if below {
		at++
	}
	e.lines = append(e.lines[:at], append([]string{""}, e.lines[at:]...)...)
	e.row = at
	e.col = 0
	e.markDirty()
}

func (e *editor) deleteChars(count int) {
	e.saveUndo()
	line := e.current()
	if e.col >= len(line) {

		if e.row < len(e.lines)-1 {
			e.register.lines, e.register.rows = nil, false
			e.register.chunk = ""
			e.lines[e.row] = line + e.lines[e.row+1]
			e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
			e.markDirty()
		}
		return
	}
	end := clamp(e.col+count, e.col+1, len(line))
	e.register.chunk = line[e.col:end]
	e.register.lines, e.register.rows = nil, false
	e.lines[e.row] = line[:e.col] + line[end:]
	e.markDirty()
}

func (e *editor) deleteLines(count int) {
	e.saveUndo()
	end := clamp(e.row+count, e.row+1, len(e.lines))
	e.register.lines = append([]string(nil), e.lines[e.row:end]...)
	e.register.rows = true
	e.register.chunk = ""
	e.lines = append(e.lines[:e.row], e.lines[end:]...)
	if len(e.lines) == 0 {
		e.lines = []string{""}
	}
	if e.row >= len(e.lines) {
		e.row = len(e.lines) - 1
	}
	e.col = clamp(e.col, 0, len(e.current()))
	e.markDirty()
}

func (e *editor) paste(after bool) {
	if e.register.rows {
		at := e.row
		if after {
			at++
		}
		pasted := append([]string(nil), e.register.lines...)
		e.lines = append(e.lines[:at], append(pasted, e.lines[at:]...)...)
		e.row = at
	} else {
		line := e.current()
		at := e.col
		if after && at < len(line) {
			at++
		}
		e.lines[e.row] = line[:at] + e.register.chunk + line[at:]
		e.col = at + len(e.register.chunk) - 1
		if e.col < 0 {
			e.col = 0
		}
	}
	e.markDirty()
}

func (e *editor) saveUndo() {
	copied := append([]string(nil), e.lines...)
	e.undo = &snapshot{lines: copied, row: e.row, col: e.col}
}

func (e *editor) undoEdit() {
	if e.undo == nil {
		e.status = "已是最初状态"
		return
	}
	e.lines, e.row, e.col = e.undo.lines, e.undo.row, e.undo.col
	e.undo = nil
	e.markDirty()
}

func (e *editor) markDirty() { e.dirty = true }

func (e *editor) search(direction int) {
	ctx := context.Background()
	var pattern []byte
	for {
		var buffer [1]byte
		n, err := e.input.ReadContext(ctx, buffer[:])
		if err != nil || n == 0 {
			return
		}
		if buffer[0] == keyCR || buffer[0] == keyNL {
			break
		}
		if buffer[0] == keyBS {
			if len(pattern) > 0 {
				pattern = pattern[:len(pattern)-1]
			}
			continue
		}
		pattern = append(pattern, buffer[0])
	}
	e.lastSearch = string(pattern)
	e.lastDirection = direction
	e.repeatSearch()
}

func (e *editor) repeatSearch() {
	if e.lastSearch == "" {
		e.status = "没有上次搜索"
		return
	}
	needle := e.lastSearch
	rows := len(e.lines)
	for step := 1; step <= rows; step++ {
		row := e.row + e.lastDirection*step

		if row < 0 {
			row += rows
		}
		if row >= rows {
			row -= rows
		}
		line := e.lines[row]
		var col int
		found := false

		homeRow := row == e.row
		if e.lastDirection > 0 {
			start := e.col + 1
			if !homeRow || start > len(line) {
				start = 0
			}
			if index := strings.Index(line[start:], needle); index >= 0 {
				col, found = start+index, true
			}
		} else {

			start := e.col - 1
			if !homeRow {
				start = len(line) - 1
			}
			if start >= len(line) {
				start = len(line) - 1
			}
			if start >= 0 {
				if index := strings.LastIndex(line[:start+1], needle); index >= 0 {
					col, found = index, true
				}
			}
		}
		if found {
			e.row, e.col = row, col
			e.scrollIfNeeded()
			return
		}
	}
	e.status = fmt.Sprintf("找不到 %q", needle)
}

func (e *editor) scrollIfNeeded() {
	if e.row < e.top {
		e.top = e.row
	}
	if e.row >= e.top+e.window() {
		e.top = e.row - e.window() + 1
	}
}

func (e *editor) window() int { return e.rows - 1 }

func (e *editor) colonCommand() bool {
	ctx := context.Background()
	var line []byte
	for {
		var buffer [1]byte
		n, err := e.input.ReadContext(ctx, buffer[:])
		if err != nil || n == 0 {
			return false
		}
		if buffer[0] == keyCR || buffer[0] == keyNL {
			break
		}
		if buffer[0] == keyESC {
			return false
		}
		if buffer[0] == keyBS {
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
			continue
		}
		line = append(line, buffer[0])
	}
	return e.executeColon(string(line))
}

func (e *editor) executeColon(command string) bool {
	command = strings.TrimSpace(command)
	switch {
	case command == "q" && !e.dirty:
		return true
	case command == "q":
		e.status = "有未保存的修改（:q! 放弃）"
	case command == "q!":
		return true
	case command == "w":
		e.write()
	case command == "wq":
		if e.write() {
			return true
		}
	case strings.HasPrefix(command, "w "):
		if err := e.writeFile(strings.TrimSpace(strings.TrimPrefix(command, "w "))); err != nil {
			e.status = err.Error()
		}
	case command == "n":
		e.status = "多文件编辑是声明子集"
	case strings.HasPrefix(command, "n "):
		e.status = "多文件编辑是声明子集"
	default:
		if target, err := strconv.Atoi(command); err == nil {
			e.row = clamp(target-1, 0, len(e.lines)-1)
			e.col = firstNonBlank(e.current())
			e.scrollIfNeeded()
			return false
		}
		e.status = fmt.Sprintf("不是编辑器命令: %s", command)
	}
	return false
}

func (e *editor) write() bool {
	if e.name == "" {
		e.status = "没有文件名（:w 文件）"
		return false
	}
	if err := e.writeFile(e.name); err != nil {
		e.status = err.Error()
		return false
	}
	return true
}

func (e *editor) writeFile(name string) error {
	ctx := context.Background()
	system := e.filesystem
	temp := fmt.Sprintf("%s.vi-%d", name, e.writeSeq)
	e.writeSeq++
	file, err := system.Open(ctx, temp, vmfsWriteCreate, 0o644)
	if err != nil {
		return fmt.Errorf("写 %s: %w", name, err)
	}
	if _, err := file.Write([]byte(joinLines(e.lines))); err != nil {
		_ = file.Close()
		return fmt.Errorf("写 %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("写 %s: %w", name, err)
	}
	if err := system.Rename(ctx, temp, name); err != nil {
		_ = system.Remove(ctx, temp)
		return fmt.Errorf("写 %s: %w", name, err)
	}
	e.name = name
	e.dirty = false
	e.status = fmt.Sprintf("已写入 %s", name)
	return nil
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

const vmfsWriteCreate = vmfs.WriteOnly | vmfs.Create | vmfs.Truncate

func (e *editor) render() {
	var out strings.Builder

	for screenRow := 0; screenRow < e.window(); screenRow++ {
		bufferRow := e.top + screenRow
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[K", screenRow+1)
		if bufferRow < len(e.lines) {
			line := e.lines[bufferRow]
			if len(line) > e.cols {
				line = line[:e.cols]
			}
			out.WriteString(line)
		}
	}

	fmt.Fprintf(&out, "\x1b[%d;1H\x1b[K", e.rows)
	status := e.status
	e.status = ""
	if status == "" {
		file := e.name
		if file == "" {
			file = "[未命名]"
		}
		modified := ""
		if e.dirty {
			modified = " [+] "
		}
		status = fmt.Sprintf("%s%s %d 行", file, modified, len(e.lines))
	}
	if len(status) > e.cols {
		status = status[:e.cols]
	}
	out.WriteString(status)

	fmt.Fprintf(&out, "\x1b[%d;%dH", e.row-e.top+1, e.col+1)
	_, _ = e.output.Write([]byte(out.String()))
}
