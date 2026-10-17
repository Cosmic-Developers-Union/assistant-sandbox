package editor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/bre"
)

func (e *Engine) changeLines(first, last int, lines []string) error {
	e.saveUndo()
	if e.Buffer.Last() == 0 && (first == 0 || first == 1) && (last == 0 || last == 1) {
		err := e.appendLinesNoUndo(0, lines)
		if err == nil && len(lines) > 0 {
			e.changeSeq++
		}
		return err
	}
	if err := e.deleteLinesNoUndo(first, last); err != nil {
		return err
	}
	if len(lines) == 0 {
		e.changeSeq++
		return nil
	}
	err := e.appendLinesNoUndo(first-1, lines)
	if err == nil {
		e.changeSeq++
	}
	return err
}

func (e *Engine) appendLinesNoUndo(after int, lines []string) error {
	if err := e.Buffer.Append(after, lines); err != nil {
		return err
	}
	for k, n := range e.marks {
		if n > after {
			e.marks[k] = n + len(lines)
		}
	}
	e.adjustGlobalInsert(after, len(lines))
	return nil
}

func (e *Engine) deleteLinesNoUndo(first, last int) error {
	if err := e.Buffer.Delete(first, last); err != nil {
		return err
	}
	delta := last - first + 1
	e.adjustGlobalDelete(first, last)
	for k, n := range e.marks {
		if n >= first && n <= last {
			delete(e.marks, k)
		} else if n > last {
			e.marks[k] = n - delta
		}
	}
	return nil
}

func (e *Engine) join(first, last int) error {
	if first < 1 || last < first || last > e.Buffer.Last() {
		return fmt.Errorf("地址无效")
	}
	if first == last {
		return nil
	}
	e.saveUndo()
	joined := strings.Join(append([]string(nil), e.Buffer.Lines[first-1:last]...), "")
	var joinedMarks []byte
	for k, n := range e.marks {
		if n >= first && n <= last {
			joinedMarks = append(joinedMarks, k)
		}
	}
	if err := e.deleteLinesNoUndo(first, last); err != nil {
		return err
	}
	if err := e.appendLinesNoUndo(first-1, []string{joined}); err != nil {
		return err
	}
	for _, k := range joinedMarks {
		e.marks[k] = first
	}
	e.changeSeq++
	return nil
}

func (e *Engine) addressAndSuffix(arg string) (int, byte, error) {
	pos := skipBlank(arg, 0)
	a, next, ok, err := e.parseAddress(arg, pos)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		a, next = e.Buffer.Current, pos
	}
	style, err := printSuffix(arg[next:])
	return a, style, err
}

func (e *Engine) copyLines(first, last, dest int) error {
	if dest < 0 || dest > e.Buffer.Last() {
		return fmt.Errorf("目的地无效")
	}
	lines := append([]string(nil), e.Buffer.Lines[first-1:last]...)
	return e.appendLines(dest, lines)
}

func (e *Engine) move(first, last, dest int) error {
	if dest < 0 || dest > e.Buffer.Last() {
		return fmt.Errorf("目的地无效")
	}
	if dest == last || dest == first-1 {

		e.Buffer.Current = last
		return nil
	}
	if dest >= first && dest < last {
		return fmt.Errorf("目的地无效")
	}
	e.saveUndo()
	lines := append([]string(nil), e.Buffer.Lines[first-1:last]...)

	movedMarks := map[byte]int{}
	for k, n := range e.marks {
		if n >= first && n <= last {
			movedMarks[k] = n - first
		}
	}
	if err := e.deleteLinesNoUndo(first, last); err != nil {
		return err
	}
	if dest > last {
		dest -= last - first + 1
	}
	if err := e.appendLinesNoUndo(dest, lines); err != nil {
		return err
	}
	for k, off := range movedMarks {
		e.marks[k] = dest + 1 + off
	}
	e.changeSeq++
	return nil
}

func (e *Engine) readFile(after int, arg string) error {
	name := trimBlank(arg)
	if strings.HasPrefix(name, "!") {
		return fmt.Errorf("不支持 shell 形式的文件命令（客体只支持独立的 ! 命令）")
	}
	if name == "" {
		name = e.Filename
	}
	if name == "" {
		return fmt.Errorf("没有当前文件名")
	}
	data, err := e.Files.Read(name)
	if err != nil {
		return e.fileDiagnostic(err)
	}
	if err := e.appendLines(after, splitText(data)); err != nil {
		return err
	}

	if !strings.HasPrefix(trimBlank(arg), "!") && e.Filename == "" {
		e.Filename = name
	}
	if !e.Silent {
		_, _ = fmt.Fprintln(e.Out, len(data))
	}
	return nil
}

