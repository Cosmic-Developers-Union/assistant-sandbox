package interp

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/internal/resolver"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/lexer"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/regex"
)

func (p *interp) printLine(writer io.Writer, line string) error {
	err := writeOutput(writer, line, p.newlineOutputCRLF)
	if err != nil {
		return err
	}
	return writeOutput(writer, p.outputRecordSep, p.newlineOutputCRLF)
}

func (p *interp) printArgs(writer io.Writer, args []value) error {
	switch p.outputMode {
	case CSVMode, TSVMode:
		fields := make([]string, 0, 7)
		for _, arg := range args {
			fields = append(fields, p.formatValue(arg, p.outputFormat))
		}
		err := p.writeCSV(writer, fields)
		if err != nil {
			return err
		}
	default:

		for i, arg := range args {
			if i > 0 {
				err := writeOutput(writer, p.outputFieldSep, p.newlineOutputCRLF)
				if err != nil {
					return err
				}
			}
			err := writeOutput(writer, p.formatValue(arg, p.outputFormat), p.newlineOutputCRLF)
			if err != nil {
				return err
			}
		}
		err := writeOutput(writer, p.outputRecordSep, p.newlineOutputCRLF)
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *interp) writeCSV(output io.Writer, fields []string) error {

	var flush func() error
	_, isBuffered := output.(*bufio.Writer)
	if !isBuffered {

		if p.csvOutput == nil {
			p.csvOutput = bufio.NewWriterSize(output, 4096)
		} else {
			p.csvOutput.Reset(output)
		}
		output = p.csvOutput
		flush = p.csvOutput.Flush
	}

	writer := csv.NewWriter(output)
	writer.Comma = p.csvOutputConfig.Separator
	writer.UseCRLF = p.newlineOutputCRLF
	err := writer.Write(fields)
	if err != nil {
		return err
	}
	if flush != nil {
		return flush()
	}
	return nil
}

func (p *interp) getOutputStream(redirect lexer.Token, destValue value) (io.Writer, error) {
	name := p.toString(destValue)
	if _, ok := p.inputStreams[name]; ok {
		return nil, newError("can't write to reader stream")
	}
	if w, ok := p.outputStreams[name]; ok {
		return w, nil
	}

	switch redirect {
	case lexer.GREATER, lexer.APPEND:

		if name == "-" {

			return p.output, nil
		}
		if p.noFileWrites {
			return nil, newError("can't write to file due to NoFileWrites")
		}

		p.flushOutputAndError()

		switch name {
		case "/dev/stderr":
			return p.errorOutput, nil
		case "/dev/stdout":
			return p.output, nil
		}

		flags := os.O_CREATE | os.O_WRONLY
		if redirect == lexer.GREATER {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_APPEND
		}
		f, err := p.openFile(name, flags, 0644)
		if err != nil {
			return nil, newError("output redirection error: %s", err)
		}
		out := newOutFileStream(f, outputBufSize)
		p.outputStreams[name] = out
		return out, nil

	case lexer.PIPE:

		if p.noExec {
			return nil, newError("can't write to pipe due to NoExec")
		}
		cmd := p.execShell(name)
		cmd.Stdout = p.output
		cmd.Stderr = p.errorOutput
		p.flushOutputAndError()
		out, err := newOutCmdStream(cmd)
		if err != nil {
			p.printErrorf("%s\n", err)
			out = newOutNullStream()
		}
		p.outputStreams[name] = out
		return out, nil

	default:

		panic(fmt.Sprintf("unexpected redirect type %s", redirect))
	}
}

func (p *interp) execShell(code string) *exec.Cmd {
	executable := p.shellCommand[0]
	args := p.shellCommand[1:]
	args = append(args, code)

	var cmd *exec.Cmd
	if p.checkCtx {
		cmd = exec.CommandContext(p.ctx, executable, args...)
	} else {
		cmd = exec.Command(executable, args...)
	}
	cmd.Dir = p.commandDir
	cmd.Env = p.commandEnv

	return cmd
}

func (p *interp) getInputScannerFile(name string) (*bufio.Scanner, error) {
	if _, ok := p.outputStreams[name]; ok {
		return nil, newError("can't read from writer stream")
	}
	if _, ok := p.inputStreams[name]; ok {
		return p.scanners[name], nil
	}
	if name == "-" {

		if scanner, ok := p.scanners["-"]; ok {
			return scanner, nil
		}
		scanner := p.newScanner(p.stdin, make([]byte, inputBufSize))
		p.scanners[name] = scanner
		return scanner, nil
	}
	if p.noFileReads {
		return nil, newError("can't read from file due to NoFileReads")
	}
	f, err := p.openFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	in := newInFileStream(f)
	scanner := p.newScanner(in, make([]byte, inputBufSize))
	p.scanners[name] = scanner
	p.inputStreams[name] = in
	return scanner, nil
}

func (p *interp) getInputScannerPipe(name string) (*bufio.Scanner, error) {
	if _, ok := p.outputStreams[name]; ok {
		return nil, newError("can't read from writer stream")
	}
	if _, ok := p.inputStreams[name]; ok {
		return p.scanners[name], nil
	}
	if p.noExec {
		return nil, newError("can't read from pipe due to NoExec")
	}
	cmd := p.execShell(name)
	cmd.Stdin = p.stdin
	cmd.Stderr = p.errorOutput
	p.flushOutputAndError()
	in, err := newInCmdStream(cmd)
	if err != nil {
		p.printErrorf("%s\n", err)
		return bufio.NewScanner(strings.NewReader("")), nil
	}

	scanner := p.newScanner(in, make([]byte, inputBufSize))
	p.inputStreams[name] = in
	p.scanners[name] = scanner
	return scanner, nil
}

func (p *interp) newScanner(input io.Reader, buffer []byte) *bufio.Scanner {
	scanner := bufio.NewScanner(input)
	switch p.inputMode {
	case CSVMode, TSVMode:
		splitter := csvSplitter{
			separator:     p.csvInputConfig.Separator,
			sepLen:        utf8.RuneLen(p.csvInputConfig.Separator),
			comment:       p.csvInputConfig.Comment,
			header:        p.csvInputConfig.Header,
			fields:        &p.fields,
			setFieldNames: p.setFieldNames,
		}
		scanner.Split(splitter.scan)
	default:

		splitter := recordSplitter{
			sep:        &p.recordSep,
			re:         &p.recordSepRegex,
			terminator: &p.recordTerminator,
		}
		scanner.Split(splitter.scan)
	}
	scanner.Buffer(buffer, maxRecordLength)
	return scanner
}

type recordSplitter struct {
	sep        *string
	re         *regex.Regexp
	terminator *string
}

func (s recordSplitter) scan(data []byte, atEOF bool) (advance int, token []byte, err error) {
	sep := *s.sep
	switch {
	case sep == "\n":
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			*s.terminator = "\n"
			return i + 1, dropCR(data[:i]), nil
		}
		if atEOF {
			*s.terminator = ""
			return len(data), dropCR(data), nil
		}
		return 0, nil, nil
	case sep == "":
		return (blankLineSplitter{terminator: s.terminator}).scan(data, atEOF)
	case len(sep) == 1:
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if i := bytes.IndexByte(data, sep[0]); i >= 0 {
			*s.terminator = sep
			return i + 1, data[:i], nil
		}
		if atEOF {
			*s.terminator = ""
			return len(data), data, nil
		}
		return 0, nil, nil
	default:
		return (regexSplitter{re: s.re, terminator: s.terminator}).scan(data, atEOF)
	}
}

