package printfcmd

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

var cmd = &toolcompat.Tool{
	Name:     "printf",
	Synopsis: "Format and print ARGUMENT(s) according to FORMAT.",
	Usage:    "printf FORMAT [ARGUMENT]...\n   or: printf OPTION",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printHelp(rc)
		return 0
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-V") {
		_, _ = fmt.Fprintf(rc.Out, "%s (qiangli/coreutils) %s\n", cmd.Name, toolcompat.Version)
		return 0
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return toolcompat.UsageError(rc, cmd, "missing operand")
	}

	format := args[0]
	values := args[1:]

	var out bytes.Buffer
	pos := 0
	failed := false
	firstPass := true
	for firstPass || pos < len(values) {
		startPos := pos
		halt, fatal := formatOnce(rc, &out, format, values, &pos, &failed)
		if fatal || halt {
			break
		}
		firstPass = false
		if pos == startPos {
			break
		}
	}

	n, err := rc.Out.Write(out.Bytes())
	if err == nil && n != out.Len() {
		err = fmt.Errorf("short write")
	}
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "%s: write error: %v\n", cmd.Name, err)
		return 1
	}
	if failed {
		return 1
	}
	return 0
}

func formatOnce(rc *toolcompat.RunContext, out *bytes.Buffer, format string, values []string, pos *int, failed *bool) (halt, fatal bool) {
	nextArg := func() (string, bool) {
		if *pos < len(values) {
			s := values[*pos]
			*pos++
			return s, true
		}
		return "", false
	}

	i := 0
	for i < len(format) {
		c := format[i]
		switch c {
		case '\\':
			adv, stop := writeFormatEscape(out, format[i:])
			i += adv
			if stop {
				return true, false
			}
		case '%':
			specStart := i
			j := i + 1
			if j >= len(format) {
				diagInvalidConversion(rc, "%")
				*failed = true
				return false, true
			}
			if format[j] == '%' {
				out.WriteByte('%')
				i = j + 1
				continue
			}

			flagsStart := j
			for j < len(format) && strings.IndexByte("-+ 0#", format[j]) >= 0 {
				j++
			}
			flags := format[flagsStart:j]

			width := 0
			hasWidth := false
			widthFromArg := false
			if j < len(format) && format[j] == '*' {
				hasWidth, widthFromArg = true, true
				j++
			} else {
				wStart := j
				for j < len(format) && format[j] >= '0' && format[j] <= '9' {
					j++
				}
				if j > wStart {
					width, _ = strconv.Atoi(format[wStart:j])
					hasWidth = true
				}
			}

			precision := 0
			hasPrecision := false
			precFromArg := false
			if j < len(format) && format[j] == '.' {
				j++
				hasPrecision = true
				if j < len(format) && format[j] == '*' {
					precFromArg = true
					j++
				} else {
					pStart := j
					for j < len(format) && format[j] >= '0' && format[j] <= '9' {
						j++
					}
					precision, _ = strconv.Atoi(format[pStart:j])
				}
			}

			if j >= len(format) {
				diagInvalidConversion(rc, format[specStart:])
				*failed = true
				return false, true
			}
			conv := format[j]
			i = j + 1

			if widthFromArg {
				s, ok := nextArg()
				if ok {
					v, hadErr := parseSigned(rc, s)
					if hadErr {
						*failed = true
					}
					width = int(v)
				}
			}
			if precFromArg {
				s, ok := nextArg()
				if ok {
					v, hadErr := parseSigned(rc, s)
					if hadErr {
						*failed = true
					}
					precision = int(v)
				}
			}
			if hasWidth && width < 0 {
				if !strings.ContainsRune(flags, '-') {
					flags += "-"
				}
				width = -width
			}
			if hasPrecision && precision < 0 {
				hasPrecision = false
			}

			switch conv {
			case 's':
				s, _ := nextArg()
				writeStringField(out, s, flags, width, hasWidth, precision, hasPrecision)
			case 'b':
				s, _ := nextArg()
				var bbuf bytes.Buffer
				stop := writeBEscapes(&bbuf, s)
				if stop {

					out.Write(bbuf.Bytes())
					return true, false
				}

				writeStringField(out, bbuf.String(), flags, width, hasWidth, precision, hasPrecision)
			case 'c':
				s, _ := nextArg()

				ch := "\x00"
				if len(s) > 0 {
					ch = s[:1]
				}
				writeStringField(out, ch, flags, width, hasWidth, 0, false)
			case 'd', 'i':
				s, ok := nextArg()
				v := int64(0)
				if ok {
					var hadErr bool
					v, hadErr = parseSigned(rc, s)
					if hadErr {
						*failed = true
					}
				}
				writeGoNumeric(out, "d", flags, width, hasWidth, precision, hasPrecision, v)
			case 'o', 'u', 'x', 'X':
				s, ok := nextArg()
				u := uint64(0)
				if ok {
					var hadErr bool
					u, hadErr = parseUnsigned(rc, s)
					if hadErr {
						*failed = true
					}
				}
				goConv := string(conv)
				if conv == 'u' {
					goConv = "d"
				}
				writeGoNumeric(out, goConv, flags, width, hasWidth, precision, hasPrecision, u)
			case 'e', 'E', 'f', 'F', 'g', 'G', 'a', 'A':
				s, ok := nextArg()
				fv := 0.0
				if ok {
					var hadErr bool
					fv, hadErr = parseFloat(rc, s)
					if hadErr {
						*failed = true
					}
				}
				switch {
				case math.IsInf(fv, 0) || math.IsNaN(fv):

					writeNonFinite(out, conv, flags, width, hasWidth, fv)
				case conv == 'a' || conv == 'A':
					var localized bytes.Buffer
					writeHexFloat(&localized, conv, flags, width, hasWidth, precision, hasPrecision, fv)
					out.WriteString(localizeRadix(localized.String(), numericRadix(rc.Env)))
				default:
					if conv == 'g' || conv == 'G' {
						if !hasPrecision {
							hasPrecision, precision = true, 6
						}
					}
					var localized bytes.Buffer
					writeGoNumeric(&localized, string(conv), flags, width, hasWidth, precision, hasPrecision, fv)
					out.WriteString(localizeRadix(localized.String(), numericRadix(rc.Env)))
				}
			default:
				diagInvalidConversion(rc, format[specStart:i])
				*failed = true
				return false, true
			}
		default:
			out.WriteByte(c)
			i++
		}
	}
	return false, false
}

