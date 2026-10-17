package testcmd

import (
	"fmt"
	"io/fs"
	"math/big"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

var cmd = &toolcompat.Tool{
	Name:     "test",
	Synopsis: "Evaluate a conditional expression.",
	Usage:    "test EXPRESSION\n  or:  test",
}

var bracketCmd = &toolcompat.Tool{
	Name:     "[",
	Synopsis: "Evaluate a conditional expression (test, spelled with a closing ']').",
	Usage:    "[ EXPRESSION ]\n  or:  [ ]",
}

func init() {
	cmd.Run = func(rc *toolcompat.RunContext, args []string) int { return run(rc, cmd, args) }
	bracketCmd.Run = func(rc *toolcompat.RunContext, args []string) int { return run(rc, bracketCmd, args) }
	toolcompat.Register(cmd)
	toolcompat.Register(bracketCmd)
}

const (
	statusTrue    = 0
	statusFalse   = 1
	statusSyntax  = 2
	statusPending = -1
)

func run(rc *toolcompat.RunContext, t *toolcompat.Tool, args []string) int {
	if t == bracketCmd {

		if len(args) == 1 {
			switch args[0] {
			case "--help":
				return help(rc, t)
			case "--version":
				return version(rc, t)
			}
		}

		if len(args) == 0 || args[len(args)-1] != "]" {
			_, _ = fmt.Fprintf(rc.Err, "%s: missing %s\n", t.Name, quote("]"))
			return statusSyntax
		}
		args = args[:len(args)-1]
	}

	p := &parser{rc: rc, name: t.Name, args: args}
	value, code := p.evaluate()
	if code != statusPending {
		return code
	}
	if value {
		return statusTrue
	}
	return statusFalse
}

func help(rc *toolcompat.RunContext, t *toolcompat.Tool) int {
	_, _ = fmt.Fprintf(rc.Out, "Usage: %s\n%s\n", t.Usage, t.Synopsis)
	_, _ = fmt.Fprint(rc.Out, `
Exit status is 0 if EXPRESSION is true, 1 if false, 2 if EXPRESSION is
malformed. An omitted EXPRESSION is false.

Expressions:
  ( EXPRESSION )           EXPRESSION is true
  ! EXPRESSION             EXPRESSION is false
  EXPRESSION1 -a EXPRESSION2   both EXPRESSION1 and EXPRESSION2 are true
  EXPRESSION1 -o EXPRESSION2   either EXPRESSION1 or EXPRESSION2 is true

  -n STRING                the length of STRING is non-zero
  STRING                   equivalent to -n STRING
  -z STRING                the length of STRING is zero
  STRING1 = STRING2        the strings are equal (== is a synonym)
  STRING1 != STRING2       the strings are not equal
  STRING1 < STRING2        STRING1 sorts before STRING2 (byte order)
  STRING1 > STRING2        STRING1 sorts after STRING2 (byte order)

  INTEGER1 -eq INTEGER2    the integers are equal
  INTEGER1 -ne INTEGER2    the integers are not equal
  INTEGER1 -gt INTEGER2    INTEGER1 is greater than INTEGER2
  INTEGER1 -ge INTEGER2    INTEGER1 is greater than or equal to INTEGER2
  INTEGER1 -lt INTEGER2    INTEGER1 is less than INTEGER2
  INTEGER1 -le INTEGER2    INTEGER1 is less than or equal to INTEGER2

  FILE1 -ef FILE2          same device and inode numbers
  FILE1 -nt FILE2          FILE1 is newer than FILE2, or FILE2 is missing
  FILE1 -ot FILE2          FILE1 is older than FILE2, or FILE1 is missing

  -e FILE                  FILE exists
  -a FILE                  same as -e (deprecated)
  -b FILE                  FILE exists and is a block special file
  -c FILE                  FILE exists and is a character special file
  -d FILE                  FILE exists and is a directory
  -f FILE                  FILE exists and is a regular file
  -g FILE                  FILE exists and is set-group-ID
  -G FILE                  FILE exists and is owned by the effective group ID
  -h FILE                  FILE exists and is a symbolic link (same as -L)
  -k FILE                  FILE exists and has its sticky bit set
  -L FILE                  FILE exists and is a symbolic link (same as -h)
  -N FILE                  FILE exists and has been modified since it was last read
  -O FILE                  FILE exists and is owned by the effective user ID
  -p FILE                  FILE exists and is a named pipe
  -r FILE                  FILE exists and read permission is granted
  -s FILE                  FILE exists and has a size greater than zero
  -S FILE                  FILE exists and is a socket
  -t FD                    file descriptor FD is opened on a terminal
  -u FILE                  FILE exists and its set-user-ID bit is set
  -w FILE                  FILE exists and write permission is granted
  -x FILE                  FILE exists and execute (or search) permission is granted

Options:
      --help     display this help and exit
      --version  output version information and exit

NOTE: only [ honors --help and --version, and only as its single
argument: '[ --help' prints this text, while '[ --help ]' is a string
test. 'test' has no options at all -- it treats --help and --version as
ordinary non-empty strings, so 'test --help' is simply true.
`)
	return statusTrue
}

func version(rc *toolcompat.RunContext, t *toolcompat.Tool) int {
	_, _ = fmt.Fprintf(rc.Out, "%s (assistant-sandbox) %s\n", t.Name, toolcompat.Version)
	return statusTrue
}

func quote(s string) string { return "'" + s + "'" }

type syntaxError struct{ msg string }

type parser struct {
	rc   *toolcompat.RunContext
	name string
	args []string
	pos  int
}

func (p *parser) fail(format string, a ...any) {
	panic(syntaxError{msg: fmt.Sprintf(format, a...)})
}

func (p *parser) beyond() {
	last := ""
	if len(p.args) > 0 {
		last = p.args[len(p.args)-1]
	}
	p.fail("missing argument after %s", quote(last))
}

func (p *parser) remaining() int { return len(p.args) - p.pos }

func (p *parser) at(i int) string { return p.args[p.pos+i] }

func (p *parser) next() string {
	if p.pos >= len(p.args) {
		p.beyond()
	}
	s := p.args[p.pos]
	p.pos++
	return s
}

func (p *parser) evaluate() (result bool, code int) {
	code = statusPending
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		se, ok := r.(syntaxError)
		if !ok {
			panic(r)
		}
		_, _ = fmt.Fprintf(p.rc.Err, "%s: %s\n", p.name, se.msg)
		result, code = false, statusSyntax
	}()

	var value bool
	switch len(p.args) {
	case 0:

		return false, statusPending
	case 1:
		value = p.oneArgument()
	case 2:
		value = p.twoArguments()
	case 3:
		value = p.threeArguments()
	case 4:
		switch {
		case p.at(0) == "!":
			p.pos++
			value = !p.threeArguments()
		case p.at(0) == "(" && p.at(3) == ")":
			p.pos++
			value = p.twoArguments()
			p.pos++
		default:

			value = p.expression()
		}
	default:
		value = p.expression()
	}
	if p.pos != len(p.args) {
		p.fail("extra argument %s", quote(p.args[p.pos]))
	}
	return value, statusPending
}