func (p *interp) setFieldNames(names []string) {
	p.fieldNames = names
	p.fieldIndexes = nil

	fieldsArray := p.array(resolver.Global, p.arrayIndexes["FIELDS"])
	for k := range fieldsArray {
		delete(fieldsArray, k)
	}
	for i, name := range names {
		fieldsArray[strconv.Itoa(i+1)] = localeStr(name, p.decimalPoint)
	}
}

func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[:len(data)-1]
	}
	return data
}

func dropLF(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\n' {
		return data[:len(data)-1]
	}
	return data
}

type blankLineSplitter struct {
	terminator *string
}

func (s blankLineSplitter) scan(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	i := 0
	for i < len(data) && (data[i] == '\n' || data[i] == '\r') {
		i++
	}
	if i >= len(data) {

		return i, nil, nil
	}
	start := i

	for ; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		end := i
		if i+1 < len(data) && data[i+1] == '\n' {
			i += 2
			for i < len(data) && (data[i] == '\n' || data[i] == '\r') {
				i++
			}
			*s.terminator = string(data[end:i])
			return i, dropCR(data[start:end]), nil
		}
		if i+2 < len(data) && data[i+1] == '\r' && data[i+2] == '\n' {
			i += 3
			for i < len(data) && (data[i] == '\n' || data[i] == '\r') {
				i++
			}
			*s.terminator = string(data[end:i])
			return i, dropCR(data[start:end]), nil
		}
	}

	if atEOF {
		token = dropCR(dropLF(data[start:]))
		*s.terminator = string(data[len(token):])
		return len(data), token, nil
	}

	return 0, nil, nil
}

