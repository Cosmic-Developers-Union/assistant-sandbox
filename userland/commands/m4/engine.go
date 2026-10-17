package m4

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

const maxNesting = 4096

const maxEvalWidth = 1 << 20

type macro struct {
	text string
	fn   builtinFunc
	name string

	blind bool
}

type argument struct {
	text string
	fn   *macro

	present bool
}

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokText
	tokName
	tokChar
)

type abort struct{}

type sourceLoc struct {
	file string
	line int
}

type processor struct {
	ctx    context.Context
	stdin  io.Reader
	stderr io.Writer
	stdout io.Writer
	out    *bufio.Writer

	control proc.Control
	pid     proc.PID
	env     map[string]string
	dir     string

	macros map[string][]*macro

	lquote, rquote string
	bcomm, ecomm   string

	pushed    []byte
	pushedLoc []sourceLoc
	src       string
	pos       int

	files     []string
	options   []macroOp
	fileIndex int

	file string
	line int

	srcFile   string
	srcLine   int
	srcPendNL bool

	stdinDone bool

	nesting int
	ticks   int
	failed  bool

	divnum     int
	diversions [10]strings.Builder
	wraps      []string
	sysval     int
	exitSet    bool
	exitCode   int
}

func (p *processor) process() (code int) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(abort); !ok {
				panic(r)
			}

			code = 1
			if p.exitSet {
				code = p.exitCode
			}
		}
		if err := p.out.Flush(); err != nil && code == 0 {
			_, _ = fmt.Fprintf(p.stderr, "m4: 写入失败: %v\n", err)
			code = 1
		}
	}()
	for {
		kind, s := p.token()
		switch kind {
		case tokEOF:
			if len(p.wraps) > 0 {

				w := p.wraps[0]
				p.wraps = p.wraps[1:]
				p.pushback(w)
				continue
			}
			p.flushDiversions()
			if p.failed {
				return 1
			}
			return 0
		case tokName:
			if handled, _ := p.expand(s); handled {
				continue
			}
			p.emit(s)
		default:
			p.emit(s)
		}
	}
}

func (p *processor) warnf(format string, a ...any) {
	_ = p.out.Flush()
	p.writeDiagnostic(format, a...)
}

func (p *processor) fatalf(format string, a ...any) {
	p.warnf(format, a...)
	panic(abort{})
}

func (p *processor) nextFile() bool {
	for len(p.files) > 0 {
		for len(p.options) > 0 && p.options[0].beforeFile == p.fileIndex {
			op := p.options[0]
			p.options = p.options[1:]
			if op.undefine {
				delete(p.macros, op.arg)
				continue
			}
			name, value, _ := strings.Cut(op.arg, "=")
			p.define(name, &macro{text: value}, false)
		}
		name := p.files[0]
		p.files = p.files[1:]
		p.fileIndex++
		var data string
		var err error
		display := name
		if name == "-" {
			display = "stdin"
			data, err = p.takeStdin()
		} else {
			data, err = readGuestFile(p.ctx, p.dir, name)
		}
		if err != nil {
			_ = p.out.Flush()
			_, _ = fmt.Fprintf(p.stderr, "m4: %s: %v\n", name, err)
			p.failed = true
			continue
		}
		p.src, p.pos = data, 0
		p.srcFile, p.srcLine, p.srcPendNL = display, 1, false
		p.file, p.line = p.srcFile, p.srcLine
		return true
	}
	return false
}

func (p *processor) fill() bool {
	for len(p.pushed) == 0 && p.pos >= len(p.src) {
		if !p.nextFile() {
			return false
		}
	}
	return true
}

func (p *processor) next() (byte, bool) {
	if !p.fill() {
		return 0, false
	}
	if n := len(p.pushed); n > 0 {
		c := p.pushed[n-1]
		loc := p.pushedLoc[n-1]
		p.pushed = p.pushed[:n-1]
		p.pushedLoc = p.pushedLoc[:n-1]
		p.file, p.line = loc.file, loc.line
		return c, true
	}
	if p.srcPendNL {
		p.srcLine++
		p.srcPendNL = false
	}
	c := p.src[p.pos]
	p.pos++
	p.file, p.line = p.srcFile, p.srcLine
	if c == '\n' {
		p.srcPendNL = true
	}
	return c, true
}