func (p *parser) oneArgument() bool { return p.next() != "" }

func (p *parser) twoArguments() bool {
	switch a := p.at(0); {
	case a == "!":
		p.pos++
		return !p.oneArgument()
	case isUnaryOp(a):
		return p.unaryOperator()
	default:
		p.fail("%s: unary operator expected", quote(a))
		return false
	}
}

func (p *parser) threeArguments() bool {
	switch {
	case isBinaryOp(p.at(1)):
		return p.binaryOperator()
	case p.at(0) == "!":
		p.pos++
		return !p.twoArguments()
	case p.at(0) == "(" && p.at(2) == ")":
		p.pos++
		value := p.oneArgument()
		p.pos++
		return value
	case p.at(1) == "-a" || p.at(1) == "-o":
		return p.expression()
	default:
		p.fail("%s: binary operator expected", quote(p.at(1)))
		return false
	}
}

func (p *parser) expression() bool {
	if p.pos >= len(p.args) {
		p.beyond()
	}
	return p.or()
}

func (p *parser) or() bool {
	value := false
	for {

		if p.and() {
			value = true
		}
		if p.pos < len(p.args) && p.args[p.pos] == "-o" {
			p.pos++
			continue
		}
		return value
	}
}

func (p *parser) and() bool {
	value := true
	for {
		if !p.term() {
			value = false
		}
		if p.pos < len(p.args) && p.args[p.pos] == "-a" {
			p.pos++
			continue
		}
		return value
	}
}

func (p *parser) term() bool {
	if p.pos >= len(p.args) {
		p.beyond()
	}
	if p.args[p.pos] == "!" {

		negate := false
		for p.pos < len(p.args) && p.args[p.pos] == "!" {
			p.pos++
			negate = !negate
		}
		return negate != p.term()
	}
	if p.args[p.pos] == "(" {
		p.pos++
		value := p.expression()
		if p.pos >= len(p.args) || p.args[p.pos] != ")" {
			p.fail("%s expected", quote(")"))
		}
		p.pos++
		return value
	}
	if p.remaining() >= 3 && isBinaryOp(p.at(1)) {
		return p.binaryOperator()
	}
	if a := p.at(0); len(a) == 2 && a[0] == '-' {
		if isUnaryOp(a) {
			return p.unaryOperator()
		}
		p.fail("%s: unary operator expected", quote(a))
	}
	return p.next() != ""
}

