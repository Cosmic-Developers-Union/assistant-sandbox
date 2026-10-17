package bre

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
)

const (
	posStart = iota
	posAtom
	posAnchor
)

func ToGo(p string) (string, error) {
	return toGo(p, false, nil)
}

func toGo(p string, cutf8 bool, tables *LocaleByteTables) (string, error) {
	var b strings.Builder
	state := posStart
	i := 0
	for i < len(p) {
		c := p[i]
		switch c {
		case '\\':
			if i+1 >= len(p) {
				return "", fmt.Errorf("trailing backslash (\\)")
			}
			n := p[i+1]
			switch {
			case n == '(':
				b.WriteByte('(')
				state = posStart
				i += 2
			case n == ')':
				b.WriteByte(')')
				state = posAtom
				i += 2
			case n == '|':
				b.WriteByte('|')
				state = posStart
				i += 2
			case n == '{':
				if state != posAtom {
					return "", fmt.Errorf("\\{ with nothing to repeat")
				}
				end := strings.Index(p[i+2:], `\}`)
				if end < 0 {
					return "", fmt.Errorf("unmatched \\{")
				}
				inner := p[i+2 : i+2+end]
				norm, ok := normalizeInterval(inner)
				if !ok {
					return "", fmt.Errorf("invalid interval \\{%s\\}", inner)
				}
				b.WriteString("{" + norm + "}")
				state = posAtom
				i += 2 + end + 2
			case n == '}':
				return "", fmt.Errorf("unmatched \\}")
			case n == '+' || n == '?':
				if state != posAtom {
					return "", fmt.Errorf("\\%c with nothing to repeat", n)
				}
				b.WriteByte(n)
				i += 2
			case n >= '1' && n <= '9':
				return "", fmt.Errorf("back-reference \\%c is not supported (RE2 has no back-references)", n)
			case n == 'w' || n == 'W' || n == 's' || n == 'S' || n == 'b' || n == 'B':
				b.WriteByte('\\')
				b.WriteByte(n)
				state = posAtom
				i += 2
			case n == '<' || n == '>':
				return "", fmt.Errorf("\\%c word-edge anchor requires the BRE matcher", n)
			case n >= utf8.RuneSelf:
				r, sz := utf8.DecodeRuneInString(p[i+1:])
				b.WriteRune(r)
				state = posAtom
				i += 1 + sz
			case isAlnumByte(n):
				return "", fmt.Errorf("unsupported escape \\%c", n)
			default:
				b.WriteString(regexp.QuoteMeta(string(n)))
				state = posAtom
				i += 2
			}
		case '*':
			if state == posAtom {
				b.WriteByte('*')
			} else {
				b.WriteString(`\*`)
				state = posAtom
			}
			i++
		case '^':
			if state == posStart {
				b.WriteByte('^')
				state = posAnchor
			} else {
				b.WriteString(`\^`)
				state = posAtom
			}
			i++
		case '$':
			anchor := i == len(p)-1 ||
				(i+2 < len(p) && p[i+1] == '\\' && (p[i+2] == ')' || p[i+2] == '|'))
			if anchor {
				b.WriteByte('$')
				state = posAnchor
			} else {
				b.WriteString(`\$`)
				state = posAtom
			}
			i++
		case '[':
			cls, n, err := translateBracketMode(p[i:], cutf8, tables)
			if err != nil {
				return "", err
			}
			b.WriteString(cls)
			state = posAtom
			i += n
		case '.':
			b.WriteByte('.')
			state = posAtom
			i++
		case '(', ')', '{', '}', '+', '?', '|':

			b.WriteString(regexp.QuoteMeta(string(c)))
			state = posAtom
			i++
		default:
			b.WriteByte(c)
			state = posAtom
			i++
		}
	}
	return b.String(), nil
}

var sedEscape = map[byte]byte{
	'n': '\n', 't': '\t', 'r': '\r', 'f': '\f', 'v': '\v', 'a': '\a',
}

