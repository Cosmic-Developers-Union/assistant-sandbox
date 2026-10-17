package ctags

import (
	"strings"
)

type tag struct {
	name string
	file string
	line int
	text string
}

var keywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true,
	"return": true, "sizeof": true, "do": true, "else": true, "goto": true,
}

func cleanLines(source string) []string {
	lines := strings.Split(source, "\n")
	out := make([]string, len(lines))
	const (
		codeState = iota
		blockCommentState
		stringState
		charState
	)
	state := codeState
	for i, line := range lines {
		var b strings.Builder
		for j := 0; j < len(line); {
			c := line[j]
			switch state {
			case blockCommentState:
				if c == '*' && j+1 < len(line) && line[j+1] == '/' {
					state = codeState
					b.WriteString("  ")
					j += 2
				} else {
					b.WriteByte(' ')
					j++
				}
			case stringState:
				switch {
				case c == '\\' && j+1 < len(line):
					b.WriteString("  ")
					j += 2
				case c == '"':
					state = codeState
					b.WriteByte(' ')
					j++
				default:
					b.WriteByte(' ')
					j++
				}
			case charState:
				switch {
				case c == '\\' && j+1 < len(line):
					b.WriteString("  ")
					j += 2
				case c == '\'':
					state = codeState
					b.WriteByte(' ')
					j++
				default:
					b.WriteByte(' ')
					j++
				}
			default:
				switch {
				case c == '/' && j+1 < len(line) && line[j+1] == '*':
					state = blockCommentState
					b.WriteString("  ")
					j += 2
				case c == '/' && j+1 < len(line) && line[j+1] == '/':

					for ; j < len(line); j++ {
						b.WriteByte(' ')
					}
				case c == '"':
					state = stringState
					b.WriteByte(' ')
					j++
				case c == '\'':
					state = charState
					b.WriteByte(' ')
					j++
				default:
					b.WriteByte(c)
					j++
				}
			}
		}
		out[i] = b.String()
	}
	return out
}

func scanFunctions(clean []string, visit func(name string, lineNo int)) {
	for i, line := range clean {
		if line == "" || !isIdentStart(line[0]) {
			continue
		}
		if first, _ := scanIdent(line, 0); first == "typedef" {
			continue
		}
		paren := strings.IndexByte(line, '(')
		if paren < 0 {
			continue
		}

		end := paren
		for end > 0 && (line[end-1] == ' ' || line[end-1] == '\t') {
			end--
		}
		start := end
		for start > 0 && isIdentChar(line[start-1]) {
			start--
		}
		if start == end || !isIdentStart(line[start]) {
			continue
		}
		name := line[start:end]
		if keywords[name] {
			continue
		}
		closed, balanced := matchParens(line, paren)
		if !balanced {
			continue
		}
		if rest := strings.TrimLeft(line[closed+1:], " \t"); strings.HasPrefix(rest, ";") {

			continue
		}
		visit(name, i+1)
	}
}

func matchParens(line string, start int) (closed int, ok bool) {
	depth := 0
	closed = -1
	for j := start; j < len(line); j++ {
		switch line[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && closed < 0 {
				closed = j
			}
			if depth < 0 {
				return -1, false
			}
		}
	}
	if depth != 0 || closed < 0 {
		return -1, false
	}
	return closed, true
}

func scanTypedefs(clean []string, visit func(name string, lineNo int)) {
	inTypedef := false
	depth := 0
	var accum strings.Builder
	for i, line := range clean {
		if !inTypedef {
			pos := indexWord(line, "typedef")
			if pos < 0 {
				continue
			}
			inTypedef = true
			depth = 0
			accum.Reset()
			line = line[pos:]
		}
		done := false
		for j := 0; j < len(line); j++ {
			switch c := line[j]; {
			case c == '{':
				depth++
				accum.WriteByte(' ')
			case c == '}':
				if depth > 0 {
					depth--
				}
				accum.WriteByte(' ')
			case c == ';' && depth == 0:
				if name := lastIdentifier(accum.String()); name != "" {
					visit(name, i+1)
				}
				done = true
			default:
				if isIdentChar(c) {
					accum.WriteByte(c)
				} else {
					accum.WriteByte(' ')
				}
			}
			if done {
				break
			}
		}
		if done {
			inTypedef = false
		}
	}
}

func indexWord(line, word string) int {
	for pos := 0; pos+len(word) <= len(line); pos++ {
		if line[pos:pos+len(word)] != word {
			continue
		}
		if pos > 0 && isIdentChar(line[pos-1]) {
			continue
		}
		if end := pos + len(word); end < len(line) && isIdentChar(line[end]) {
			continue
		}
		return pos
	}
	return -1
}

func lastIdentifier(text string) string {
	end := len(text)
	for end > 0 && !isIdentChar(text[end-1]) {
		end--
	}
	start := end
	for start > 0 && isIdentChar(text[start-1]) {
		start--
	}
	if start >= end || !isIdentStart(text[start]) {
		return ""
	}
	return text[start:end]
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

func scanIdent(line string, start int) (string, int) {
	end := start
	for end < len(line) && isIdentChar(line[end]) {
		end++
	}
	if end == start {
		return "", start
	}
	return line[start:end], end
}

func escapePattern(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' || text[i] == '/' {
			b.WriteByte('\\')
		}
		b.WriteByte(text[i])
	}
	return b.String()
}