func (e *Engine) write(addrs []int, explicit bool, arg string, appendMode bool) error {
	if arg != "" && arg[0] != ' ' && arg[0] != '\t' {
		return fmt.Errorf("文件参数需要空白分隔")
	}
	name := trimBlank(arg)
	if strings.HasPrefix(name, "!") {
		return fmt.Errorf("不支持 shell 形式的文件命令（客体只支持独立的 ! 命令）")
	}
	if name == "" {
		name = e.Filename
	}
	if name == "" {
		return fmt.Errorf("没有当前文件名")
	}
	first, last := 1, e.Buffer.Last()
	if explicit {
		var err error
		first, last, err = e.twoAddresses(addrs, true, first, last, false)
		if err != nil {
			return err
		}
	}
	var lines []string
	if last >= first && last > 0 {
		lines = e.Buffer.Lines[first-1 : last]
	}
	data := joinLines(lines)
	if appendMode {
		if e.Files.Append == nil {
			return fmt.Errorf("追加写出不可用")
		}
		if err := e.Files.Append(name, data); err != nil {
			return e.fileDiagnostic(err)
		}
	} else if err := e.Files.Write(name, data); err != nil {
		return e.fileDiagnostic(err)
	}
	if e.Filename == "" {
		e.Filename = name
	}
	if !appendMode && first == 1 && last == e.Buffer.Last() {
		e.Buffer.Dirty = false
	}
	e.warned = false
	if !e.Silent {
		_, _ = fmt.Fprintln(e.Out, len(data))
	}
	return nil
}

func (e *Engine) shellEscape(arg string) error {
	if e.Shell == nil {
		return fmt.Errorf("shell 逃逸不可用")
	}
	command, expanded, err := e.prepareShell(trimBlank(arg))
	if err != nil {
		return err
	}
	if expanded {
		if _, err := fmt.Fprintln(e.Out, command); err != nil {
			return err
		}
	}
	if err := e.Shell(command); err != nil {
		return err
	}
	if !e.Silent {
		if _, err := fmt.Fprintln(e.Out, "!"); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) prepareShell(command string) (string, bool, error) {
	if command == "" {
		return "", false, fmt.Errorf("需要 shell 命令")
	}
	replaced := false
	if command[0] == '!' {
		if e.lastShell == "" {
			return "", false, fmt.Errorf("没有上一个 shell 命令")
		}
		command = e.lastShell + command[1:]
		replaced = true
	}
	var expanded strings.Builder
	for i := 0; i < len(command); i++ {
		if command[i] == '\\' && i+1 < len(command) &&
			(command[i+1] == '%' || command[i+1] == '!') {
			expanded.WriteByte(command[i+1])
			i++
			continue
		}
		if command[i] == '%' {
			if e.Filename == "" {
				return "", false, fmt.Errorf("没有当前文件名")
			}
			expanded.WriteString(e.Filename)
			replaced = true
			continue
		}
		expanded.WriteByte(command[i])
	}
	e.lastShell = expanded.String()
	return e.lastShell, replaced, nil
}

func (e *Engine) oneAddress(addrs []int, explicit bool, def int, allowZero bool) (int, error) {
	a := def
	if explicit {
		a = addrs[len(addrs)-1]
	}
	if a < 0 || a > e.Buffer.Last() || (!allowZero && a == 0) {
		return 0, fmt.Errorf("地址无效")
	}
	return a, nil
}

func (e *Engine) twoAddresses(addrs []int, explicit bool, defFirst, defLast int, allowZero bool) (int, int, error) {
	first, last := defFirst, defLast
	if explicit {
		last = addrs[len(addrs)-1]
		first = last
		if len(addrs) > 1 {
			first = addrs[len(addrs)-2]
		}
	}
	if first > last || first < 0 || last > e.Buffer.Last() || (!allowZero && first == 0) {
		return 0, 0, fmt.Errorf("地址无效")
	}
	return first, last, nil
}

func (e *Engine) printRange(addrs []int, max int, style byte) error {
	first, last, err := e.twoAddresses(addrs, len(addrs) > 0, e.Buffer.Current, e.Buffer.Current, false)
	if max == 1 {
		last, err = e.oneAddress(addrs, len(addrs) > 0, e.Buffer.Current, false)
		first = last
	}
	if err != nil {
		return err
	}
	for n := first; n <= last; n++ {
		line := e.Buffer.Lines[n-1]
		switch style {
		case 'n':
			_, _ = fmt.Fprintf(e.Out, "%d\t%s\n", n, line)
		case 'l':
			_, _ = fmt.Fprintln(e.Out, listLine(line))
		default:
			if e.List {
				_, _ = fmt.Fprintln(e.Out, listLine(line))
			} else if e.Number {
				_, _ = fmt.Fprintf(e.Out, "%d\t%s\n", n, line)
			} else {
				_, _ = fmt.Fprintln(e.Out, line)
			}
		}
	}
	e.Buffer.Current = last
	return nil
}

const listWidth = 76

func listLine(s string) string {
	var out strings.Builder
	col := 0
	emit := func(unit string, width int) {
		if col >= listWidth {
			out.WriteString("\\\n")
			col = 0
		}
		out.WriteString(unit)
		col += width
	}
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			emit(fmt.Sprintf("\\%03o", s[0]), 4)
			s = s[1:]
			continue
		}
		encoded := s[:size]
		s = s[size:]
		switch r {
		case '\\':
			emit("\\\\", 2)
		case '\a':
			emit("\\a", 2)
		case '\t':
			emit("\\t", 2)
		case '\b':
			emit("\\b", 2)
		case '\r':
			emit("\\r", 2)
		case '\f':
			emit("\\f", 2)
		case '\v':
			emit("\\v", 2)
		case '$':
			emit("\\$", 2)
		default:
			if unicode.IsPrint(r) {
				emit(encoded, 1)
			} else {
				var b strings.Builder
				for i := 0; i < len(encoded); i++ {
					fmt.Fprintf(&b, "\\%03o", encoded[i])
				}
				emit(b.String(), 4*len(encoded))
			}
		}
	}
	out.WriteByte('$')
	return out.String()
}

