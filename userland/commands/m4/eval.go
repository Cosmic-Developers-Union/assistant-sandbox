package m4

func evalExpr(s string) (v int32, errmsg string) {
	if len(s) == 0 {
		return 0, ""
	}
	e := &evaluator{s: s}
	v = e.binary(0, true)
	if e.err == "" {
		e.space()
		if e.pos < len(e.s) {
			e.err = "表达式不合法"
		}
	}
	return v, e.err
}

type evaluator struct {
	s     string
	pos   int
	err   string
	depth int
}

func (e *evaluator) fail(msg string) {
	if e.err == "" {
		e.err = msg
	}
}

func (e *evaluator) space() {
	for e.pos < len(e.s) && isSpace(e.s[e.pos]) {
		e.pos++
	}
}

var binaryOps = [][]string{
	{"||"},
	{"&&"},
	{"|"},
	{"^"},
	{"&"},
	{"==", "!="},
	{"<=", ">=", "<", ">"},
	{"<<", ">>"},
	{"+", "-"},
	{"*", "/", "%"},
}

func (e *evaluator) operator(level int) string {
	e.space()
	rest := e.s[e.pos:]
	for _, op := range binaryOps[level] {
		if len(rest) < len(op) || rest[:len(op)] != op {
			continue
		}
		if len(op) == 1 && len(rest) > 1 {
			switch rest[:2] {
			case "||", "&&", "<<", ">>", "<=", ">=", "==", "!=":
				return ""
			}
		}
		e.pos += len(op)
		return op
	}
	return ""
}

func (e *evaluator) binary(level int, live bool) int32 {
	if level == len(binaryOps) {
		return e.unary(live)
	}
	l := e.binary(level+1, live)
	for e.err == "" {
		op := e.operator(level)
		if op == "" {
			break
		}
		switch op {
		case "||":
			r := e.binary(level+1, live && l == 0)
			l = truth(l != 0 || r != 0)
			continue
		case "&&":
			r := e.binary(level+1, live && l != 0)
			l = truth(l != 0 && r != 0)
			continue
		}
		r := e.binary(level+1, live)
		if e.err != "" {
			break
		}
		switch op {
		case "|":
			l |= r
		case "^":
			l ^= r
		case "&":
			l &= r
		case "==":
			l = truth(l == r)
		case "!=":
			l = truth(l != r)
		case "<":
			l = truth(l < r)
		case "<=":
			l = truth(l <= r)
		case ">":
			l = truth(l > r)
		case ">=":
			l = truth(l >= r)
		case "<<":
			l = int32(uint32(l) << (uint32(r) & 31))
		case ">>":
			l >>= uint32(r) & 31
		case "+":
			l += r
		case "-":
			l -= r
		case "*":
			l *= r
		case "/", "%":
			switch {
			case r != 0 && op == "/":
				l /= r
			case r != 0:
				l %= r
			case live && op == "/":
				e.fail("除数为零")
			case live:
				e.fail("取模数为零")
			}
		}
	}
	return l
}

func truth(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

const maxEvalDepth = 4096

func (e *evaluator) unary(live bool) int32 {
	e.space()
	if e.pos == len(e.s) {
		e.fail("表达式不合法")
		return 0
	}
	switch c := e.s[e.pos]; c {
	case '+':
		e.pos++
		return e.unary(live)
	case '-':
		e.pos++
		return -e.unary(live)
	case '~':
		e.pos++
		return ^e.unary(live)
	case '!':
		e.pos++
		return truth(e.unary(live) == 0)
	case '(':
		if e.depth++; e.depth > maxEvalDepth {
			e.fail("括号嵌套过深")
			return 0
		}
		defer func() { e.depth-- }()
		e.pos++
		v := e.binary(0, live)
		e.space()
		if e.pos == len(e.s) || e.s[e.pos] != ')' {
			e.fail("表达式不合法")
			return 0
		}
		e.pos++
		return v
	}
	return e.constant()
}

func (e *evaluator) constant() int32 {
	base := uint32(10)
	start := e.pos
	if e.s[e.pos] == '0' {
		base = 8
		if e.pos+1 < len(e.s) && (e.s[e.pos+1] == 'x' || e.s[e.pos+1] == 'X') {
			base = 16
			e.pos += 2
			start = e.pos
		}
	}
	var v uint32
	for ; e.pos < len(e.s); e.pos++ {
		c := e.s[e.pos]
		var d uint32
		switch {
		case c >= '0' && c <= '9':
			d = uint32(c - '0')
		case c >= 'a' && c <= 'z':
			d = uint32(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			d = uint32(c-'A') + 10
		default:
			d = 99
		}
		if d == 99 {
			break
		}
		if d >= base {
			e.fail("表达式不合法")
			return 0
		}
		v = v*base + d
	}
	if e.pos == start {
		e.fail("表达式不合法")
		return 0
	}
	return int32(v)
}
