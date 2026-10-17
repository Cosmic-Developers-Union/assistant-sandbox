package sed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

type command struct {
	address *address

	end     *address
	negate  bool
	kind    byte
	matcher *regexp.Regexp
	replace string
	global  bool
	print   bool

	text string

	label string
	jump  int

	width int

	file string

	trans map[byte]byte
}

type machine struct {
	ctx      context.Context
	h        interp.HandlerContext
	commands []command
	quiet    bool

	pattern string
	hold    string

	lines  []string
	cursor int
	line   int
	total  int
	ranges map[int]bool

	lastInRange bool

	substituted bool

	writers map[string]vmfs.File

	outputs []pending
}

type pending struct {
	text string
	raw  bool
}

func (m *machine) run() error {
	for m.cursor < len(m.lines) {
		m.readLine()
		quit, err := m.cycle()
		if err != nil {
			return err
		}
		if quit {
			return nil
		}
	}
	return nil
}

func (m *machine) readLine() {
	m.pattern = m.lines[m.cursor]
	m.cursor++
	m.line++
	m.substituted = false
}

func (m *machine) appendLine() bool {
	if m.cursor >= len(m.lines) {
		return false
	}
	m.pattern += "\n" + m.lines[m.cursor]
	m.cursor++
	m.line++
	return true
}

func (m *machine) flushOutputs() error {
	for _, item := range m.outputs {
		if item.raw {
			if _, err := io.WriteString(m.h.Stdout, item.text); err != nil {
				return err
			}
			continue
		}
		if err := emit(m.h, item.text); err != nil {
			return err
		}
	}
	m.outputs = m.outputs[:0]
	return nil
}

func openWriters(ctx context.Context, commands []command) (map[string]vmfs.File, error) {
	writers := map[string]vmfs.File{}
	h := commandio.Context(ctx)
	for _, item := range commands {
		if item.kind != 'w' {
			continue
		}
		if _, opened := writers[item.file]; opened {
			continue
		}
		file, err := commandio.Open(ctx, commandio.Path(h.Dir, item.file), vmfs.Create|vmfs.WriteOnly|vmfs.Truncate, 0o666)
		if err != nil {
			err = errors.Join(err, closeWriters(writers))
			return nil, fmt.Errorf("sed: 无法打开文件 %s: %w", item.file, err)
		}
		writers[item.file] = file
	}
	return writers, nil
}

func closeWriters(writers map[string]vmfs.File) error {
	var firstErr error
	for _, file := range writers {
		if err := file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type address struct {
	line    int
	last    bool
	matcher *regexp.Regexp
}

func (a *address) plainLine() bool { return a != nil && !a.last && a.matcher == nil }

func (a *address) matches(text string, number, total int) bool {
	switch {
	case a == nil:
		return true
	case a.last:
		return number == total
	case a.matcher != nil:
		return a.matcher.MatchString(text)
	default:
		return number == a.line
	}
}

func Run(ctx context.Context, args []string) (closeResult0 error) {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "sed")
	quiet := flags.BoolP("quiet", "n", false, "不自动打印")
	scripts := flags.StringArrayP("expression", "e", nil, "脚本片段")
	scriptFiles := flags.StringArrayP("file", "f", nil, "从客体文件读取脚本")
	var extended bool
	flags.BoolVarP(&extended, "regexp-extended", "E", false, "扩展正则")
	flags.BoolVarP(&extended, "regexp-extended-r", "r", false, "扩展正则（-r 同义）")
	var inPlace string
	flags.StringVarP(&inPlace, "in-place", "i", "", "就地编辑（可选后缀）")

	flags.Lookup("in-place").NoOptDefVal = inPlaceNoSuffix

	args = normalizeInPlace(args)
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	inPlaceSet := flags.Changed("in-place")
	if inPlace == inPlaceNoSuffix {
		inPlace = ""
	}
	operands := flags.Args()

	for _, name := range *scriptFiles {
		content, err := readGuestFile(ctx, commandio.Path(commandio.Directory(ctx), name))
		if err != nil {
			return err
		}
		*scripts = append(*scripts, content)
	}
	if len(*scripts) == 0 {
		if len(operands) == 0 {
			return fmt.Errorf("缺少脚本")
		}
		*scripts = []string{operands[0]}
		operands = operands[1:]
	}
	var commands []command
	for _, script := range *scripts {
		parsed, err := parse(script, extended)
		if err != nil {
			return err
		}
		commands = append(commands, parsed...)
	}
	if len(commands) == 0 {
		return fmt.Errorf("脚本为空")
	}
	writers, err := openWriters(ctx, commands)
	if err != nil {
		return err
	}
	defer func() {
		closeResult0 = errors.Join(closeResult0, closeWriters(writers))
	}()

	if inPlaceSet {

		if len(operands) == 0 {
			return fmt.Errorf("sed -i 需要文件操作数")
		}
		return runInPlace(ctx, operands, commands, *quiet, inPlace, writers)
	}

	var lines []string
	err = commandio.Inputs(ctx, operands, func(_, input string) error {
		lines = append(lines, commandio.Rows(input)...)
		return nil
	})
	if err != nil {
		return err
	}
	m := &machine{ctx: ctx, h: h, commands: commands, quiet: *quiet, ranges: map[int]bool{}, lines: lines, total: len(lines), writers: writers}
	if err := m.run(); err != nil {
		return err
	}
	return nil
}

const inPlaceNoSuffix = "\x00no-suffix"

func normalizeInPlace(args []string) []string {
	normalized := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "-i.") && !strings.HasPrefix(arg, "--") {
			normalized = append(normalized, "--in-place="+arg[2:])
			continue
		}
		normalized = append(normalized, arg)
	}
	return normalized
}