type regexSplitter struct {
	re         *regex.Regexp
	terminator *string
}

func (s regexSplitter) scan(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	loc, err := (*s.re).FindIndex(data)
	if err != nil {
		return 0, nil, err
	}

	if loc != nil && loc[0] != loc[1] {
		*s.terminator = string(data[loc[0]:loc[1]])
		return loc[1], data[:loc[0]], nil
	}

	if atEOF {
		*s.terminator = ""
		return len(data), data, nil
	}

	return 0, nil, nil
}

type csvSplitter struct {
	separator rune
	sepLen    int
	comment   rune
	header    bool

	recordBuffer []byte
	fieldIndexes []int
	noBOMCheck   bool

	fields        *[]string
	setFieldNames func(names []string)
	rowNum        int
}

func (s *csvSplitter) scan(data []byte, atEOF bool) (advance int, token []byte, err error) {

	if !s.noBOMCheck && len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
		advance = 3
		s.noBOMCheck = true
	}

	origData := data
	if atEOF && len(data) == 0 {

		return 0, nil, nil
	}

	readLine := func() []byte {
		newline := bytes.IndexByte(data, '\n')
		var line []byte
		switch {
		case newline >= 0:

			line = data[:newline+1]
			data = data[newline+1:]
		case atEOF:

			line = data
			data = data[len(data):]
		default:

			return nil
		}

		if len(line) > 0 && atEOF && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
			advance++
		}

		return line
	}

	skip := 0
	var line []byte
	for {
		line = readLine()
		if len(line) == 0 {
			return 0, nil, nil
		}
		if s.comment != 0 && nextRune(line) == s.comment {
			advance += len(line)
			skip += len(line)
			continue
		}
		if len(line) == lenNewline(line) {
			advance += len(line)
			skip += len(line)
			continue
		}
		break
	}

	const quoteLen = len(`"`)
	tokenHasCR := false
	s.recordBuffer = s.recordBuffer[:0]
	s.fieldIndexes = s.fieldIndexes[:0]
parseField:
	for {
		if len(line) == 0 || line[0] != '"' {

			i := bytes.IndexRune(line, s.separator)
			field := line
			if i >= 0 {
				advance += i + s.sepLen
				field = field[:i]
			} else {
				advance += len(field)
				field = field[:len(field)-lenNewline(field)]
			}
			s.recordBuffer = append(s.recordBuffer, field...)
			s.fieldIndexes = append(s.fieldIndexes, len(s.recordBuffer))
			if i >= 0 {
				line = line[i+s.sepLen:]
				continue parseField
			}
			break parseField
		} else {

			line = line[quoteLen:]
			advance += quoteLen
			for {
				i := bytes.IndexByte(line, '"')
				if i >= 0 {

					s.recordBuffer = append(s.recordBuffer, line[:i]...)
					line = line[i+quoteLen:]
					advance += i + quoteLen
					switch rn := nextRune(line); {
					case rn == '"':

						s.recordBuffer = append(s.recordBuffer, '"')
						line = line[quoteLen:]
						advance += quoteLen
					case rn == s.separator:

						line = line[s.sepLen:]
						s.fieldIndexes = append(s.fieldIndexes, len(s.recordBuffer))
						advance += s.sepLen
						continue parseField
					case lenNewline(line) == len(line):

						s.fieldIndexes = append(s.fieldIndexes, len(s.recordBuffer))
						advance += len(line)
						break parseField
					default:

						s.recordBuffer = append(s.recordBuffer, '"')
					}
				} else if len(line) > 0 {

					advance += len(line)
					newlineLen := lenNewline(line)
					if newlineLen == 2 {
						tokenHasCR = true
						s.recordBuffer = append(s.recordBuffer, line[:len(line)-2]...)
						s.recordBuffer = append(s.recordBuffer, '\n')
					} else {
						s.recordBuffer = append(s.recordBuffer, line...)
					}
					line = readLine()
					if line == nil {
						return 0, nil, nil
					}
				} else {

					s.fieldIndexes = append(s.fieldIndexes, len(s.recordBuffer))
					advance += len(line)
					break parseField
				}
			}
		}
	}

	strBuf := string(s.recordBuffer)
	fields := make([]string, len(s.fieldIndexes))
	preIdx := 0
	for i, idx := range s.fieldIndexes {
		fields[i] = strBuf[preIdx:idx]
		preIdx = idx
	}

	s.noBOMCheck = true

	if s.rowNum == 0 && s.header {

		s.rowNum++
		s.setFieldNames(fields)
		return advance, nil, nil
	}

	s.rowNum++
	*s.fields = fields
	token = origData[skip:advance]
	token = token[:len(token)-lenNewline(token)]
	if tokenHasCR {
		token = bytes.ReplaceAll(token, []byte{'\r'}, nil)
	}
	return advance, token, nil
}