func (p *processor) peekAt(i int) (byte, bool) {
	n := len(p.pushed)
	if i < n {
		return p.pushed[n-1-i], true
	}
	if j := p.pos + i - n; j < len(p.src) {
		return p.src[j], true
	}
	return 0, false
}

func (p *processor) hasPrefix(s string) bool {
	for i := 0; i < len(s); i++ {
		if c, ok := p.peekAt(i); !ok || c != s[i] {
			return false
		}
	}
	return true
}

func (p *processor) skip(n int) {
	for ; n > 0; n-- {
		p.next()
	}
}

func (p *processor) pushback(s string) {
	loc := sourceLoc{file: p.file, line: p.line}
	for i := len(s) - 1; i >= 0; i-- {
		p.pushed = append(p.pushed, s[i])
		p.pushedLoc = append(p.pushedLoc, loc)
	}
}

func (p *processor) pushIncluded(name string, data string) {
	locs := make([]sourceLoc, len(data))
	line := 1
	for i, c := range []byte(data) {
		locs[i] = sourceLoc{file: name, line: line}
		if c == '\n' {
			line++
		}
	}
	for i := len(data) - 1; i >= 0; i-- {
		p.pushed = append(p.pushed, data[i])
		p.pushedLoc = append(p.pushedLoc, locs[i])
	}
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

func (p *processor) token() (tokenKind, string) {

	if p.ticks++; p.ticks&0xfff == 0 && p.ctx.Err() != nil {
		p.fatalf("已取消")
	}
	if !p.fill() {
		return tokEOF, ""
	}
	if p.bcomm != "" && p.hasPrefix(p.bcomm) {
		p.skip(len(p.bcomm))
		b := []byte(p.bcomm)
		for {
			if p.hasPrefix(p.ecomm) {
				p.skip(len(p.ecomm))
				return tokText, string(append(b, p.ecomm...))
			}
			if _, ok := p.peekAt(0); !ok {
				return tokText, string(b)
			}
			c, _ := p.next()
			b = append(b, c)
		}
	}
	c, _ := p.peekAt(0)
	if isNameStart(c) {
		var b []byte
		for {
			c, ok := p.peekAt(0)
			if !ok || !isNameChar(c) {
				return tokName, string(b)
			}
			p.next()
			b = append(b, c)
		}
	}
	if p.lquote != "" && p.hasPrefix(p.lquote) {
		p.skip(len(p.lquote))
		return tokText, p.quoted()
	}
	p.next()
	return tokChar, string(c)
}

func (p *processor) quoted() string {
	var b []byte
	depth := 1
	for {
		if p.hasPrefix(p.rquote) {
			p.skip(len(p.rquote))
			if depth--; depth == 0 {
				return string(b)
			}
			b = append(b, p.rquote...)
			continue
		}
		if p.lquote != "" && p.hasPrefix(p.lquote) {
			p.skip(len(p.lquote))
			depth++
			b = append(b, p.lquote...)
			continue
		}
		c, ok := p.next()
		if !ok {
			p.fatalf("字符串缺少结束引号")
		}
		b = append(b, c)
	}
}

func (p *processor) lookup(name string) *macro {
	if st := p.macros[name]; len(st) > 0 {
		return st[len(st)-1]
	}
	return nil
}

func (p *processor) define(name string, m *macro, push bool) {
	st := p.macros[name]
	if push || len(st) == 0 {
		p.macros[name] = append(st, m)
		return
	}
	st[len(st)-1] = m
}

func (p *processor) expand(name string) (handled bool, fn *macro) {
	m := p.lookup(name)
	if m == nil {
		return false, nil
	}
	c, ok := p.peekAt(0)
	called := ok && c == '('
	if !called && m.blind {
		return false, nil
	}
	var args []argument
	if called {
		p.next()
		args = p.collect()
	}
	if m.fn != nil {
		text, fn := m.fn(p, m.name, args)
		p.pushback(text)
		return true, fn
	}
	text := p.substitute(name, m.text, args)
	p.pushback(text)
	return true, nil
}

func (p *processor) collect() []argument {
	if p.nesting++; p.nesting > maxNesting {
		p.fatalf("宏调用嵌套过深（上限 %d）", maxNesting)
	}
	defer func() { p.nesting-- }()
	var args []argument
	for {
		for {
			if !p.fill() {
				p.fatalf("参数列表缺少右括号")
			}
			if c, ok := p.peekAt(0); !ok || !isSpace(c) {
				break
			}
			p.next()
		}
		var b []byte
		var argFn *macro
		present := false
		parens := 0
	arg:
		for {
			kind, s := p.token()
			switch kind {
			case tokEOF:
				p.fatalf("参数列表缺少右括号")
			case tokText:
				present = true
				b = append(b, s...)
			case tokName:
				present = true
				handled, fn := p.expand(s)
				if !handled {
					b = append(b, s...)
				} else if fn != nil && len(b) == 0 {
					argFn = fn
				}
			case tokChar:
				switch {
				case s == "(":
					parens++
				case s == ")" && parens > 0:
					parens--
				case s == ")":
					return append(args, argument{text: string(b), fn: argFn, present: present})
				case s == "," && parens == 0:
					args = append(args, argument{text: string(b), fn: argFn, present: present})
					break arg
				}
				present = true
				b = append(b, s...)
			}
		}
	}
}

func (p *processor) substitute(name, text string, args []argument) string {
	if !strings.Contains(text, "$") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '$' || i+1 == len(text) {
			b.WriteByte(c)
			continue
		}
		switch d := text[i+1]; {
		case d == '0':
			b.WriteString(name)
		case d >= '1' && d <= '9':
			if n := int(d - '0'); n <= len(args) {
				b.WriteString(args[n-1].text)
			}
		case d == '#':
			b.WriteString(strconv.Itoa(len(args)))
		case d == '*':
			b.WriteString(joinArgs(args, "", ""))
		case d == '@':
			b.WriteString(joinArgs(args, p.lquote, p.rquote))
		default:
			b.WriteByte(c)
			continue
		}
		i++
	}
	return b.String()
}