func SedEscapes(p string) string {
	if !strings.Contains(p, `\`) {
		return p
	}
	var b strings.Builder
	b.Grow(len(p))
	inBracket := false
	firstBracket := false
	for i := 0; i < len(p); i++ {
		if p[i] == '[' && !inBracket {
			inBracket = true
			firstBracket = true
			b.WriteByte(p[i])
			continue
		}
		if inBracket {
			b.WriteByte(p[i])
			if firstBracket && p[i] == '^' {
				continue
			}
			if p[i] == '[' && i+1 < len(p) && (p[i+1] == ':' || p[i+1] == '.' || p[i+1] == '=') {
				delim := p[i+1]
				end := strings.Index(p[i+2:], string(delim)+"]")
				if end >= 0 {
					end += i + 4
					b.WriteString(p[i+1 : end])
					i = end - 1
				}
				firstBracket = false
				continue
			}
			if p[i] == ']' && !firstBracket {
				inBracket = false
			}
			firstBracket = false
			continue
		}
		if p[i] != '\\' || i+1 >= len(p) {
			b.WriteByte(p[i])
			continue
		}
		if c, ok := sedEscape[p[i+1]]; ok {
			b.WriteByte(c)
		} else {
			b.WriteByte('\\')
			b.WriteByte(p[i+1])
		}
		i++
	}
	return b.String()
}

func normalizeInterval(s string) (string, bool) {
	digits := func(x string) bool {
		if x == "" {
			return false
		}
		for i := 0; i < len(x); i++ {
			if x[i] < '0' || x[i] > '9' {
				return false
			}
		}
		return true
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		if !digits(s) || !withinDupMax(s) {
			return "", false
		}
		return stripZeros(s), true
	}
	lo, hi := s[:comma], s[comma+1:]
	if strings.Contains(hi, ",") || (lo == "" && hi == "") {
		return "", false
	}
	if lo == "" {
		lo = "0"
	}
	if !digits(lo) || (hi != "" && !digits(hi)) {
		return "", false
	}

	if !withinDupMax(lo) || (hi != "" && !withinDupMax(hi)) {
		return "", false
	}
	if hi != "" && atoi(lo) > atoi(hi) {
		return "", false
	}

	hiN := hi
	if hi != "" {
		hiN = stripZeros(hi)
	}
	return stripZeros(lo) + "," + hiN, true
}

const REDupMax = 255

func withinDupMax(x string) bool {
	if len(strings.TrimLeft(x, "0")) > 3 {
		return false
	}
	return atoi(x) <= REDupMax
}

func stripZeros(x string) string {
	x = strings.TrimLeft(x, "0")
	if x == "" {
		return "0"
	}
	return x
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func escapeClassRune(r rune) string {
	switch r {
	case '\\', ']', '^', '-':
		return "\\" + string(r)
	}
	return string(r)
}

func translateBracket(s string) (string, int, error) {
	return translateBracketMode(s, false, nil)
}

func translateBracketMode(s string, cutf8 bool, tables *LocaleByteTables) (string, int, error) {
	if tables != nil && tables.tables.elements != nil {
		return unicodeElementBracket(s, tables)
	}
	var b strings.Builder
	b.WriteByte('[')
	i := 1
	if i < len(s) && s[i] == '^' {
		b.WriteByte('^')
		i++
	}
	first := true
	for i < len(s) {
		if s[i] == ']' && !first {
			b.WriteByte(']')
			cls := b.String()
			if _, err := regexp.Compile(cls); err != nil {
				return "", 0, fmt.Errorf("invalid bracket expression %q: %w", s[:i+1], err)
			}
			return cls, i + 1, nil
		}
		switch {
		case s[i] == '[' && i+1 < len(s) && s[i+1] == ':':
			end := strings.Index(s[i:], ":]")
			if end < 0 {
				return "", 0, fmt.Errorf("unmatched [: in bracket expression")
			}
			name := s[i+2 : i+end]
			if cutf8 {
				content, ok := locale.CUTF8RE2ClassContent(name)
				if !ok {
					return "", 0, fmt.Errorf("unsupported named character class %q", name)
				}
				b.WriteString(content)
			} else {
				b.WriteString(s[i : i+end+2])
			}
			i += end + 2
		case s[i] == '[' && i+1 < len(s) && (s[i+1] == '.' || s[i+1] == '='):

			delim := s[i+1]
			end := strings.Index(s[i+2:], string(delim)+"]")
			if end < 0 {
				return "", 0, fmt.Errorf("unmatched [%c in bracket expression", delim)
			}
			content := s[i+2 : i+2+end]
			if content == "" {
				return "", 0, fmt.Errorf("empty %s", map[byte]string{'.': "collating symbol [..]", '=': "equivalence class [==]"}[delim])
			}
			r, size := utf8.DecodeRuneInString(content)
			if size != len(content) || (r == utf8.RuneError && size == 1) {
				return "", 0, fmt.Errorf("invalid collating element %q in C locale", content)
			}
			if cutf8 && delim == '=' && tables != nil && r <= 0xff {
				for value, member := range tables.tables.equivalent[byte(r)] {
					if member {
						b.WriteString(escapeClassRune(rune(value)))
					}
				}
			} else {
				b.WriteString(escapeClassRune(r))
			}
			i += 2 + end + 2
		case s[i] == '\\':

			b.WriteString(`\\`)
			i++
		case s[i] == ']':

			b.WriteString(`\]`)
			i++
		default:
			b.WriteByte(s[i])
			i++
		}
		first = false
	}
	return "", 0, fmt.Errorf("unmatched [")
}

func ToGoERE(p string) (string, error) {
	return toGoERE(p, false, nil)
}

func toGoERE(p string, cutf8 bool, tables *LocaleByteTables) (string, error) {
	var b strings.Builder
	state := posStart
	for i := 0; i < len(p); {
		if p[i] == '[' {
			cls, n, err := translateBracketMode(p[i:], cutf8, tables)
			if err != nil {
				return "", err
			}
			b.WriteString(cls)
			state = posAtom
			i += n
			continue
		}
		if p[i] != '\\' {
			switch p[i] {
			case '^':
				if state == posStart {
					b.WriteByte('^')
					state = posAnchor
				} else {
					b.WriteString(`\^`)
					state = posAtom
				}
			case '$':
				anchor := i == len(p)-1 || (i+1 < len(p) && (p[i+1] == ')' || p[i+1] == '|'))
				if anchor {
					b.WriteByte('$')
					state = posAnchor
				} else {
					b.WriteString(`\$`)
					state = posAtom
				}
			case '(', '|':
				b.WriteByte(p[i])
				state = posStart
			case ')':
				b.WriteByte(p[i])
				state = posAtom
			case '{':
				if state != posAtom {
					return "", fmt.Errorf("{ with nothing to repeat")
				}
				end := strings.IndexByte(p[i+1:], '}')
				if end < 0 {
					return "", fmt.Errorf("unmatched {")
				}
				inner := p[i+1 : i+1+end]
				norm, ok := normalizeInterval(inner)
				if !ok {
					return "", fmt.Errorf("invalid interval {%s}", inner)
				}
				b.WriteByte('{')
				b.WriteString(norm)
				b.WriteByte('}')
				state = posAtom
				i += end + 2
				continue
			default:
				b.WriteByte(p[i])
				if p[i] != '*' && p[i] != '+' && p[i] != '?' && p[i] != '}' {
					state = posAtom
				}
			}
			i++
			continue
		}
		if i+1 >= len(p) {
			return "", fmt.Errorf("trailing backslash (\\)")
		}
		n := p[i+1]
		if n >= '1' && n <= '9' {
			return "", fmt.Errorf("back-reference \\%c is not supported (RE2 has no back-references)", n)
		}
		if n == '<' || n == '>' {
			b.WriteString(`\b`)
			state = posAnchor
			i += 2
			continue
		}
		b.WriteByte('\\')
		b.WriteByte(n)
		state = posAtom
		i += 2
	}
	return b.String(), nil
}

func ValidateERE(p string) error {
	_, err := ToGoERE(p)
	return err
}

func isAlnumByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