func lenNewline(b []byte) int {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		if len(b) > 1 && b[len(b)-2] == '\r' {
			return 2
		}
		return 1
	}
	return 0
}

func nextRune(b []byte) rune {
	r, _ := utf8.DecodeRune(b)
	return r
}

func (p *interp) setFile(filename string) {
	p.filename = localeNumStr(filename, p.decimalPoint)
	p.fileLineNum = num(0)
	p.hadFiles = true
}

func (p *interp) setLine(line string, isTrueStr bool) {
	p.line = line
	p.lineIsTrueStr = isTrueStr
	p.haveFields = false
	p.reparseCSV = true

	p.savedFieldSep = p.fieldSep
	p.savedFieldSepRegex = p.fieldSepRegex
}

func (p *interp) splitOnFieldSepRegex(fields []string, line string) ([]string, error) {
	indices, err := p.savedFieldSepRegex.FindAllStringIndex(line, -1)
	if err != nil {
		return nil, err
	}
	prevIndex := 0
	for _, match := range indices {
		start, end := match[0], match[1]

		if start == end {
			continue
		}
		fields = append(fields, line[prevIndex:start])
		prevIndex = end
	}
	fields = append(fields, line[prevIndex:])
	return fields, nil
}

func (p *interp) ensureFields() error {
	if p.haveFields {
		return nil
	}
	p.haveFields = true

	switch {
	case p.inputMode == CSVMode || p.inputMode == TSVMode:

		if p.reparseCSV {
			scanner := bufio.NewScanner(strings.NewReader(p.line))
			scanner.Buffer(nil, maxRecordLength)
			splitter := csvSplitter{
				separator: p.csvInputConfig.Separator,
				sepLen:    utf8.RuneLen(p.csvInputConfig.Separator),
				comment:   p.csvInputConfig.Comment,
				fields:    &p.fields,
			}
			scanner.Split(splitter.scan)
			if !scanner.Scan() {
				p.fields = nil
			}
		}
	case p.savedFieldSep == " ":

		p.fields = strings.Fields(p.line)
	case p.line == "":
		p.fields = nil
	case utf8.RuneCountInString(p.savedFieldSep) <= 1:

		p.fields = strings.Split(p.line, p.savedFieldSep)
	default:

		var err error
		p.fields, err = p.splitOnFieldSepRegex(p.fields[:0], p.line)
		if err != nil {
			p.haveFields = false
			return err
		}
	}

	if p.inputMode == DefaultMode && p.recordSep == "" && utf8.RuneCountInString(p.savedFieldSep) == 1 {
		fields := make([]string, 0, len(p.fields))
		for _, field := range p.fields {
			lines := strings.Split(field, "\n")
			for _, line := range lines {
				trimmed := strings.TrimSuffix(line, "\r")
				fields = append(fields, trimmed)
			}
		}
		p.fields = fields
	}

	p.fieldsIsTrueStr = p.fieldsIsTrueStr[:0]
	for range p.fields {
		p.fieldsIsTrueStr = append(p.fieldsIsTrueStr, false)
	}
	p.numFields = num(float64(len(p.fields)))
	return nil
}