const unaryOps = "abcdefghknprstuwxzGLNOS"

func isUnaryOp(s string) bool {
	return len(s) == 2 && s[0] == '-' && strings.IndexByte(unaryOps, s[1]) >= 0
}

func isBinaryOp(s string) bool {
	switch s {
	case "=", "==", "!=", "<", ">",
		"-eq", "-ne", "-lt", "-le", "-gt", "-ge",
		"-ef", "-nt", "-ot":
		return true
	}
	return false
}

func (p *parser) unaryOperator() bool {
	op := p.next()[1]
	operand := p.next()
	switch op {
	case 'n':
		return operand != ""
	case 'z':
		return operand == ""
	case 't':
		return p.terminalTest(operand)
	default:
		return p.fileTest(op, operand)
	}
}

func (p *parser) binaryOperator() bool {
	left := p.next()
	op := p.next()
	right := p.next()
	switch op {
	case "=", "==":
		return left == right
	case "!=":
		return left != right
	case "<":
		return left < right
	case ">":
		return left > right
	case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
		c := p.integer(left).Cmp(p.integer(right))
		switch op {
		case "-eq":
			return c == 0
		case "-ne":
			return c != 0
		case "-lt":
			return c < 0
		case "-le":
			return c <= 0
		case "-gt":
			return c > 0
		default:
			return c >= 0
		}
	case "-ef":
		li, lerr := statOperand(p.rc, left)
		ri, rerr := statOperand(p.rc, right)
		return lerr == nil && rerr == nil && vfs.SameFile(li, ri)
	case "-nt", "-ot":
		li, lerr := statOperand(p.rc, left)
		ri, rerr := statOperand(p.rc, right)
		if op == "-nt" {

			return lerr == nil && (rerr != nil || li.ModTime().After(ri.ModTime()))
		}
		return rerr == nil && (lerr != nil || li.ModTime().Before(ri.ModTime()))
	}

	p.fail("%s: binary operator expected", quote(op))
	return false
}

func (p *parser) integer(s string) *big.Int {
	body := strings.Trim(s, " \t")
	digits := body
	if strings.HasPrefix(digits, "+") || strings.HasPrefix(digits, "-") {
		digits = digits[1:]
	}
	if digits == "" {
		p.fail("invalid integer %s", quote(s))
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			p.fail("invalid integer %s", quote(s))
		}
	}
	n, ok := new(big.Int).SetString(body, 10)
	if !ok {
		p.fail("invalid integer %s", quote(s))
	}
	return n
}

func (p *parser) terminalTest(operand string) bool {

	fd := p.integer(operand)
	if !fd.IsInt64() || fd.Sign() < 0 {
		return false
	}
	return false
}

func (p *parser) fileTest(op byte, operand string) bool {
	switch op {
	case 'h', 'L':
		fi, err := lstatOperand(p.rc, operand)
		return err == nil && fi.Mode()&fs.ModeSymlink != 0
	case 'r', 'w', 'x':
		ok, err := accessOK(p.rc, operand, op)
		if err != nil {
			p.fail("-%c: %v", op, err)
		}
		return ok
	case 'O', 'G':
		owned, err := ownedByEffective(p.rc, operand, op == 'G')
		if err != nil {
			p.fail("-%c: %v", op, err)
		}
		return owned
	case 'N':
		atime, mtime, err := fileTimes(p.rc, operand)
		if err != nil {
			if vfs.NotExist(err) {
				return false
			}
			p.fail("-N: %v", err)
		}

		return mtime > atime
	}

	fi, err := statOperand(p.rc, operand)
	if err != nil {
		return false
	}
	mode := fi.Mode()
	switch op {
	case 'a', 'e':
		return true
	case 'f':
		return mode.IsRegular()
	case 'd':
		return fi.IsDir()
	case 's':
		return fi.Size() > 0
	case 'b':
		return mode&fs.ModeDevice != 0 && mode&fs.ModeCharDevice == 0
	case 'c':
		return mode&fs.ModeCharDevice != 0
	case 'p':
		return mode&fs.ModeNamedPipe != 0
	case 'S':
		return mode&fs.ModeSocket != 0
	case 'u':
		return mode&fs.ModeSetuid != 0
	case 'g':
		return mode&fs.ModeSetgid != 0
	case 'k':
		return mode&fs.ModeSticky != 0
	}

	p.fail("-%c: unary operator expected", op)
	return false
}

func statOperand(rc *toolcompat.RunContext, operand string) (fs.FileInfo, error) {
	return vfs.Stat(operand)
}

func lstatOperand(rc *toolcompat.RunContext, operand string) (fs.FileInfo, error) {
	return vfs.Lstat(operand)
}
