package editor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Files struct {
	Read   func(name string) (string, error)
	Write  func(name string, data string) error
	Append func(name string, data string) error
}

type Engine struct {
	Buffer Buffer

	Out, Diag io.Writer
	Files     Files

	Shell func(command string) error

	ReportError func(detail string)
	Silent      bool
	Prompt      string
	Filename    string

	Number bool
	List   bool

	lastRE           string
	lastReplacement  string
	hasReplacement   bool
	lastDiagnostic   string
	lastShell        string
	promptSetting    string
	help             bool
	hadError         bool
	warned           bool
	reader           *bufio.Reader
	list             *bufio.Reader
	marks            map[byte]int
	undoBuffer       Buffer
	undoMarks        map[byte]int
	undoValid        bool
	inGlobal         bool
	globalQuit       bool
	globalTargets    []int
	globalSubCommand int
	globalSubAttempt []bool
	globalSubChanged []bool
	changeSeq        uint64
}

var errWarned = errors.New("警告：缓冲已修改")

const zWindow = 20

func (e *Engine) Start(in io.Reader) { e.reader = bufio.NewReader(in) }

func (e *Engine) NextLine(withPrompt bool) (string, error) {
	if withPrompt && e.Prompt != "" {
		if _, err := fmt.Fprint(e.Out, e.Prompt); err != nil {
			return "", err
		}
	}
	line, err := e.reader.ReadString('\n')
	return strings.TrimSuffix(line, "\n"), err
}

func (e *Engine) Run(in io.Reader) int {
	e.Start(in)
	for {
		line, err := e.NextLine(true)
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				if e.Buffer.Dirty && !e.warned {
					e.warned = true
					e.commandError(errWarned.Error())
					continue
				}
				break
			}
			e.commandError(err.Error())
			break
		}
		quit, cmdErr := e.execute(line)
		if cmdErr != nil {
			e.commandError(cmdErr.Error())
		}
		if quit {
			break
		}
	}
	return e.exitStatus()
}

func (e *Engine) Execute(line string) bool {
	quit, cmdErr := e.execute(line)
	if cmdErr != nil {
		e.commandError(cmdErr.Error())
	}
	return quit
}

func (e *Engine) Fail(detail string) { e.commandError(detail) }

func (e *Engine) HadError() bool { return e.hadError }

func (e *Engine) exitStatus() int {
	if e.hadError {
		return 1
	}
	return 0
}

func (e *Engine) commandError(detail string) {
	e.hadError = true
	e.lastDiagnostic = detail
	if e.ReportError != nil {
		e.ReportError(detail)
		return
	}
	_, _ = fmt.Fprintln(e.Diag, "?")
	if e.help && detail != "" {
		_, _ = fmt.Fprintln(e.Diag, detail)
	}
}

func (e *Engine) fileDiagnostic(err error) error {
	if e.Diag != nil {
		_, _ = fmt.Fprintln(e.Diag, err.Error())
	}
	return err
}

func (e *Engine) Load(name string) (int, error) {
	data, err := e.Files.Read(name)
	if err != nil {
		return 0, err
	}
	e.Buffer.Reset(splitText(data), false)
	e.marks = make(map[byte]int)
	e.undoValid = false
	e.Filename = name
	e.warned = false
	return len(data), nil
}

