package xargs

import (
	"errors"
	"io"
	"strings"
)

const maxInputBytes = 4 << 20

type inputItem struct {
	value string
	line  int
}

func readItems(r io.Reader, o options) ([]inputItem, error) {
	if r == nil {
		r = strings.NewReader("")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxInputBytes {
		return nil, errors.New("标准输入超过 4 MiB 预算")
	}
	var items []inputItem
	switch {
	case o.replaceSet:
		lines := strings.Split(string(data), "\n")
		for line, text := range lines {
			if line == len(lines)-1 && text == "" {
				continue
			}
			value, parseErr := unquoteReplacementLine(strings.TrimSuffix(text, "\r"))
			if parseErr != nil {
				return nil, parseErr
			}
			items = append(items, inputItem{value: value, line: line + 1})
		}
	case o.null:
		items = plainItems(splitOn(string(data), '\x00'))
	case o.delim != "":
		items = plainItems(splitOn(string(data), unescapeDelim(o.delim)))
	default:
		items, err = splitQuoted(string(data), o.maxLines > 0)
		if err != nil {
			return nil, err
		}
	}
	if o.eof != "" {
		for k, it := range items {
			if it.value == o.eof {
				return items[:k], nil
			}
		}
	}
	return items, nil
}

func unquoteReplacementLine(text string) (string, error) {
	start := 0
	for start < len(text) && (text[start] == ' ' || text[start] == '\t') {
		start++
	}
	text = text[start:]
	var out strings.Builder
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				out.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\\':
			if i+1 == len(text) {
				return "", errors.New("未配对的转义结尾")
			}
			i++
			out.WriteByte(text[i])
		case '\'', '"':
			quote = c
		default:
			out.WriteByte(c)
		}
	}
	switch quote {
	case '\'':
		return "", errors.New("未配对的单引号")
	case '"':
		return "", errors.New("未配对的双引号")
	}
	return out.String(), nil
}

func plainItems(values []string) []inputItem {
	items := make([]inputItem, len(values))
	for i, value := range values {
		items[i] = inputItem{value: value, line: i + 1}
	}
	return items
}

func splitOn(s string, delim rune) []string {
	var out []string
	for part := range strings.SplitSeq(s, string(delim)) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitQuoted(s string, useContinuation bool) ([]inputItem, error) {
	var lineMap map[int]int
	if useContinuation {
		lineMap = computeLogicalLines(s)
	}
	var items []inputItem
	var current strings.Builder
	inItem := false
	line, itemLine := 1, 1
	flush := func() {
		if !inItem {
			return
		}
		value := itemLine
		if mapped, ok := lineMap[itemLine]; ok {
			value = mapped
		}
		items = append(items, inputItem{value: current.String(), line: value})
		current.Reset()
		inItem = false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\r', '\v', '\f':
			flush()
		case '\n':
			flush()
			line++
			itemLine = line
		case '\\':
			if i+1 == len(s) {
				return nil, errors.New("未配对的转义结尾")
			}
			i++
			current.WriteByte(s[i])
			inItem = true
		case '\'':
			inItem = true
			for i++; i < len(s) && s[i] != '\''; i++ {
				current.WriteByte(s[i])
			}
			if i == len(s) {
				return nil, errors.New("未配对的单引号")
			}
		case '"':
			inItem = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				current.WriteByte(s[i])
			}
			if i == len(s) {
				return nil, errors.New("未配对的双引号")
			}
		default:
			current.WriteByte(c)
			inItem = true
		}
	}
	flush()
	return items, nil
}

func computeLogicalLines(s string) map[int]int {
	parts := strings.Split(s, "\n")
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	m := make(map[int]int, len(parts))
	logicalLine := 0
	inContinuation := false
	for i, part := range parts {
		physical := i + 1
		isEmpty, endsWithUnescapedBlank := analyzePhysicalLine(part)
		if isEmpty {
			if logicalLine == 0 {
				logicalLine = 1
			}
			m[physical] = logicalLine
			continue
		}
		if !inContinuation {
			logicalLine++
		}
		m[physical] = logicalLine
		inContinuation = endsWithUnescapedBlank
	}
	return m
}

func analyzePhysicalLine(line string) (isEmpty bool, endsWithUnescapedBlank bool) {
	inSingle, inDouble, escaped := false, false, false
	nonBlankSeen := false
	line = strings.TrimSuffix(line, "\r")
	var lastIsBlank, lastIsEscaped, lastIsQuoted bool
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			nonBlankSeen = true
			lastIsBlank = c == ' ' || c == '\t'
			lastIsEscaped = true
			lastIsQuoted = inSingle || inDouble
			continue
		}
		if inSingle || inDouble {
			if (inSingle && c == '\'') || (inDouble && c == '"') {
				inSingle, inDouble = false, false
				continue
			}
			nonBlankSeen = true
			lastIsBlank = c == ' ' || c == '\t'
			lastIsEscaped = false
			lastIsQuoted = true
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '\'', '"':
			nonBlankSeen = true
			inSingle, inDouble = c == '\'', c == '"'
		default:
			if c != ' ' && c != '\t' {
				nonBlankSeen = true
			}
			lastIsBlank = c == ' ' || c == '\t'
			lastIsEscaped = false
			lastIsQuoted = false
		}
	}
	return !nonBlankSeen, lastIsBlank && !lastIsEscaped && !lastIsQuoted
}

func unescapeDelim(s string) rune {
	if len(s) >= 2 && s[0] == '\\' {
		switch s[1] {
		case 'n':
			return '\n'
		case 't':
			return '\t'
		case 'r':
			return '\r'
		case '0':
			return '\x00'
		case '\\':
			return '\\'
		}
	}
	if r := []rune(s); len(r) > 0 {
		return r[0]
	}
	return '\n'
}