func runInPlace(ctx context.Context, operands []string, commands []command, quiet bool, suffix string, writers map[string]vmfs.File) error {
	h := commandio.Context(ctx)
	for _, name := range operands {
		if name == "-" {
			return fmt.Errorf("sed -i 不接受标准输入")
		}
		path := commandio.Path(h.Dir, name)
		content, err := readGuestFile(ctx, path)
		if err != nil {
			return err
		}
		rows := commandio.Rows(content)
		var out strings.Builder
		buffer := h
		buffer.Stdout = &out
		m := &machine{ctx: ctx, h: buffer, commands: commands, quiet: quiet, ranges: map[int]bool{}, lines: rows, total: len(rows), writers: writers}
		if err := m.run(); err != nil {
			return err
		}
		if suffix != "" {
			if err := writeGuestFile(ctx, path+suffix, content); err != nil {
				return err
			}
		}
		if err := writeGuestFile(ctx, path, out.String()); err != nil {
			return err
		}
	}
	return nil
}

func readGuestFile(ctx context.Context, path string) (closeResult0 string, closeResult1 error) {
	file, err := commandio.Open(ctx, path, vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		closeResult1 = errors.Join(closeResult1, file.Close())
	}()

	return commandio.ReadText(ctx, file)
}

func writeGuestFile(ctx context.Context, path, content string) error {
	file, err := commandio.Open(ctx, path, vmfs.Create|vmfs.WriteOnly|vmfs.Truncate, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(file, content); err != nil {
		err = errors.Join(err, file.Close())
		return err
	}
	return file.Close()
}

func (m *machine) selected(index int, item command) bool {
	m.lastInRange = true
	match := func(a *address) bool { return a.matches(m.pattern, m.line, m.total) }
	var hit bool
	switch {
	case item.end == nil:
		hit = item.address == nil || match(item.address)
	case m.ranges[index]:
		hit = true
		if match(item.end) {
			m.ranges[index] = false
		} else {
			m.lastInRange = false
		}
	default:
		hit = match(item.address)
		if hit {
			if item.end.plainLine() && item.end.line <= m.line {

			} else {
				m.ranges[index] = true
				m.lastInRange = false
			}
		}
	}
	if item.negate {
		hit = !hit
	}
	return hit
}

func (m *machine) cycle() (bool, error) {
	for pc := 0; pc < len(m.commands); {
		item := m.commands[pc]
		if !m.selected(pc, item) {

			if item.kind == '{' {
				pc = item.jump
				continue
			}
			pc++
			continue
		}
		switch item.kind {
		case '{':

		case '}', ':', '=':
			if item.kind == '=' {
				if _, err := fmt.Fprintln(m.h.Stdout, m.line); err != nil {
					return false, err
				}
			}
		case 'd':

			m.pattern = ""
			return false, m.flushOutputs()
		case 'D':
			if index := strings.IndexByte(m.pattern, '\n'); index >= 0 {

				m.pattern = m.pattern[index+1:]
				pc = 0
				continue
			}
			m.pattern = ""
			return false, m.flushOutputs()
		case 'P':
			if index := strings.IndexByte(m.pattern, '\n'); index >= 0 {
				if err := emit(m.h, m.pattern[:index]); err != nil {
					return false, err
				}
			} else if err := emit(m.h, m.pattern); err != nil {
				return false, err
			}
		case 'p':
			if err := emit(m.h, m.pattern); err != nil {
				return false, err
			}
		case 'l':
			if _, err := fmt.Fprintln(m.h.Stdout, listForm(m.pattern, item.width)); err != nil {
				return false, err
			}
		case 'n':
			if !m.quiet {
				if err := emit(m.h, m.pattern); err != nil {
					return false, err
				}
			}
			if err := m.flushOutputs(); err != nil {
				return false, err
			}
			if m.cursor >= len(m.lines) {
				return true, nil
			}

			m.readLine()
		case 'N':
			if !m.appendLine() {

				if !m.quiet {
					if err := emit(m.h, m.pattern); err != nil {
						return false, err
					}
				}
				return true, m.flushOutputs()
			}
		case 'q':
			if !m.quiet {
				if err := emit(m.h, m.pattern); err != nil {
					return false, err
				}
			}
			return true, m.flushOutputs()
		case 's':
			replaced := replace(item, m.pattern)
			if replaced != m.pattern {
				m.pattern = replaced
				m.substituted = true
				if item.print {
					if err := emit(m.h, m.pattern); err != nil {
						return false, err
					}
				}
			}
		case 'y':
			m.pattern = transliterate(item.trans, m.pattern)
		case 'h':
			m.hold = m.pattern
		case 'H':
			m.hold += "\n" + m.pattern
		case 'g':
			m.pattern = m.hold
		case 'G':
			m.pattern += "\n" + m.hold
		case 'x':
			m.pattern, m.hold = m.hold, m.pattern
		case 'w':
			writer, opened := m.writers[item.file]
			if !opened {
				return false, fmt.Errorf("sed: w 的目标文件未打开: %s", item.file)
			}
			if _, err := io.WriteString(writer, m.pattern+"\n"); err != nil {
				return false, err
			}
		case 'r':

			if content, err := readGuestFile(m.ctx, commandio.Path(commandio.Directory(m.ctx), item.file)); err == nil {
				m.outputs = append(m.outputs, pending{text: content, raw: true})
			}
		case 'a':
			m.outputs = append(m.outputs, pending{text: item.text})
		case 'i':
			if err := emit(m.h, item.text); err != nil {
				return false, err
			}
		case 'c':

			if item.end == nil || m.lastInRange {
				if err := emit(m.h, item.text); err != nil {
					return false, err
				}
			}
			m.pattern = ""
			return false, m.flushOutputs()
		case 'b':
			pc = item.jump
			continue
		case 't':
			if m.substituted {
				m.substituted = false
				pc = item.jump
				continue
			}
		}
		pc++
	}
	if !m.quiet {
		if err := emit(m.h, m.pattern); err != nil {
			return false, err
		}
	}
	if err := m.flushOutputs(); err != nil {
		return false, err
	}
	m.pattern = ""
	return false, nil
}

func listForm(text string, width int) string {
	var out strings.Builder
	column := 0
	write := func(part string) {
		out.WriteString(part)
		column += len(part)
	}
	for index := 0; index < len(text); index++ {
		if width > 0 && column >= width-1 {
			write("\\\n")
			column = 0
		}
		switch value := text[index]; value {
		case '\\':
			write("\\\\")
		case '\a':
			write("\\a")
		case '\b':
			write("\\b")
		case '\f':
			write("\\f")
		case '\n':
			write("\\n")
		case '\r':
			write("\\r")
		case '\t':
			write("\\t")
		case '\v':
			write("\\v")
		default:
			if value < 0x20 || value == 0x7f || value >= 0x80 {
				write(fmt.Sprintf("\\%03o", value))
				continue
			}
			out.WriteByte(value)
			column++
		}
	}
	write("$")
	return out.String()
}

func transliterate(table map[byte]byte, text string) string {
	if len(table) == 0 {
		return text
	}
	out := []byte(text)
	for index, value := range out {
		if mapped, ok := table[value]; ok {
			out[index] = mapped
		}
	}
	return string(out)
}

func emit(h interp.HandlerContext, text string) error {
	_, err := fmt.Fprintln(h.Stdout, text)
	return err
}

func replace(item command, text string) string {
	if item.global {
		return item.matcher.ReplaceAllString(text, item.replace)
	}
	location := item.matcher.FindStringIndex(text)
	if location == nil {
		return text
	}
	head, found, tail := text[:location[0]], text[location[0]:location[1]], text[location[1]:]
	return head + item.matcher.ReplaceAllString(found, item.replace) + tail
}

func parse(script string, extended bool) ([]command, error) {
	var commands []command
	labels := map[string]int{}

	var blocks []int
	for _, piece := range split(script) {
		piece = strings.TrimSpace(piece)
		if piece == "" || strings.HasPrefix(piece, "#") {
			continue
		}
		item, err := parseCommand(piece, extended)
		if err != nil {
			return nil, err
		}
		switch item.kind {
		case ':':
			if _, exists := labels[item.label]; exists {
				return nil, fmt.Errorf("sed 标签重复: %s", item.label)
			}
			labels[item.label] = len(commands)
		case '{':
			blocks = append(blocks, len(commands))
		case '}':
			if len(blocks) == 0 {
				return nil, fmt.Errorf("sed 块结束符没有对应的开始符")
			}
			open := blocks[len(blocks)-1]
			blocks = blocks[:len(blocks)-1]

			commands[open].jump = len(commands) + 1
		}
		commands = append(commands, item)
	}
	if len(blocks) != 0 {
		return nil, fmt.Errorf("sed 块缺少结束符")
	}

	for index := range commands {
		item := &commands[index]
		if item.kind != 'b' && item.kind != 't' {
			continue
		}
		if item.label == "" {
			item.jump = len(commands)
			continue
		}
		target, ok := labels[item.label]
		if !ok {
			return nil, fmt.Errorf("sed 分支跳到未知标签: %s", item.label)
		}
		item.jump = target
	}
	return commands, nil
}

func split(script string) []string {
	var pieces []string
	var builder strings.Builder
	delimiter := byte(0)
	sections := 0
	for index := 0; index < len(script); index++ {
		value := script[index]
		if delimiter != 0 {
			builder.WriteByte(value)
			if value == '\\' && index+1 < len(script) {
				index++
				builder.WriteByte(script[index])
				continue
			}
			if value == delimiter {
				sections++
				if sections == 2 {
					delimiter = 0
				}
			}
			continue
		}
		if (value == 's' || value == 'y') && atCommandPosition(builder.String()) {
			builder.WriteByte(value)
			if index+1 < len(script) {
				index++
				delimiter = script[index]
				builder.WriteByte(delimiter)
			}

			sections = 0
			continue
		}
		if (value == 'a' || value == 'i' || value == 'c' || value == 'r' || value == 'w') && atCommandPosition(builder.String()) {

			builder.WriteByte(value)
			index++
			backslash := index < len(script) && script[index] == '\\'
			if backslash {
				index++
				if index < len(script) && script[index] == '\n' {
					index++
				}
			} else {

				for index < len(script) && (script[index] == ' ' || script[index] == '\t') {
					index++
				}
			}
			for index < len(script) {
				if script[index] == '\\' && index+1 < len(script) && script[index+1] == '\n' {
					builder.WriteString("\n")
					index += 2
					continue
				}
				if script[index] == '\n' {
					break
				}
				builder.WriteByte(script[index])
				index++
			}
			pieces = append(pieces, builder.String())
			builder.Reset()
			index--
			continue
		}
		if value == ':' && atCommandPosition(builder.String()) {

			builder.WriteByte(value)
			index++
			for index < len(script) && script[index] != ';' && script[index] != '\n' && script[index] != ' ' && script[index] != '\t' {
				builder.WriteByte(script[index])
				index++
			}
			pieces = append(pieces, builder.String())
			builder.Reset()
			index--
			continue
		}
		if (value == '{' || value == '}') && insideRegexAddress(builder.String()) {

			builder.WriteByte(value)
			continue
		}
		if value == '{' {

			builder.WriteByte(value)
			pieces = append(pieces, builder.String())
			builder.Reset()
			continue
		}
		if value == '}' {
			if builder.Len() > 0 {
				pieces = append(pieces, builder.String())
				builder.Reset()
			}
			pieces = append(pieces, "}")
			continue
		}
		if value == '\n' || value == ';' {
			pieces = append(pieces, builder.String())
			builder.Reset()
			continue
		}
		builder.WriteByte(value)
	}
	pieces = append(pieces, builder.String())
	return pieces
}

func insideRegexAddress(piece string) bool {
	slashes := 0
	for index := 0; index < len(piece); index++ {
		if piece[index] == '\\' {
			index++
			continue
		}
		if piece[index] == '/' {
			slashes++
		}
	}
	return slashes%2 == 1
}

func atCommandPosition(piece string) bool {
	rest := strings.TrimSpace(piece)
	if _, remainder, ok := parseAddress(rest, false); ok {
		rest = strings.TrimSpace(remainder)
		if strings.HasPrefix(rest, ",") {
			if _, after, ok := parseAddress(strings.TrimSpace(rest[1:]), false); ok {
				rest = strings.TrimSpace(after)
			}
		}
	}
	if strings.HasPrefix(rest, "!") {
		rest = strings.TrimSpace(rest[1:])
	}
	return rest == ""
}

func parseCommand(piece string, extended bool) (command, error) {
	item := command{}
	rest := piece
	if address, remainder, ok := parseAddress(rest, extended); ok {
		item.address = address
		rest = strings.TrimSpace(remainder)

		if strings.HasPrefix(rest, ",") {
			end, after, ok := parseAddress(strings.TrimSpace(rest[1:]), extended)
			if !ok {
				return item, fmt.Errorf("sed 区间地址不完整: %s", piece)
			}
			item.end = end
			rest = strings.TrimSpace(after)
		}
		if strings.HasPrefix(rest, "!") {
			item.negate = true
			rest = strings.TrimSpace(rest[1:])
		}
	}
	if rest == "" {
		return item, fmt.Errorf("sed 缺少命令: %s", piece)
	}
	item.kind = rest[0]
	switch item.kind {
	case 'p', 'P', 'd', 'D', 'q', '=', 'h', 'H', 'g', 'G', 'x', 'n', 'N', 'l', '{', '}':
		if item.kind == '{' || item.kind == '}' {
			return item, nil
		}
		if item.kind == 'l' {

			item.width = 70
			if operand := strings.TrimSpace(rest[1:]); operand != "" {
				width, err := strconv.Atoi(operand)
				if err != nil || width < 0 {
					return item, fmt.Errorf("sed l 的宽度无效: %s", piece)
				}
				item.width = width
			}
			return item, nil
		}
		if len(rest) != 1 {
			return item, fmt.Errorf("sed 命令 %c 不接受参数", item.kind)
		}
		return item, nil
	case ':':
		item.label = strings.TrimSpace(rest[1:])
		if item.label == "" {
			return item, fmt.Errorf("sed 标签缺少名字: %s", piece)
		}
		if item.address != nil || item.negate {
			return item, fmt.Errorf("sed 标签不接受地址: %s", piece)
		}
		return item, nil
	case 'b', 't':

		item.label = strings.TrimSpace(rest[1:])
		return item, nil
	case 'a', 'i', 'c':
		item.text = rest[1:]
		return item, nil
	case 'r', 'w':
		item.file = strings.TrimSpace(rest[1:])
		if item.file == "" {
			return item, fmt.Errorf("sed 命令 %c 缺少文件名: %s", item.kind, piece)
		}
		return item, nil
	case 'y':
		if len(rest) < 4 {
			return item, fmt.Errorf("sed 转写命令不完整: %s", piece)
		}
		delimiter := rest[1]
		parts, flags, err := sections(rest[2:], delimiter)
		if err != nil {
			return item, err
		}
		if len(parts) != 2 {
			return item, fmt.Errorf("sed 转写命令需要两段: %s", piece)
		}
		if strings.TrimSpace(flags) != "" {
			return item, fmt.Errorf("sed 转写命令不接受标志: %s", piece)
		}
		from, err := unescape(parts[0])
		if err != nil {
			return item, err
		}
		to, err := unescape(parts[1])
		if err != nil {
			return item, err
		}
		if len(from) != len(to) {
			return item, fmt.Errorf("sed 转写命令两侧长度不同: %s", piece)
		}
		item.trans = make(map[byte]byte, len(from))
		for index := range from {
			item.trans[from[index]] = to[index]
		}
		return item, nil
	case 's':
		if len(rest) < 4 {
			return item, fmt.Errorf("sed 替换命令不完整: %s", piece)
		}
		delimiter := rest[1]
		parts, flags, err := sections(rest[2:], delimiter)
		if err != nil {
			return item, err
		}
		if len(parts) != 2 {
			return item, fmt.Errorf("sed 替换命令需要两段: %s", piece)
		}
		expression := parts[0]
		if expression == "" {
			return item, fmt.Errorf("sed 替换命令缺少模式")
		}
		if !extended {
			expression = locale.BREToERE(expression)
		}
		matcher, err := regexp.Compile(expression)
		if err != nil {
			return item, fmt.Errorf("sed 正则无效: %w", err)
		}
		item.matcher = matcher
		item.replace = expansion(parts[1])
		for _, flag := range flags {
			switch flag {
			case 'g':
				item.global = true
			case 'p':
				item.print = true
			case 'I', 'i':
				item.matcher, err = regexp.Compile("(?i)" + expression)
				if err != nil {
					return item, fmt.Errorf("sed 正则无效: %w", err)
				}
			default:
				return item, fmt.Errorf("sed 不支持的替换标志: %c", flag)
			}
		}
		return item, nil
	default:
		return item, fmt.Errorf("sed 不支持的命令: %c", item.kind)
	}
}

func parseAddress(piece string, extended bool) (*address, string, bool) {
	switch {
	case piece == "":
		return nil, piece, false
	case piece[0] == '$':
		return &address{last: true}, piece[1:], true
	case piece[0] >= '0' && piece[0] <= '9':
		index := 0
		for index < len(piece) && piece[index] >= '0' && piece[index] <= '9' {
			index++
		}
		value, err := strconv.Atoi(piece[:index])
		if err != nil || value < 1 {
			return nil, piece, false
		}
		return &address{line: value}, piece[index:], true
	case piece[0] == '/':
		if index := strings.IndexByte(piece[1:], '/'); index > 0 {
			expression := piece[1 : 1+index]
			if !extended {
				expression = locale.BREToERE(expression)
			}
			matcher, err := regexp.Compile(expression)
			if err != nil {
				return nil, piece, false
			}
			return &address{matcher: matcher}, piece[2+index:], true
		}
	}
	return nil, piece, false
}

func sections(text string, delimiter byte) ([]string, string, error) {
	var parts []string
	var builder strings.Builder
	for index := 0; index < len(text); index++ {
		value := text[index]
		if value == '\\' && index+1 < len(text) {
			builder.WriteByte(value)
			index++
			builder.WriteByte(text[index])
			continue
		}
		if value == delimiter {
			parts = append(parts, builder.String())
			builder.Reset()
			if len(parts) == 2 {
				return parts, text[index+1:], nil
			}
			continue
		}
		builder.WriteByte(value)
	}
	return nil, "", fmt.Errorf("sed 替换命令缺少结束定界符 %c", delimiter)
}

func unescape(text string) (string, error) {
	out := make([]byte, 0, len(text))
	for index := 0; index < len(text); index++ {
		value := text[index]
		if value != '\\' {
			out = append(out, value)
			continue
		}
		if index+1 >= len(text) {
			return "", fmt.Errorf("sed 转写命令以未完成的反斜杠结尾")
		}
		index++
		switch next := text[index]; next {
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case 'r':
			out = append(out, '\r')
		default:
			out = append(out, next)
		}
	}
	return string(out), nil
}

func expansion(replacement string) string {
	var out strings.Builder
	for index := 0; index < len(replacement); index++ {
		value := replacement[index]
		if value == '\\' && index+1 < len(replacement) {
			next := replacement[index+1]
			if next >= '1' && next <= '9' {
				out.WriteString("${")
				out.WriteByte(next)
				out.WriteString("}")
				index++
				continue
			}
			if next == '&' {
				out.WriteByte('&')
				index++
				continue
			}
			out.WriteByte(next)
			index++
			continue
		}
		if value == '&' {
			out.WriteString("${0}")
			continue
		}
		out.WriteByte(value)
	}
	return out.String()
}