func (p *interp) nextLine() (string, error) {
	for {
		if p.scanner == nil {
			if prevInput, ok := p.input.(io.Closer); ok && p.input != p.stdin {

				_ = prevInput.Close()
			}
			if p.filenameIndex >= int(p.argc.num()) && !p.hadFiles {

				p.input = p.stdin
				p.setFile("-")
			} else {
				if p.filenameIndex >= int(p.argc.num()) {

					return "", io.EOF
				}

				index := strconv.Itoa(p.filenameIndex)
				argvIndex := p.arrayIndexes["ARGV"]
				argvArray := p.array(resolver.Global, argvIndex)
				filename := p.toString(argvArray[index])
				p.filenameIndex++

				var matches []string
				if !p.noArgVars {
					matches = varRegex.FindStringSubmatch(filename)
				}
				if len(matches) >= 3 {

					name, val := matches[1], matches[2]

					unescaped, err := lexer.Unescape(val)
					if err == nil {
						val = unescaped
					}
					err = p.setVarByName(name, val)
					if err != nil {
						return "", err
					}
					continue
				} else if filename == "" {

					p.input = nil
					continue
				} else if filename == "-" {

					p.input = p.stdin
					p.setFile("-")
				} else {

					if p.noFileReads {
						return "", newError("can't read from file due to NoFileReads")
					}
					var input io.Reader
					var err error
					if p.openInput != nil {

						var stream io.ReadCloser
						stream, err = p.openInput(filename)
						input = stream
					} else {
						input, err = p.openFile(filename, os.O_RDONLY, 0)
					}
					if err != nil {
						return "", err
					}
					p.input = input
					p.setFile(filename)
				}
			}
			if p.inputBuffer == nil {
				p.inputBuffer = make([]byte, inputBufSize)
			}
			p.scanner = p.newScanner(p.input, p.inputBuffer)
		}
		p.recordTerminator = p.recordSep
		if p.scanner.Scan() {

			break
		}
		err := p.scanner.Err()
		if err != nil {
			return "", fmt.Errorf("error reading from input: %w", err)
		}

		p.scanner = nil
	}

	p.lineNum = num(p.lineNum.num() + 1)
	p.fileLineNum = num(p.fileLineNum.num() + 1)
	return p.scanner.Text(), nil
}

func writeOutput(w io.Writer, s string, crlfNewline bool) error {
	if crlfNewline {

		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	_, err := io.WriteString(w, s)
	return err
}

func (p *interp) closeAll() {
	if prevInput, ok := p.input.(io.Closer); ok {
		_ = prevInput.Close()
	}
	for _, r := range p.inputStreams {
		_ = r.Close()
	}
	for _, w := range p.outputStreams {
		_ = w.Close()
	}
	if f, ok := p.output.(flusher); ok {
		_ = f.Flush()
	}
	if f, ok := p.errorOutput.(flusher); ok {
		_ = f.Flush()
	}
}

func (p *interp) flushAll() bool {
	allGood := true
	for name, writer := range p.outputStreams {
		if !p.flushWriter(name, writer) {
			allGood = false
		}
	}
	if !p.flushWriter("stdout", p.output) {
		allGood = false
	}
	return allGood
}

func (p *interp) flushStream(name string) bool {
	writer := p.outputStreams[name]
	if writer == nil {
		p.printErrorf("error flushing %q: not an output file or pipe\n", name)
		return false
	}
	return p.flushWriter(name, writer)
}

type flusher interface {
	Flush() error
}

func (p *interp) flushWriter(name string, writer io.Writer) bool {
	flusher, ok := writer.(flusher)
	if !ok {
		return true
	}
	err := flusher.Flush()
	if err != nil {
		p.printErrorf("error flushing %q: %v\n", name, err)
		return false
	}
	return true
}

func (p *interp) flushOutputAndError() {
	if flusher, ok := p.output.(flusher); ok {
		_ = flusher.Flush()
	}
	if flusher, ok := p.errorOutput.(flusher); ok {
		_ = flusher.Flush()
	}
}

func (p *interp) printErrorf(format string, args ...any) {
	if flusher, ok := p.output.(flusher); ok {
		_ = flusher.Flush()
	}
	_, _ = fmt.Fprintf(p.errorOutput, format, args...)
	if flusher, ok := p.errorOutput.(flusher); ok {
		_ = flusher.Flush()
	}
}
