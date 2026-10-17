package lexer

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

type Lexer struct {
	src      []byte
	offset   int
	ch       byte
	pos      Position
	nextPos  Position
	hadSpace bool
	lastTok  Token
}

type Position struct {
	Line int

	Column int
}

func (p Position) String() string {
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

func NewLexer(src []byte) *Lexer {
	l := &Lexer{src: src}
	l.nextPos.Line = 1
	l.nextPos.Column = 1
	l.next()
	return l
}

func (l *Lexer) HadSpace() bool {
	return l.hadSpace
}

func (l *Lexer) Scan() (Position, Token, string) {
	pos, tok, val := l.scan()
	l.lastTok = tok
	return pos, tok, val
}

func (l *Lexer) scan() (Position, Token, string) {

	l.hadSpace = false
	for l.ch == ' ' || l.ch == '\t' || l.ch == '\r' || l.ch == '\\' {
		l.hadSpace = true
		if l.ch == '\\' {
			l.next()
			if l.ch == '\r' {
				l.next()
			}
			if l.ch != '\n' {
				return l.pos, ILLEGAL, "expected \\n after \\ line continuation"
			}
		}
		l.next()
	}
	if l.ch == '#' {

		l.next()
		for l.ch != '\n' && l.ch != 0 {
			l.next()
		}
	}
	if l.ch == 0 {

		return l.pos, EOF, ""
	}

	pos := l.pos
	var tok Token
	val := ""

	ch := l.ch
	l.next()

	if isNameStart(ch) {
		start := l.offset - 2
		for isNameStart(l.ch) || isDigit(l.ch) {
			l.next()
		}
		name := string(l.src[start : l.offset-1])
		tok := KeywordToken(name)
		if tok == ILLEGAL {
			tok = NAME
			val = name
		}
		return pos, tok, val
	}

	switch ch {
	case '$':
		tok = DOLLAR
	case '@':
		tok = AT
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.':

		if ch == '0' && (l.ch == 'x' || l.ch == 'X') {
			return pos, NUMBER, "0"
		}

		start := l.offset - 2
		gotDigit := false
		if ch != '.' {
			gotDigit = true
			for isDigit(l.ch) {
				l.next()
			}
			if l.ch == '.' {
				l.next()
			}
		}
		for isDigit(l.ch) {
			gotDigit = true
			l.next()
		}
		if !gotDigit {
			return l.pos, ILLEGAL, "expected digits"
		}
		if l.ch == 'e' || l.ch == 'E' {
			l.next()
			gotSign := false
			if l.ch == '+' || l.ch == '-' {
				gotSign = true
				l.next()
			}
			gotDigit = false
			for isDigit(l.ch) {
				l.next()
				gotDigit = true
			}

			if !gotDigit {
				if gotSign {
					l.unread()
				}
				l.unread()
			}
		}
		tok = NUMBER
		val = string(l.src[start : l.offset-1])
	case '{':
		tok = LBRACE
	case '}':
		tok = RBRACE
	case '=':
		tok = l.choice('=', ASSIGN, EQUALS)
	case '<':
		tok = l.choice('=', LESS, LTE)
	case '>':
		switch l.ch {
		case '=':
			l.next()
			tok = GTE
		case '>':
			l.next()
			tok = APPEND
		default:
			tok = GREATER
		}
	case '"', '\'':

		s, err := parseString(ch, func() byte { return l.ch }, l.next)
		if err != nil {
			return l.pos, ILLEGAL, err.Error()
		}
		if l.ch != ch {
			return l.pos, ILLEGAL, "didn't find end quote in string"
		}
		l.next()
		tok = STRING
		val = s
	case '(':
		tok = LPAREN
	case ')':
		tok = RPAREN
	case ',':
		tok = COMMA
	case ';':
		tok = SEMICOLON
	case '+':
		switch l.ch {
		case '+':
			l.next()
			tok = INCR
		case '=':
			l.next()
			tok = ADD_ASSIGN
		default:
			tok = ADD
		}
	case '-':
		switch l.ch {
		case '-':
			l.next()
			tok = DECR
		case '=':
			l.next()
			tok = SUB_ASSIGN
		default:
			tok = SUB
		}
	case '*':
		switch l.ch {
		case '*':
			l.next()
			tok = l.choice('=', POW, POW_ASSIGN)
		case '=':
			l.next()
			tok = MUL_ASSIGN
		default:
			tok = MUL
		}
	case '/':
		tok = l.choice('=', DIV, DIV_ASSIGN)
	case '%':
		tok = l.choice('=', MOD, MOD_ASSIGN)
	case '[':
		tok = LBRACKET
	case ']':
		tok = RBRACKET
	case '\n':
		tok = NEWLINE
	case '^':
		tok = l.choice('=', POW, POW_ASSIGN)
	case '!':
		switch l.ch {
		case '=':
			l.next()
			tok = NOT_EQUALS
		case '~':
			l.next()
			tok = NOT_MATCH
		default:
			tok = NOT
		}
	case '~':
		tok = MATCH
	case '?':
		tok = QUESTION
	case ':':
		tok = COLON
	case '&':
		tok = l.choice('&', ILLEGAL, AND)
		if tok == ILLEGAL {
			return l.pos, ILLEGAL, "unexpected char after '&'"
		}
	case '|':
		tok = l.choice('|', PIPE, OR)
	default:
		tok = ILLEGAL
		val = "unexpected char"
	}
	return pos, tok, val
}

func (l *Lexer) ScanRegex() (Position, Token, string) {
	pos, tok, val := l.scanRegex()
	l.lastTok = tok
	return pos, tok, val
}

func (l *Lexer) scanRegex() (Position, Token, string) {
	pos := l.pos
	chars := make([]byte, 0, 32)
	inBracket := false
	switch l.lastTok {
	case DIV:

		pos.Column -= 1
	case DIV_ASSIGN:

		pos.Column -= 2
		chars = append(chars, '=')
	default:
		panic("ScanRegex should only be called after DIV or DIV_ASSIGN token")
	}
	for l.ch != '/' {
		c := l.ch
		if c == 0 {
			return l.pos, ILLEGAL, "didn't find end slash in regex"
		}
		if c == '\r' || c == '\n' {
			return l.pos, ILLEGAL, "can't have newline in regex"
		}
		if c == '\\' {
			l.next()
			switch l.ch {
			case '"', '/':
				c = l.ch
				l.next()
			case '\\':
				chars = appendRegexLiteralByte(chars, l.ch, inBracket)
				l.next()
				continue
			case 'a':
				c = '\a'
				l.next()
			case 'b':
				c = '\b'
				l.next()
			case 'f':
				c = '\f'
				l.next()
			case 'n':
				c = '\n'
				l.next()
			case 'r':
				c = '\r'
				l.next()
			case 't':
				c = '\t'
				l.next()
			case 'v':
				c = '\v'
				l.next()
			case '0', '1', '2', '3', '4', '5', '6', '7':
				c = 0
				for digits := 0; digits < 3 && l.ch >= '0' && l.ch <= '7'; digits++ {
					c = c*8 + l.ch - '0'
					l.next()
				}
			default:

				chars = append(chars, '\\')
				c = l.ch
				l.next()
				chars = append(chars, c)
				continue
			}
			chars = appendRegexLiteralByte(chars, c, inBracket)
			continue
		}
		chars = append(chars, c)
		if c == '[' && !inBracket {
			inBracket = true
		} else if c == ']' && inBracket {
			inBracket = false
		}
		l.next()
	}
	l.next()
	return pos, REGEX, string(chars)
}

func appendRegexLiteralByte(dst []byte, b byte, inBracket bool) []byte {
	meta := ".^$[\\()*+?{|"
	if inBracket {
		meta = "\\]-^"
	}
	for i := 0; i < len(meta); i++ {
		if b == meta[i] {
			dst = append(dst, '\\')
			break
		}
	}
	return append(dst, b)
}

func (l *Lexer) next() {
	l.pos = l.nextPos
	if l.offset >= len(l.src) {

		if l.ch != 0 {
			l.ch = 0
			l.offset++
			l.nextPos.Column++
		}
		return
	}
	ch := l.src[l.offset]
	if ch == '\n' {
		l.nextPos.Line++
		l.nextPos.Column = 1
	} else if ch != '\r' {
		l.nextPos.Column++
	}
	l.ch = ch
	l.offset++
}

func (l *Lexer) unread() {
	l.offset--
	l.pos.Column--
	l.nextPos.Column--
	l.ch = l.src[l.offset-1]
}

func isNameStart(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func hexDigit(ch byte) byte {
	switch {
	case isDigit(ch):
		return ch - '0'
	case ch >= 'a' && ch <= 'f':
		return ch - 'a' + 10
	case ch >= 'A' && ch <= 'F':
		return ch - 'A' + 10
	default:
		return 0xff
	}
}

func (l *Lexer) choice(ch byte, one, two Token) Token {
	if l.ch == ch {
		l.next()
		return two
	}
	return one
}

func (l *Lexer) PeekByte() byte {
	return l.ch
}

func Unescape(s string) (string, error) {
	i := 0
	ch := func() byte {
		if i >= len(s) {
			return 0
		}
		return s[i]
	}
	next := func() {
		i++
	}
	return parseString(0, ch, next)
}

func parseString(quote byte, ch func() byte, next func()) (string, error) {
	chars := make([]byte, 0, 32)
	for {
		c := ch()
		if c == quote || c == 0 {
			break
		}
		if c == '\r' || c == '\n' {
			return "", errors.New("can't have newline in string")
		}
		if c != '\\' {

			chars = append(chars, c)
			next()
			continue
		}

		next()
		switch ch() {
		case 'n':
			c = '\n'
			next()
		case 't':
			c = '\t'
			next()
		case 'r':
			c = '\r'
			next()
		case 'a':
			c = '\a'
			next()
		case 'b':
			c = '\b'
			next()
		case 'f':
			c = '\f'
			next()
		case 'v':
			c = '\v'
			next()
		case 'x':

			next()
			digit := hexDigit(ch())
			if digit > 15 {
				return "", errors.New("1 or 2 hex digits expected")
			}
			c = digit
			next()
			digit = hexDigit(ch())
			if digit <= 15 {
				c = c*16 + digit
				next()
			}
		case 'u':

			next()
			digit := hexDigit(ch())
			if digit > 15 {
				return "", errors.New("1-8 hex digits expected")
			}
			r := uint64(digit)
			next()
			for i := 0; i < 7; i++ {
				digit = hexDigit(ch())
				if digit > 15 {
					break
				}
				next()
				r = r*16 + uint64(digit)
			}
			if r > utf8.MaxRune || !utf8.ValidRune(rune(r)) {
				return "", errors.New("invalid Unicode character")
			}
			runeBytes := make([]byte, utf8.UTFMax)
			n := utf8.EncodeRune(runeBytes, rune(r))
			chars = append(chars, runeBytes[:n]...)
			continue
		case '0', '1', '2', '3', '4', '5', '6', '7':

			c = ch() - '0'
			next()
			for i := 0; i < 2 && ch() >= '0' && ch() <= '7'; i++ {
				c = c*8 + ch() - '0'
				next()
			}
		default:

			c = ch()
			if c == 0 {

				c = '\\'
			}
			next()
		}
		chars = append(chars, c)
	}
	return string(chars), nil
}