func diagInvalidConversion(rc *toolcompat.RunContext, spec string) {
	_, _ = fmt.Fprintf(rc.Err, "%s: %s: invalid conversion specification\n", cmd.Name, spec)
}

func buildVerb(flags string, width int, hasWidth bool, precision int, hasPrecision bool, goConv string) string {
	var verb strings.Builder
	verb.WriteByte('%')
	verb.WriteString(flags)
	if hasWidth {
		verb.WriteString(strconv.Itoa(width))
	}
	if hasPrecision {
		verb.WriteByte('.')
		verb.WriteString(strconv.Itoa(precision))
	}
	verb.WriteString(goConv)
	return verb.String()
}

func writeGoNumeric(out *bytes.Buffer, goConv, flags string, width int, hasWidth bool, precision int, hasPrecision bool, value any) {
	fmt.Fprintf(out, buildVerb(flags, width, hasWidth, precision, hasPrecision, goConv), value)
}

func writeStringField(buf *bytes.Buffer, s string, flags string, width int, hasWidth bool, precision int, hasPrecision bool) {
	data := s
	if hasPrecision && precision < len(data) {
		data = data[:precision]
	}
	pad := 0
	if hasWidth && width > len(data) {
		pad = width - len(data)
	}
	left := strings.ContainsRune(flags, '-')
	if !left && pad > 0 {
		buf.WriteString(strings.Repeat(" ", pad))
	}
	buf.WriteString(data)
	if left && pad > 0 {
		buf.WriteString(strings.Repeat(" ", pad))
	}
}

