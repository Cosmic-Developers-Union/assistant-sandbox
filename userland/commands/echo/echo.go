package echocmd

import (
	"bytes"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

var cmd = &toolcompat.Tool{
	Name:     "echo",
	Synopsis: "Display a line of text.",
	Usage:    "echo [SHORT-OPTION]... [STRING]...\n   or: echo LONG-OPTION",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {

	posix := envPresent(rc.Env, "POSIXLY_CORRECT")
	if !posix && len(args) == 1 && (args[0] == "--help" || args[0] == "--version") {
		if args[0] == "--help" {
			printHelp(rc)
			return 0
		}
		_, _ = fmt.Fprintf(rc.Out, "%s (qiangli/coreutils) %s\n", cmd.Name, toolcompat.Version)
		return 0
	}

	noNewline := false
	escapes := false
	if posix {

		escapes = true
		if len(args) == 0 || args[0] != "-n" {
			return writeOperands(rc, args, escapes, noNewline)
		}
	}

	i := 0
scan:
	for ; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			break
		}
		for j := 1; j < len(a); j++ {
			switch a[j] {
			case 'n', 'e', 'E':
			default:
				break scan
			}
		}
		for j := 1; j < len(a); j++ {
			switch a[j] {
			case 'n':
				noNewline = true
			case 'e':
				escapes = true
			case 'E':
				escapes = false
			}
		}
	}
	if posix {

		escapes = true
	}

	return writeOperands(rc, args[i:], escapes, noNewline)
}

func writeOperands(rc *toolcompat.RunContext, operands []string, escapes, noNewline bool) int {
	var buf bytes.Buffer
	stopped := false
	for k, s := range operands {
		var operand bytes.Buffer
		if escapes {
			if interpretEscapes(&operand, s) {
				stopped = true
				if k > 0 && operand.Len() > 0 {
					buf.WriteByte(' ')
				}
				buf.Write(operand.Bytes())
				break
			}
		} else {
			operand.WriteString(s)
		}
		if k > 0 {
			buf.WriteByte(' ')
		}
		buf.Write(operand.Bytes())
	}
	if !stopped && !noNewline {
		buf.WriteByte('\n')
	}
	if _, err := rc.Out.Write(buf.Bytes()); err != nil {

		_, _ = fmt.Fprintf(rc.Err, "echo: write error: %v\n", toolcompat.SysErr(err))
		return 1
	}
	return 0
}

func envPresent(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func printHelp(rc *toolcompat.RunContext) {
	_, _ = fmt.Fprintf(rc.Out, "Usage: %s\n%s\n", cmd.Usage, cmd.Synopsis)
	_, _ = fmt.Fprint(rc.Out, `
Options:
  -n          do not output the trailing newline
  -e          enable interpretation of backslash escapes
  -E          disable interpretation of backslash escapes (default)
  --help     display this help and exit
  --version  output version information and exit
`)
}

func interpretEscapes(buf *bytes.Buffer, s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			buf.WriteByte(c)
			continue
		}
		if i == len(s)-1 {

			buf.WriteByte('\\')
			continue
		}
		i++
		switch s[i] {
		case 'a':
			buf.WriteByte('\a')
		case 'b':
			buf.WriteByte('\b')
		case 'c':
			return true
		case 'e':
			buf.WriteByte(0x1b)
		case 'f':
			buf.WriteByte('\f')
		case 'n':
			buf.WriteByte('\n')
		case 'r':
			buf.WriteByte('\r')
		case 't':
			buf.WriteByte('\t')
		case 'v':
			buf.WriteByte('\v')
		case '\\':
			buf.WriteByte('\\')
		case '0':

			v := 0
			j := i + 1
			for j < len(s) && j-i-1 < 3 && s[j] >= '0' && s[j] <= '7' {
				v = v*8 + int(s[j]-'0')
				j++
			}
			buf.WriteByte(byte(v))
			i = j - 1
		case 'x':

			v, n := 0, 0
			j := i + 1
			for j < len(s) && n < 2 {
				d, ok := hexVal(s[j])
				if !ok {
					break
				}
				v = v*16 + d
				j++
				n++
			}
			if n == 0 {
				buf.WriteString("\\x")
			} else {
				buf.WriteByte(byte(v))
				i = j - 1
			}
		default:

			buf.WriteByte('\\')
			buf.WriteByte(s[i])
		}
	}
	return false
}

func hexVal(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}