func (e *Engine) substitute(first, last int, arg string) error {
	if arg == "" {
		return fmt.Errorf("替换表达式缺失")
	}
	delim, delimLen := e.commandDelimiter(arg)
	if delim == 0 || delim == ' ' || delim == '\n' || delim == '\\' {
		return fmt.Errorf("替换分隔符无效")
	}
	pattern, rest, closed := scanRE(arg[delimLen:], delim)
	if !closed {
		return fmt.Errorf("正则未闭合")
	}
	replacement, flags, replacementClosed, err := delimitedBy(rest, delim)
	if err != nil {
		return err
	}
	if pattern == "" {
		pattern = e.lastRE
	}
	if pattern == "" {
		return fmt.Errorf("没有上一个正则")
	}
	if replacement == "%" {
		if !e.hasReplacement {
			return fmt.Errorf("没有上一个替换")
		}
		replacement = e.lastReplacement
	}
	re, err := e.compileRE(pattern)
	if err != nil {
		return err
	}
	global, count, style, err := parseSubFlags(flags)
	if err != nil {
		return err
	}
	if !replacementClosed {

		style = 'p'
	}
	e.lastRE, e.lastReplacement, e.hasReplacement = pattern, replacement, true
	oldUndo, oldUndoMarks, oldUndoValid := e.undoBuffer.Clone(), cloneMarks(e.undoMarks), e.undoValid
	e.saveUndo()
	changed, lastChanged := false, 0
	offset := 0
	for original := first; original <= last; original++ {
		n := original + offset
		line := e.Buffer.Lines[n-1]
		matches, err := re.FindAllStringSubmatchIndex(line, -1)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			continue
		}
		selected := make([]bool, len(matches))
		if count > 0 {
			if count > len(matches) {
				continue
			}
			selected[count-1] = true
		} else if global {
			for i := range selected {
				selected[i] = true
			}
		} else {
			selected[0] = true
		}
		var b strings.Builder
		at := 0
		for i, m := range matches {
			if !selected[i] {
				continue
			}
			b.WriteString(line[at:m[0]])
			b.WriteString(expandReplacement(replacement, line, m))
			at = m[1]
		}
		b.WriteString(line[at:])
		pieces := strings.Split(b.String(), "\n")
		delta := len(pieces) - 1
		updated := make([]string, 0, len(e.Buffer.Lines)+delta)
		updated = append(updated, e.Buffer.Lines[:n-1]...)
		updated = append(updated, pieces...)
		updated = append(updated, e.Buffer.Lines[n:]...)
		e.Buffer.Lines = updated

		for i, target := range e.globalTargets {
			switch {
			case target == n:
				e.globalTargets[i] = 0
			case target > n:
				e.globalTargets[i] = target + delta
			}
		}
		for k, mark := range e.marks {
			if mark == n {

				e.marks[k] = mark + delta
			} else if mark > n {
				e.marks[k] = mark + delta
			}
		}
		offset += delta
		changed, lastChanged = true, n+len(pieces)-1
	}
	if !changed {
		if e.inGlobal {

			if e.globalSubCommand >= 0 {
				e.globalSubAttempt[e.globalSubCommand] = true
			}
			return nil
		}
		e.undoBuffer, e.undoMarks, e.undoValid = oldUndo, oldUndoMarks, oldUndoValid
		return fmt.Errorf("没有匹配")
	}
	e.Buffer.Current = lastChanged
	e.Buffer.Dirty = true
	e.changeSeq++
	if e.inGlobal && e.globalSubCommand >= 0 {
		e.globalSubAttempt[e.globalSubCommand] = true
		e.globalSubChanged[e.globalSubCommand] = true
	}
	if style != 0 {
		return e.printRange([]int{lastChanged}, 1, style)
	}
	return nil
}