func cloneMarks(src map[byte]int) map[byte]int {
	dst := make(map[byte]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (e *Engine) saveUndo() {
	if e.inGlobal {
		return
	}
	e.undoBuffer, e.undoMarks, e.undoValid = e.Buffer.Clone(), cloneMarks(e.marks), true
}

func (e *Engine) appendLines(after int, lines []string) error {
	if after < 0 || after > e.Buffer.Last() {
		return fmt.Errorf("地址无效")
	}
	if len(lines) > 0 {
		e.saveUndo()
	}
	if err := e.Buffer.Append(after, lines); err != nil {
		return err
	}
	if len(lines) > 0 {
		e.changeSeq++
		e.adjustGlobalInsert(after, len(lines))
	}
	for k, n := range e.marks {
		if n > after {
			e.marks[k] = n + len(lines)
		}
	}
	return nil
}

func (e *Engine) deleteLines(first, last int) error {
	e.saveUndo()
	if err := e.Buffer.Delete(first, last); err != nil {
		return err
	}
	e.changeSeq++
	e.adjustGlobalDelete(first, last)
	delta := last - first + 1
	for k, n := range e.marks {
		if n >= first && n <= last {
			delete(e.marks, k)
		} else if n > last {
			e.marks[k] = n - delta
		}
	}
	return nil
}

func (e *Engine) adjustGlobalInsert(after, count int) {
	if count == 0 {
		return
	}
	for i, n := range e.globalTargets {
		if n > after {
			e.globalTargets[i] = n + count
		}
	}
}

func (e *Engine) adjustGlobalDelete(first, last int) {
	delta := last - first + 1
	for i, n := range e.globalTargets {
		switch {
		case n >= first && n <= last:
			e.globalTargets[i] = 0
		case n > last:
			e.globalTargets[i] = n - delta
		}
	}
}

func (e *Engine) execute(line string) (quit bool, execErr error) {
	originalCurrent, originalSeq := e.Buffer.Current, e.changeSeq
	defer func() {
		if execErr != nil && e.changeSeq == originalSeq {
			e.Buffer.Current = originalCurrent
		}
		if e.changeSeq != originalSeq || (execErr != nil && !errors.Is(execErr, errWarned)) {
			e.warned = false
		}
	}()
	addrs, pos, explicit, err := e.parseAddresses(line)
	if err != nil {
		return false, err
	}
	for pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
		pos++
	}
	cmd := byte('\n')
	if pos < len(line) {
		cmd = line[pos]
		pos++
	}
	arg := line[pos:]
	switch cmd {
	case '\n':

		if explicit {
			return false, e.printRange(addrs, 1, 'p')
		}
		return false, e.printRange([]int{e.Buffer.Current + 1}, 1, 'p')
	case 'a', 'i', 'c':
		style, err := printSuffix(arg)
		if err != nil {
			return false, err
		}
		text, err := e.readInput()
		if err != nil {
			return false, err
		}
		switch cmd {
		case 'a':
			a, addressErr := e.oneAddress(addrs, explicit, e.Buffer.Current, true)
			if addressErr != nil {
				return false, addressErr
			}
			err = e.appendLines(a, text)
		case 'i':
			a, addressErr := e.oneAddress(addrs, explicit, e.Buffer.Current, true)
			if addressErr != nil {
				return false, addressErr
			}
			if a == 0 {
				a = 1
			}
			err = e.appendLines(a-1, text)
			if err == nil && len(text) == 0 {

				if a > e.Buffer.Last() {
					a = e.Buffer.Last()
				}
				e.Buffer.Current = a
			}
		default:
			first, last, addressErr := e.twoAddresses(addrs, explicit, e.Buffer.Current, e.Buffer.Current, true)
			if addressErr != nil {
				return false, addressErr
			}
			if first == 0 {
				first = 1
			}
			if last == 0 {
				last = 1
			}
			err = e.changeLines(first, last, text)
		}
		if err == nil && style != 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case 'd':
		first, last, err := e.twoAddresses(addrs, explicit, e.Buffer.Current, e.Buffer.Current, false)
		if err != nil {
			return false, err
		}
		style, err := printSuffix(arg)
		if err != nil {
			return false, err
		}
		if err = e.deleteLines(first, last); err == nil && style != 0 && e.Buffer.Last() > 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case 'j':

		first, last, err := e.twoAddresses(addrs, explicit, e.Buffer.Current, e.Buffer.Current+1, false)
		if err != nil {
			return false, err
		}
		style, err := printSuffix(arg)
		if err != nil {
			return false, err
		}
		err = e.join(first, last)
		if err == nil && style != 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case 'k':
		a, err := e.oneAddress(addrs, explicit, e.Buffer.Current, false)
		if err != nil {
			return false, err
		}
		arg = strings.TrimRight(arg, " \t")
		if len(arg) < 1 || len(arg) > 2 || arg[0] < 'a' || arg[0] > 'z' {
			return false, fmt.Errorf("标记无效")
		}
		style, err := printSuffix(arg[1:])
		if err != nil {
			return false, err
		}
		if e.marks == nil {
			e.marks = make(map[byte]int)
		}
		e.marks[arg[0]] = a
		if style != 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case 'm', 't':
		first, last, err := e.twoAddresses(addrs, explicit, e.Buffer.Current, e.Buffer.Current, false)
		if err != nil {
			return false, err
		}
		dest, style, err := e.addressAndSuffix(arg)
		if err != nil {
			return false, err
		}
		if cmd == 'm' {
			err = e.move(first, last, dest)
		} else {
			err = e.copyLines(first, last, dest)
		}
		if err == nil && style != 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case 'p', 'n', 'l':
		if _, err := printSuffix(arg); err != nil {
			return false, err
		}

		return false, e.printRange(addrs, 2, cmd)
	case 'z':

		a, err := e.oneAddress(addrs, explicit, e.Buffer.Current, false)
		if err != nil {
			return false, err
		}
		count := zWindow
		if operand := trimBlank(arg); operand != "" {
			count, err = strconv.Atoi(operand)
			if err != nil || count < 0 {
				return false, fmt.Errorf("z 的行数无效")
			}
		}
		first := a + 1
		last := first + count - 1
		if last > e.Buffer.Last() {
			last = e.Buffer.Last()
		}
		if first <= e.Buffer.Last() {
			if err := e.printRange([]int{first, last}, 2, 0); err != nil {
				return false, err
			}
		}
		return false, nil
	case '=':
		style, err := printSuffix(arg)
		if err != nil {
			return false, err
		}
		a, err := e.oneAddress(addrs, explicit, e.Buffer.Last(), true)
		if err != nil {
			return false, err
		}
		_, _ = fmt.Fprintln(e.Out, a)
		if style != 0 && e.Buffer.Current > 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case 's':
		first, last, err := e.twoAddresses(addrs, explicit, e.Buffer.Current, e.Buffer.Current, false)
		if err != nil {
			return false, err
		}
		arg, err = e.readSubstituteArgument(arg)
		if err != nil {
			return false, err
		}
		return false, e.substitute(first, last, arg)
	case 'w', 'W':

		if cmd == 'w' && len(arg) > 0 && arg[0] == 'q' &&
			(len(arg) == 1 || arg[1] == ' ' || arg[1] == '\t') {
			if err := e.write(addrs, explicit, arg[1:], false); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, e.write(addrs, explicit, arg, cmd == 'W')
	case 'r':
		a, err := e.oneAddress(addrs, explicit, e.Buffer.Last(), true)
		if err != nil {
			return false, err
		}
		if arg != "" && arg[0] != ' ' && arg[0] != '\t' {
			return false, fmt.Errorf("文件参数需要空白分隔")
		}
		return false, e.readFile(a, arg)
	case 'u':
		if explicit {
			return false, fmt.Errorf("意外的地址")
		}
		style, err := printSuffix(arg)
		if err != nil {
			return false, err
		}
		if !e.undoValid {
			return false, fmt.Errorf("没有可撤销的修改")
		}
		curB, curM := e.Buffer.Clone(), cloneMarks(e.marks)
		restored := e.undoBuffer.Clone()
		if !sameLines(restored.Lines, e.Buffer.Lines) {

			restored.Dirty = true
		}
		e.Buffer, e.marks = restored, cloneMarks(e.undoMarks)
		e.undoBuffer, e.undoMarks = curB, curM
		e.changeSeq++
		if e.inGlobal {
			for i := range e.globalTargets {
				e.globalTargets[i] = 0
			}
		}
		if style != 0 && e.Buffer.Current > 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case 'g', 'v':
		e.globalQuit = false
		err := e.global(addrs, explicit, arg, cmd == 'g')
		return e.globalQuit, err
	case 'P':
		if explicit {
			return false, fmt.Errorf("意外的地址或参数")
		}
		style, err := printSuffix(arg)
		if err != nil {
			return false, err
		}
		if e.promptSetting == "" {
			if e.Prompt != "" {
				e.promptSetting = e.Prompt
			} else {
				e.promptSetting = "*"
			}
		}
		if e.Prompt == "" {
			e.Prompt = e.promptSetting
		} else {
			e.Prompt = ""
		}
		if style != 0 && e.Buffer.Current > 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case '!':
		if explicit {
			return false, fmt.Errorf("意外的地址")
		}
		return false, e.shellEscape(arg)
	case 'e', 'E':
		if explicit {
			return false, fmt.Errorf("意外的地址")
		}
		if arg != "" && arg[0] != ' ' && arg[0] != '\t' {
			return false, fmt.Errorf("文件参数需要空白分隔")
		}
		if cmd == 'e' && e.Buffer.Dirty && !e.warned {
			e.warned = true
			return false, errWarned
		}
		name := trimBlank(arg)
		if name == "" {
			name = e.Filename
		}
		if name == "" {
			return false, fmt.Errorf("没有当前文件名")
		}
		if strings.HasPrefix(name, "!") {
			return false, fmt.Errorf("不支持 shell 形式的文件命令（客体只支持独立的 ! 命令）")
		}
		n, err := e.Load(name)
		if err != nil {

			e.Buffer.Reset(nil, false)
			e.marks = make(map[byte]int)
			e.undoValid = false
			e.Filename = name
			e.changeSeq++
			return false, e.fileDiagnostic(err)
		}
		e.changeSeq++
		if e.inGlobal {
			for i := range e.globalTargets {
				e.globalTargets[i] = 0
			}
		}
		if !e.Silent {
			_, _ = fmt.Fprintln(e.Out, n)
		}
		return false, nil
	case 'q', 'Q':
		if explicit || arg != "" {
			return false, fmt.Errorf("意外的地址或参数")
		}
		if cmd == 'q' && e.Buffer.Dirty && !e.warned {
			e.warned = true
			return false, errWarned
		}
		return true, nil
	case 'f':
		if explicit {
			return false, fmt.Errorf("意外的地址")
		}
		if arg != "" && arg[0] != ' ' && arg[0] != '\t' {
			return false, fmt.Errorf("文件参数需要空白分隔")
		}
		name := trimBlank(arg)
		if strings.HasPrefix(name, "!") {
			return false, fmt.Errorf("文件名无效")
		}
		if name != "" {
			e.Filename = name
		}
		if e.Filename == "" {
			return false, fmt.Errorf("没有当前文件名")
		}
		_, _ = fmt.Fprintln(e.Out, e.Filename)
		return false, nil
	case 'h':
		if explicit {
			return false, fmt.Errorf("意外的地址或参数")
		}
		style, err := printSuffix(arg)
		if err != nil {
			return false, err
		}

		if e.lastDiagnostic != "" {
			if _, printErr := fmt.Fprintln(e.Diag, e.lastDiagnostic); printErr != nil {
				return false, printErr
			}
		}
		if style != 0 && e.Buffer.Current > 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	case 'H':
		if explicit {
			return false, fmt.Errorf("意外的地址或参数")
		}
		style, err := printSuffix(arg)
		if err != nil {
			return false, err
		}
		e.help = !e.help
		if e.help && e.lastDiagnostic != "" {
			if _, printErr := fmt.Fprintln(e.Diag, e.lastDiagnostic); printErr != nil {
				return false, printErr
			}
		}
		if style != 0 && e.Buffer.Current > 0 {
			err = e.printRange([]int{e.Buffer.Current}, 1, style)
		}
		return false, err
	default:
		return false, fmt.Errorf("未知命令")
	}
}

func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func printSuffix(arg string) (byte, error) {
	if arg == "" {
		return 0, nil
	}
	if len(arg) == 1 && strings.ContainsRune("pln", rune(arg[0])) {
		return arg[0], nil
	}
	return 0, fmt.Errorf("不支持的命令后缀")
}

func (e *Engine) readCommandLine() (string, error) {
	if e.list != nil {
		return e.list.ReadString('\n')
	}
	return e.reader.ReadString('\n')
}

func (e *Engine) readInput() ([]string, error) {
	var lines []string
	for {
		line, err := e.readCommandLine()
		if err != nil && len(line) == 0 {
			if errors.Is(err, io.EOF) {
				return lines, nil
			}
			return nil, err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "." {
			return lines, nil
		}
		lines = append(lines, line)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return lines, nil
			}
			return nil, err
		}
	}
}

func (e *Engine) readSubstituteArgument(arg string) (string, error) {
	for hasOddTrailingBackslashes(arg) {
		next, err := e.readCommandLine()
		if err != nil && len(next) == 0 {
			return "", fmt.Errorf("替换文本未闭合")
		}
		next = strings.TrimSuffix(next, "\n")
		arg = arg[:len(arg)-1] + "\n" + next
		if err != nil {
			return "", fmt.Errorf("替换文本未闭合")
		}
	}
	return arg, nil
}

func hasOddTrailingBackslashes(s string) bool {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func stripContinuation(s string) string {
	if hasOddTrailingBackslashes(s) {
		return s[:len(s)-1]
	}
	return s
}