func writeFormatEscape(buf *bytes.Buffer, s string) (adv int, stop bool) {
	if len(s) == 1 {
		buf.WriteByte('\\')
		return 1, false
	}
	switch s[1] {
	case 'a':
		buf.WriteByte('\a')
		return 2, false
	case 'b':
		buf.WriteByte('\b')
		return 2, false
	case 'c':
		return 2, true
	case 'e':
		buf.WriteByte(0x1b)
		return 2, false
	case 'f':
		buf.WriteByte('\f')
		return 2, false
	case 'n':
		buf.WriteByte('\n')
		return 2, false
	case 'r':
		buf.WriteByte('\r')
		return 2, false
	case 't':
		buf.WriteByte('\t')
		return 2, false
	case 'v':
		buf.WriteByte('\v')
		return 2, false
	case '\\':
		buf.WriteByte('\\')
		return 2, false
	case '"':
		buf.WriteByte('"')
		return 2, false
	case 'x':
		v, n := readHex(s[2:], 2)
		if n == 0 {
			buf.WriteString("\\x")
			return 2, false
		}
		buf.WriteByte(byte(v))
		return 2 + n, false
	case '0', '1', '2', '3', '4', '5', '6', '7':
		v, n := readOctal(s[1:], 3)
		buf.WriteByte(byte(v))
		return 1 + n, false
	default:
		buf.WriteByte('\\')
		buf.WriteByte(s[1])
		return 2, false
	}
}

func writeBEscapes(buf *bytes.Buffer, s string) bool {
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
		case '"':
			buf.WriteByte('"')
		case 'x':
			v, n := readHex(s[i+1:], 2)
			if n == 0 {
				buf.WriteString("\\x")
			} else {
				buf.WriteByte(byte(v))
				i += n
			}
		case '0':
			v, n := readOctal(s[i+1:], 3)
			buf.WriteByte(byte(v))
			i += n
		default:
			buf.WriteByte('\\')
			buf.WriteByte(s[i])
		}
	}
	return false
}

func readOctal(s string, max int) (v, n int) {
	for n < max && n < len(s) && s[n] >= '0' && s[n] <= '7' {
		v = v*8 + int(s[n]-'0')
		n++
	}
	return v, n
}

func readHex(s string, max int) (v, n int) {
	for n < max && n < len(s) {
		d, ok := hexVal(s[n])
		if !ok {
			break
		}
		v = v*16 + d
		n++
	}
	return v, n
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

func charConstant(s string) (value rune, ok bool, warn bool) {
	if s == "" || (s[0] != '\'' && s[0] != '"') {
		return 0, false, false
	}
	rest := s[1:]
	if rest == "" {
		return 0, true, false
	}
	r, size := utf8.DecodeRuneInString(rest)
	return r, true, size < len(rest)
}

func printHelp(rc *toolcompat.RunContext) {
	_, _ = fmt.Fprintf(rc.Out, "Usage: %s\n%s\n", cmd.Usage, cmd.Synopsis)
	_, _ = io.WriteString(rc.Out, `
FORMAT controls the output the same way as in C printf. Interpreted
sequences in FORMAT are:

  \\        backslash            \a        alert (BEL)
  \b        backspace            \c        produce no further output
  \e        escape               \f        form feed
  \n        new line             \r        carriage return
  \t        horizontal tab       \v        vertical tab
  \"        double quote         \NNN      byte with octal value NNN (1-3 digits)
  \xHH      byte with hexadecimal value HH (1-2 digits)

  %%        a single %           %b        ARGUMENT with '\' escapes interpreted
  %c        first character of ARGUMENT    %s   ARGUMENT as a string
  %d %i %o %u %x %X               signed/unsigned/octal/hex integer conversions
  %e %E %f %F %g %G %a %A         floating-point conversions

FORMAT is reused as many times as necessary to convert all the given
ARGUMENTs; missing ARGUMENTs are treated as null strings or zero.

Options:
  -h, --help     display this help and exit
  -V, --version  output version information and exit
`)
}