type breMatcher struct{ re *bre.Regexp }

func (m breMatcher) MatchString(s string) (bool, error) { return m.re.MatchStringErr(s) }

func (m breMatcher) FindAllStringSubmatchIndex(s string, n int) ([][]int, error) {
	return m.re.FindAllStringSubmatchIndexErr(s, n)
}

func (e *Engine) compileRE(pattern string) (*breMatcher, error) {
	re, err := bre.Compile(pattern)
	if err != nil {
		return nil, err
	}
	re.Longest()
	return &breMatcher{re: re}, nil
}

func (e *Engine) commandDelimiter(s string) (byte, int) {
	if s == "" {
		return 0, 0
	}
	return s[0], 1
}

func parseSubFlags(s string) (global bool, count int, style byte, err error) {
	for i := 0; i < len(s); {
		switch s[i] {
		case 'g':
			global = true
			i++
		case 'p', 'n', 'l':
			style = s[i]
			i++
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			j := i + 1
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			count, err = strconv.Atoi(s[i:j])
			if err != nil || count == 0 {
				return false, 0, 0, fmt.Errorf("替换计数无效")
			}
			i = j
		case ' ', '\t':
			i++
		default:
			return false, 0, 0, fmt.Errorf("替换标志无效")
		}
	}
	return
}

func expandReplacement(repl, src string, match []int) string {
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		if repl[i] == '&' {
			b.WriteString(src[match[0]:match[1]])
			continue
		}
		if repl[i] == '\\' && i+1 < len(repl) {
			i++
			if repl[i] >= '1' && repl[i] <= '9' {
				n := int(repl[i]-'0') * 2
				if n+1 < len(match) && match[n] >= 0 {
					b.WriteString(src[match[n]:match[n+1]])
				}
			} else {
				b.WriteByte(repl[i])
			}
			continue
		}
		b.WriteByte(repl[i])
	}
	return b.String()
}

func scanRE(s string, delim byte) (pattern, rest string, closed bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == delim {
			return b.String(), s[i+1:], true
		}
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == delim {
			b.WriteByte(delim)
			i += 2
			continue
		}
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if s[i] == '[' {
			if end := bracketEnd(s, i); end > 0 {
				b.WriteString(s[i:end])
				i = end
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), "", false
}

func bracketEnd(s string, start int) int {
	i := start + 1
	if i < len(s) && s[i] == '^' {
		i++
	}
	if i < len(s) && s[i] == ']' {
		i++
	}
	for i < len(s) {
		switch {
		case s[i] == ']':
			return i + 1
		case s[i] == '[' && i+1 < len(s) && (s[i+1] == ':' || s[i+1] == '=' || s[i+1] == '.'):
			term := string(s[i+1]) + "]"
			end := strings.Index(s[i+2:], term)
			if end < 0 {
				return 0
			}
			i += 2 + end + len(term)
		default:
			i++
		}
	}
	return 0
}

func delimitedBy(s string, delim byte) (text, rest string, closed bool, err error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == delim {
			return b.String(), s[i+1:], true, nil
		}
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == delim {
			b.WriteByte(delim)
			i += 2
			continue
		}
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), "", false, nil
}