func joinArgs(args []argument, lq, rq string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(lq)
		b.WriteString(a.text)
		b.WriteString(rq)
	}
	return b.String()
}

func (p *processor) writeOutput(s string) {
	if p.divnum == 0 {
		_, _ = p.out.WriteString(s)
	} else if p.divnum > 0 && p.divnum < len(p.diversions) {
		_, _ = p.diversions[p.divnum].WriteString(s)
	}
}

func (p *processor) emit(s string) {
	if p.divnum < 0 || p.divnum >= len(p.diversions) {
		return
	}
	p.writeOutput(s)
}

func (p *processor) flushDiversions() {
	old := p.divnum
	p.divnum = 0
	for i := 1; i < len(p.diversions); i++ {
		if p.diversions[i].Len() == 0 {
			continue
		}
		s := p.diversions[i].String()
		p.diversions[i].Reset()
		p.writeOutput(s)
	}
	p.divnum = old
}

func (p *processor) runSystem(cmd string, capture bool) (string, int) {
	if err := p.out.Flush(); err != nil {
		p.warnf("写入失败: %v", err)
		p.failed = true
		return "", 127
	}
	if p.control == nil {
		p.warnf("syscmd: 缺少进程控制端口，按错误 127 处理")
		return "", 127
	}
	streams := fd.Streams{Input: p.stdin, Output: p.stdout, Error: p.stderr}
	var captured strings.Builder
	if capture {
		streams.Output = &captured
	}
	terminal, err := p.control.Run(p.ctx, proc.RunSpec{
		Parent:  p.pid,
		Name:    "bash",
		Args:    []string{"-c", cmd},
		Dir:     p.dir,
		Env:     p.env,
		Streams: streams,
	})
	if err != nil {
		switch {
		case errors.Is(err, errno.NotFound):
			p.warnf("syscmd: 未注册沙箱命令 bash，按错误 127 处理")
			return captured.String(), 127
		case errors.Is(err, errno.NoExec), errors.Is(err, errno.Permission):
			p.warnf("syscmd: 无法执行 bash，按错误 126 处理")
			return captured.String(), 126
		}
		p.warnf("syscmd: %v，按错误 127 处理", err)
		return captured.String(), 127
	}
	return captured.String(), int(terminal.ExitCode)
}