func (e *Engine) global(addrs []int, explicit bool, arg string, match bool) (retErr error) {
	first, last, err := e.twoAddresses(addrs, explicit, 1, e.Buffer.Last(), false)
	if err != nil {
		return err
	}
	if arg == "" {
		return fmt.Errorf("全局命令缺少正则")
	}
	delim, delimLen := e.commandDelimiter(arg)
	if delim == ' ' || delim == '\n' || delim == '\\' {
		return fmt.Errorf("正则分隔符无效")
	}
	pattern, rest, closed := scanRE(arg[delimLen:], delim)
	if !closed {
		rest = ""
	}
	if pattern == "" {
		pattern = e.lastRE
	}
	if pattern == "" {
		return fmt.Errorf("没有上一个正则")
	}
	re, err := e.compileRE(pattern)
	if err != nil {
		return err
	}
	e.lastRE = pattern
	command := trimBlank(rest)
	if command == "" {
		command = "p"
	}

	for hasOddTrailingBackslashes(lastLine(command)) {
		next, readErr := e.readCommandLine()
		if readErr != nil && len(next) == 0 {
			return fmt.Errorf("命令列表未闭合")
		}
		command += "\n" + strings.TrimSuffix(next, "\n")
	}
	targets := make([]int, 0)
	for n := first; n <= last; n++ {
		ok, err := re.MatchString(e.Buffer.Lines[n-1])
		if err != nil {
			return err
		}
		if ok == match {
			targets = append(targets, n)
		}
	}
	oldUndo, oldUndoMarks, oldUndoValid := e.undoBuffer.Clone(), cloneMarks(e.undoMarks), e.undoValid
	beforeSeq := e.changeSeq
	e.saveUndo()
	previousInGlobal := e.inGlobal
	e.inGlobal = true
	e.globalTargets = targets
	e.globalSubCommand = -1
	e.globalSubAttempt = nil
	e.globalSubChanged = nil
	defer func() {
		e.inGlobal = previousInGlobal
		e.globalTargets = nil
		e.globalSubCommand = -1
		e.globalSubAttempt = nil
		e.globalSubChanged = nil
		if beforeSeq == e.changeSeq {
			if retErr != nil {

				e.undoBuffer, e.undoMarks, e.undoValid = oldUndo, oldUndoMarks, oldUndoValid
			} else {

				e.undoBuffer, e.undoMarks, e.undoValid = e.Buffer.Clone(), cloneMarks(e.marks), true
				e.changeSeq++
			}
		}
	}()
	for targetIndex := range e.globalTargets {
		n := e.globalTargets[targetIndex]
		if n == 0 {
			continue
		}

		e.globalTargets[targetIndex] = 0
		e.Buffer.Current = n
		quit, execErr := e.executeGlobalList(command)
		if execErr != nil {
			return execErr
		}
		if quit {
			e.globalQuit = true
			return nil
		}
	}
	for i := range e.globalSubAttempt {
		if e.globalSubAttempt[i] && !e.globalSubChanged[i] {
			return fmt.Errorf("没有匹配")
		}
	}
	return nil
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func (e *Engine) executeGlobalList(commands string) (bool, error) {
	savedList := e.list
	e.list = bufio.NewReader(strings.NewReader(commands + "\n"))
	defer func() {
		e.list = savedList
	}()
	subCommand := 0
	for {
		line, err := e.readCommandLine()
		if err != nil && len(line) == 0 {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
		line = strings.TrimSuffix(line, "\n")
		_, p, _, parseErr := e.parseAddresses(line)
		if parseErr != nil {
			return false, parseErr
		}
		p = skipBlank(line, p)
		letter := commandLetter(line)
		if letter != 's' || e.closedSubstitutionAt(line, p) {
			line = stripContinuation(line)
		}
		if p < len(line) && strings.ContainsRune("acigv", rune(line[p])) {
			if strings.ContainsRune("gv", rune(line[p])) {
				return false, fmt.Errorf("不能嵌套全局命令")
			}
			return false, fmt.Errorf("全局命令列表不允许 %c 命令", line[p])
		}
		if letter == 's' {
			e.globalSubCommand = subCommand
			subCommand++
			for len(e.globalSubAttempt) <= e.globalSubCommand {
				e.globalSubAttempt = append(e.globalSubAttempt, false)
				e.globalSubChanged = append(e.globalSubChanged, false)
			}
		} else {
			e.globalSubCommand = -1
		}
		quit, execErr := e.execute(line)
		if execErr != nil || quit {
			return quit, execErr
		}
		if err != nil {
			return false, nil
		}
	}
}

func (e *Engine) closedSubstitutionAt(line string, p int) bool {
	if p >= len(line) || line[p] != 's' || p+1 >= len(line) {
		return false
	}
	arg := line[p+1:]
	delim, delimLen := e.commandDelimiter(arg)
	if delim == 0 || delim == ' ' || delim == '\n' || delim == '\\' {
		return false
	}
	_, rest, patternClosed := scanRE(arg[delimLen:], delim)
	if !patternClosed {
		return false
	}
	_, _, replacementClosed, err := delimitedBy(rest, delim)
	return err == nil && replacementClosed
}
