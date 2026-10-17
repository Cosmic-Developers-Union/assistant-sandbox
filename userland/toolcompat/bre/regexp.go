package bre

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxBacktrackSteps = 200000

type MatchLimitError struct{ Limit int }

func (e *MatchLimitError) Error() string {
	return fmt.Sprintf("regular expression match exceeded resource limit (%d steps)", e.Limit)
}

func (e *MatchLimitError) Is(target error) bool { return target == ErrMatchLimit }

var ErrMatchLimit = errors.New("regular expression match resource limit exceeded")

type Regexp struct {
	re *regexp.Regexp
	bt *btProg
}

func CompileCUTF8WithFlags(pattern, flags string, extended bool, collation *LocaleByteTables) (*Regexp, error) {
	if (!extended && !needsBacktrack(pattern)) || (extended && !ERERequiresBacktracking(pattern)) {
		var translated string
		var err error
		if extended {
			translated, err = toGoERE(pattern, true, collation)
		} else {
			translated, err = toGo(pattern, true, collation)
		}
		if err != nil {
			return nil, err
		}
		re, err := regexp.Compile(flags + translated)
		if err != nil {
			return nil, err
		}
		return &Regexp{re: re}, nil
	}
	bt, err := compileBackrefMode(pattern, flags, extended, true, collation)
	if err != nil {
		return nil, err
	}
	return &Regexp{bt: bt}, nil
}

func Compile(pattern string) (*Regexp, error) {
	if !needsBacktrack(pattern) {
		t, err := ToGo(pattern)
		if err != nil {
			return nil, err
		}
		re, err := regexp.Compile(t)
		if err != nil {
			return nil, err
		}
		return &Regexp{re: re}, nil
	}
	bt, err := compileBackref(pattern, "", false)
	if err != nil {
		return nil, err
	}
	return &Regexp{bt: bt}, nil
}

func CompileWithFlags(pattern, flags string) (*Regexp, error) {
	if !needsBacktrack(pattern) {
		t, err := ToGo(pattern)
		if err != nil {
			return nil, err
		}
		re, err := regexp.Compile(flags + t)
		if err != nil {
			return nil, err
		}
		return &Regexp{re: re}, nil
	}
	bt, err := compileBackref(pattern, flags, false)
	if err != nil {
		return nil, err
	}
	return &Regexp{bt: bt}, nil
}

func CompileEREWithFlags(pattern, flags string) (*Regexp, error) {
	if !ERERequiresBacktracking(pattern) {
		t, err := ToGoERE(pattern)
		if err != nil {
			return nil, err
		}
		re, err := regexp.Compile(flags + t)
		if err != nil {
			return nil, err
		}
		return &Regexp{re: re}, nil
	}
	bt, err := compileBackref(pattern, flags, true)
	if err != nil {
		return nil, err
	}
	return &Regexp{bt: bt}, nil
}

func (r *Regexp) Longest() {
	if r.re != nil {
		r.re.Longest()
		return
	}
	r.bt.longest = true
}

func (r *Regexp) MatchString(s string) bool {
	matched, _ := r.MatchStringErr(s)
	return matched
}

func (r *Regexp) MatchStringErr(s string) (bool, error) {
	loc, err := r.FindStringIndexErr(s)
	return loc != nil, err
}

func (r *Regexp) FindStringIndex(s string) []int {
	loc, _ := r.FindStringIndexErr(s)
	return loc
}

func (r *Regexp) FindStringIndexErr(s string) ([]int, error) {
	if r.re != nil {
		return r.re.FindStringIndex(s), nil
	}
	m, err := r.bt.find(s, 1)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m[0][:2], nil
}

func (r *Regexp) FindAllStringSubmatchIndex(s string, n int) [][]int {
	matches, _ := r.FindAllStringSubmatchIndexErr(s, n)
	return matches
}

func (r *Regexp) FindAllStringSubmatchIndexErr(s string, n int) ([][]int, error) {
	if r.re != nil {
		return r.re.FindAllStringSubmatchIndex(s, n), nil
	}
	return r.bt.find(s, n)
}

func (r *Regexp) FindStringSubmatchIndex(s string) []int {
	matches, _ := r.FindStringSubmatchIndexErr(s)
	return matches
}

func (r *Regexp) FindStringSubmatchIndexErr(s string) ([]int, error) {
	if r.re != nil {
		return r.re.FindStringSubmatchIndex(s), nil
	}
	matches, err := r.bt.find(s, 1)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return matches[0], nil
}

func (r *Regexp) NumSubexp() int {
	if r.re != nil {
		return r.re.NumSubexp()
	}
	return r.bt.groups
}

func (r *Regexp) FindAllSubmatchIndex(b []byte, n int) [][]int {
	return r.FindAllStringSubmatchIndex(string(b), n)
}

func (r *Regexp) FindAllSubmatchIndexErr(b []byte, n int) ([][]int, error) {
	return r.FindAllStringSubmatchIndexErr(string(b), n)
}

func (r *Regexp) ExpandString(dst []byte, template, src string, match []int) []byte {
	return expandTemplate(dst, template, src, match)
}

func (r *Regexp) Expand(dst []byte, template []byte, src []byte, match []int) []byte {
	return expandTemplate(dst, string(template), string(src), match)
}

func needsBacktrack(p string) bool {
	for i := 0; i+1 < len(p); i++ {
		if p[i] == '\\' {
			n := p[i+1]
			if (n >= '1' && n <= '9') || n == '<' || n == '>' {
				return true
			}
			i++
		}
	}
	return false
}

func ERERequiresBacktracking(p string) bool {
	state := posStart
	for i := 0; i < len(p); {
		if p[i] == '[' {
			_, n, err := translateBracket(p[i:])
			if err != nil {
				return false
			}
			state = posAtom
			i += n
			continue
		}
		if p[i] == '\\' {
			if i+1 >= len(p) {
				return false
			}
			n := p[i+1]
			if n >= '1' && n <= '9' {
				return true
			}
			if n == '<' || n == '>' || n == 'b' || n == 'B' {
				state = posAnchor
			} else {
				state = posAtom
			}
			i += 2
			continue
		}
		switch p[i] {
		case '^':
			if state == posStart {
				state = posAnchor
			} else {
				state = posAtom
			}
		case '$':
			if i == len(p)-1 || (i+1 < len(p) && (p[i+1] == ')' || p[i+1] == '|')) {
				state = posAnchor
			} else {
				state = posAtom
			}
		case '(':
			if i+2 < len(p) && p[i+1] == '?' && p[i+2] == ':' {
				i += 3
				state = posStart
				continue
			}
			state = posStart
		case '|':
			state = posStart
		case ')':
			state = posAtom
		case '*', '+', '?':
			if state != posAtom {
				return false
			}
			state = posAnchor
		case '{':
			if state != posAtom {
				return false
			}
			end := strings.IndexByte(p[i+1:], '}')
			if end < 0 {
				return false
			}
			inner := p[i+1 : i+1+end]
			if _, ok := normalizeInterval(inner); !ok {
				return false
			}
			state = posAnchor
			i += end + 2
			continue
		default:
			if p[i] != '}' {
				state = posAtom
			}
		}
		i++
	}
	return false
}

func expandTemplate(dst []byte, template, src string, match []int) []byte {
	for i := 0; i < len(template); i++ {
		if template[i] != '$' {
			dst = append(dst, template[i])
			continue
		}
		if i+1 < len(template) && template[i+1] == '$' {
			dst = append(dst, '$')
			i++
			continue
		}
		if i+1 >= len(template) || template[i+1] != '{' {
			dst = append(dst, '$')
			continue
		}
		j := i + 2
		for j < len(template) && template[j] >= '0' && template[j] <= '9' {
			j++
		}
		if j == i+2 || j >= len(template) || template[j] != '}' {
			dst = append(dst, '$')
			continue
		}
		n, _ := strconv.Atoi(template[i+2 : j])
		if 2*n+1 < len(match) && match[2*n] >= 0 && match[2*n+1] >= 0 {
			dst = append(dst, src[match[2*n]:match[2*n+1]]...)
		}
		i = j
	}
	return dst
}

type btProg struct {
	root       btNode
	groups     int
	anchored   bool
	ignoreCase bool
	longest    bool
	cutf8      bool
}

type btState struct {
	pos  int
	caps [20]int
}

type btCtx struct {
	s     string
	steps int
	limit int
}

type btNode interface {
	match(*btCtx, btState) []btState
}

type seqNode []btNode

func (n seqNode) match(ctx *btCtx, st btState) []btState {
	states := []btState{st}
	for _, child := range n {
		var next []btState
		for _, s := range states {
			next = append(next, child.match(ctx, s)...)
			if ctx.steps > ctx.limit {
				return nil
			}
		}
		if len(next) == 0 {
			return nil
		}
		states = next
	}
	return states
}

type altNode []btNode

func (n altNode) match(ctx *btCtx, st btState) []btState {
	var out []btState
	for _, child := range n {
		out = append(out, child.match(ctx, st)...)
		if ctx.steps > ctx.limit {
			return nil
		}
	}
	return out
}

type literalNode struct {
	lit        string
	ignoreCase bool
}

func (n literalNode) match(ctx *btCtx, st btState) []btState {
	ctx.steps++
	if st.pos+len(n.lit) > len(ctx.s) {
		return nil
	}
	got := ctx.s[st.pos : st.pos+len(n.lit)]
	if (n.ignoreCase && strings.EqualFold(got, n.lit)) || (!n.ignoreCase && got == n.lit) {
		st.pos += len(n.lit)
		return []btState{st}
	}
	return nil
}

type dotNode struct {
	dotAll bool
	cutf8  bool
}

func (n dotNode) match(ctx *btCtx, st btState) []btState {
	ctx.steps++
	if st.pos >= len(ctx.s) || (!n.dotAll && ctx.s[st.pos] == '\n') {
		return nil
	}
	if n.cutf8 {
		_, size := utf8.DecodeRuneInString(ctx.s[st.pos:])
		st.pos += size
	} else {
		st.pos++
	}
	return []btState{st}
}

type anchorNode byte

func (n anchorNode) match(ctx *btCtx, st btState) []btState {
	ctx.steps++
	switch byte(n) {
	case '^':
		if st.pos == 0 {
			return []btState{st}
		}
	case '$':
		if st.pos == len(ctx.s) {
			return []btState{st}
		}
	}
	return nil
}

type wordEdgeNode byte

func (n wordEdgeNode) match(ctx *btCtx, st btState) []btState {
	ctx.steps++
	switch byte(n) {
	case '<':
		if st.pos < len(ctx.s) && isWordByte(ctx.s[st.pos]) &&
			(st.pos == 0 || !isWordByte(ctx.s[st.pos-1])) {
			return []btState{st}
		}
	case '>':
		if st.pos > 0 && isWordByte(ctx.s[st.pos-1]) &&
			(st.pos == len(ctx.s) || !isWordByte(ctx.s[st.pos])) {
			return []btState{st}
		}
	}
	return nil
}

type wordBoundaryNode byte

func (n wordBoundaryNode) match(ctx *btCtx, st btState) []btState {
	ctx.steps++
	left := st.pos > 0 && isWordByte(ctx.s[st.pos-1])
	right := st.pos < len(ctx.s) && isWordByte(ctx.s[st.pos])
	if (byte(n) == 'b') == (left != right) {
		return []btState{st}
	}
	return nil
}

type builtinClassNode byte

func (n builtinClassNode) match(ctx *btCtx, st btState) []btState {
	ctx.steps++
	if st.pos >= len(ctx.s) {
		return nil
	}
	c := ctx.s[st.pos]
	var ok bool
	switch byte(n) {
	case 'w', 'W':
		ok = isWordByte(c)
	case 's', 'S':
		ok = c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
	}
	if byte(n) == 'W' || byte(n) == 'S' {
		ok = !ok
	}
	if ok {
		st.pos++
		return []btState{st}
	}
	return nil
}

type classNode struct {
	maxElementBytes int
	re              *regexp.Regexp
	cutf8           bool
}

func (n classNode) match(ctx *btCtx, st btState) []btState {
	ctx.steps++
	if st.pos >= len(ctx.s) {
		return nil
	}
	if n.maxElementBytes > 0 {
		var states []btState
		for end := st.pos; end < len(ctx.s) && end-st.pos < n.maxElementBytes; {
			_, size := utf8.DecodeRuneInString(ctx.s[end:])
			end += size
			ctx.steps++
			if ctx.steps > ctx.limit {
				return nil
			}
			if n.re.MatchString(ctx.s[st.pos:end]) {
				next := st
				next.pos = end
				states = append(states, next)
			}
		}

		for i, j := 0, len(states)-1; i < j; i, j = i+1, j-1 {
			states[i], states[j] = states[j], states[i]
		}
		return states
	}
	size := 1
	if n.cutf8 {
		_, size = utf8.DecodeRuneInString(ctx.s[st.pos:])
	}
	if n.re.MatchString(ctx.s[st.pos : st.pos+size]) {
		st.pos += size
		return []btState{st}
	}
	return nil
}

type groupNode struct {
	num   int
	child btNode
}

func (n groupNode) match(ctx *btCtx, st btState) []btState {
	outs := n.child.match(ctx, st)
	for i := range outs {
		outs[i].caps[2*n.num] = st.pos
		outs[i].caps[2*n.num+1] = outs[i].pos
	}
	return outs
}

type backrefNode struct {
	num        int
	ignoreCase bool
}

func (n backrefNode) match(ctx *btCtx, st btState) []btState {
	ctx.steps++
	s, e := st.caps[2*n.num], st.caps[2*n.num+1]
	if s < 0 || e < 0 {

		return nil
	}
	if e == s {

		return []btState{st}
	}
	lit := ctx.s[s:e]
	if st.pos+len(lit) > len(ctx.s) {
		return nil
	}
	got := ctx.s[st.pos : st.pos+len(lit)]
	if (n.ignoreCase && strings.EqualFold(got, lit)) || (!n.ignoreCase && got == lit) {
		st.pos += len(lit)
		return []btState{st}
	}
	return nil
}

type repeatNode struct {
	child btNode
	min   int
	max   int
}

func (n repeatNode) match(ctx *btCtx, st btState) []btState {
	var levels [][]btState
	levels = append(levels, []btState{st})
	max := n.max
	if max < 0 {

		max = len(ctx.s) - st.pos + 1
	}
	for i := 0; i < max; i++ {
		var next []btState
		for _, s := range levels[len(levels)-1] {
			for _, out := range n.child.match(ctx, s) {
				if out == s {
					continue
				}
				next = append(next, out)
			}
		}
		if len(next) == 0 || ctx.steps > ctx.limit {
			break
		}
		levels = append(levels, next)
	}
	if len(levels)-1 < n.min {
		return nil
	}
	var out []btState
	for i := len(levels) - 1; i >= n.min; i-- {
		out = append(out, levels[i]...)
	}
	return out
}

type parser struct {
	p            string
	i            int
	groups       int
	closed       [10]bool
	ignoreCase   bool
	dotAll       bool
	extended     bool
	cutf8        bool
	localeTables *LocaleByteTables
}

func compileBackref(pattern, flags string, extended bool) (*btProg, error) {
	return compileBackrefMode(pattern, flags, extended, false, nil)
}

func compileBackrefMode(pattern, flags string, extended, cutf8 bool, tables *LocaleByteTables) (*btProg, error) {

	ignoreCase := strings.Contains(flags, "i")
	dotAll := strings.Contains(flags, "s")
	p := &parser{p: pattern, ignoreCase: ignoreCase, dotAll: dotAll, extended: extended, cutf8: cutf8, localeTables: tables}
	root, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.i != len(pattern) {
		return nil, fmt.Errorf("unsupported BRE syntax near %q", pattern[p.i:])
	}
	prog := &btProg{root: root, groups: p.groups, ignoreCase: ignoreCase, cutf8: cutf8}

	if seq, ok := root.(seqNode); ok && len(seq) > 0 {
		if a, ok := seq[0].(anchorNode); ok && byte(a) == '^' {
			prog.anchored = true
		}
	}
	return prog, nil
}

func (p *parser) parseExpr() (btNode, error) {
	var alts []btNode
	for {
		seq, err := p.parseSeq()
		if err != nil {
			return nil, err
		}
		alts = append(alts, seq)
		if p.isAlt() {
			p.i += 2
			if p.extended {
				p.i--
			}
			continue
		}
		break
	}
	if len(alts) == 1 {
		return alts[0], nil
	}
	return altNode(alts), nil
}

func (p *parser) parseSeq() (btNode, error) {
	var nodes []btNode
	state := posStart
	for p.i < len(p.p) {
		if p.endsSeq() {
			break
		}
		atom, nextState, quantifiable, err := p.parseAtom(state)
		if err != nil {
			return nil, err
		}
		if quantifiable {
			atom, err = p.parseQuant(atom)
			if err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, atom)
		state = nextState
	}
	return seqNode(nodes), nil
}

func (p *parser) parseAtom(state int) (btNode, int, bool, error) {
	c := p.p[p.i]
	switch c {
	case '\\':
		if p.i+1 >= len(p.p) {
			return nil, state, false, fmt.Errorf("trailing backslash (\\)")
		}
		n := p.p[p.i+1]
		switch {
		case n == '(' && !p.extended:
			p.i += 2
			p.groups++
			num := p.groups
			if num > 9 {
				return nil, state, false, fmt.Errorf("too many capture groups for BRE back-reference matcher")
			}
			child, err := p.parseExpr()
			if err != nil {
				return nil, state, false, err
			}
			if p.i+1 >= len(p.p) || p.p[p.i] != '\\' || p.p[p.i+1] != ')' {
				return nil, state, false, fmt.Errorf("unmatched \\(")
			}
			p.i += 2
			p.closed[num] = true
			return groupNode{num: num, child: child}, posAtom, true, nil
		case n == ')' && !p.extended:
			p.i += 2
			return literalNode{lit: ")", ignoreCase: p.ignoreCase}, posAtom, true, nil
		case n >= '1' && n <= '9':
			p.i += 2
			num := int(n - '0')
			if num > p.groups || !p.closed[num] {
				return nil, state, false, fmt.Errorf("invalid back-reference \\%c", n)
			}
			return backrefNode{num: num, ignoreCase: p.ignoreCase}, posAtom, true, nil
		case n == '<' || n == '>':
			p.i += 2
			return wordEdgeNode(n), posAnchor, false, nil
		case n == 'b' || n == 'B':
			p.i += 2
			return wordBoundaryNode(n), posAnchor, false, nil
		case n == 'w' || n == 'W' || n == 's' || n == 'S':
			p.i += 2
			return builtinClassNode(n), posAtom, true, nil
		case n == '{':
			if p.extended {
				p.i += 2
				return literalNode{lit: "{", ignoreCase: p.ignoreCase}, posAtom, true, nil
			}
			return nil, state, false, fmt.Errorf("\\{ with nothing to repeat")
		case n == '}':
			if p.extended {
				p.i += 2
				return literalNode{lit: "}", ignoreCase: p.ignoreCase}, posAtom, true, nil
			}
			return nil, state, false, fmt.Errorf("unmatched \\}")
		default:
			p.i += 2
			return literalNode{lit: string(n), ignoreCase: p.ignoreCase}, posAtom, true, nil
		}
	case '(':
		if p.extended {
			p.i++
			if p.i+1 < len(p.p) && p.p[p.i] == '?' && p.p[p.i+1] == ':' {
				p.i += 2
				child, err := p.parseExpr()
				if err != nil {
					return nil, state, false, err
				}
				if p.i >= len(p.p) || p.p[p.i] != ')' {
					return nil, state, false, fmt.Errorf("unmatched (?:")
				}
				p.i++
				return child, posAtom, true, nil
			}
			p.groups++
			num := p.groups
			if num > 9 {
				return nil, state, false, fmt.Errorf("too many capture groups for ERE back-reference matcher")
			}
			child, err := p.parseExpr()
			if err != nil {
				return nil, state, false, err
			}
			if p.i >= len(p.p) || p.p[p.i] != ')' {
				return nil, state, false, fmt.Errorf("unmatched (")
			}
			p.i++
			p.closed[num] = true
			return groupNode{num: num, child: child}, posAtom, true, nil
		}
		p.i++
		return literalNode{lit: "(", ignoreCase: p.ignoreCase}, posAtom, true, nil
	case ')':
		p.i++
		return literalNode{lit: ")", ignoreCase: p.ignoreCase}, posAtom, true, nil
	case '.':
		p.i++
		return dotNode{dotAll: p.dotAll, cutf8: p.cutf8}, posAtom, true, nil
	case '^':
		p.i++
		if state == posStart {
			return anchorNode('^'), posAnchor, false, nil
		}
		return literalNode{lit: "^", ignoreCase: p.ignoreCase}, posAtom, true, nil
	case '$':
		p.i++
		if p.dollarAnchors() {
			return anchorNode('$'), posAnchor, false, nil
		}
		return literalNode{lit: "$", ignoreCase: p.ignoreCase}, posAtom, true, nil
	case '[':
		cls, n, err := translateBracketMode(p.p[p.i:], p.cutf8, p.localeTables)
		if err != nil {
			return nil, state, false, err
		}
		if p.ignoreCase {
			cls = "(?i)" + cls
		}
		re, err := regexp.Compile("^" + cls + "$")
		if err != nil {
			return nil, state, false, err
		}
		p.i += n
		maxBytes := 0
		if p.localeTables != nil && p.localeTables.tables.elements != nil {
			maxBytes = utf8.UTFMax
			for _, e := range p.localeTables.tables.elements.elements {
				if len(e.Text) > maxBytes {
					maxBytes = len(e.Text)
				}
			}
		}
		return classNode{re: re, cutf8: p.cutf8, maxElementBytes: maxBytes}, posAtom, true, nil
	case '*':
		p.i++
		return literalNode{lit: "*", ignoreCase: p.ignoreCase}, posAtom, true, nil
	case '+', '?', '{', '}', '|':
		p.i++
		return literalNode{lit: string(c), ignoreCase: p.ignoreCase}, posAtom, true, nil
	default:
		_, size := utf8.DecodeRuneInString(p.p[p.i:])
		if size == 0 {
			size = 1
		}
		lit := p.p[p.i : p.i+size]
		p.i += size
		return literalNode{lit: lit, ignoreCase: p.ignoreCase}, posAtom, true, nil
	}
}

func (p *parser) dollarAnchors() bool {
	if p.i == len(p.p) {
		return true
	}
	if p.extended {
		return p.p[p.i] == ')' || p.p[p.i] == '|'
	}
	return p.i+1 < len(p.p) && p.p[p.i] == '\\' && (p.p[p.i+1] == ')' || p.p[p.i+1] == '|')
}

func (p *parser) parseQuant(atom btNode) (btNode, error) {
	if p.i >= len(p.p) {
		return atom, nil
	}
	if p.p[p.i] == '*' {
		p.i++
		return repeatNode{child: atom, min: 0, max: -1}, nil
	}
	if p.extended {
		switch p.p[p.i] {
		case '+':
			p.i++
			return repeatNode{child: atom, min: 1, max: -1}, nil
		case '?':
			p.i++
			return repeatNode{child: atom, min: 0, max: 1}, nil
		case '{':
			end := strings.IndexByte(p.p[p.i+1:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unmatched {")
			}
			inner := p.p[p.i+1 : p.i+1+end]
			norm, ok := normalizeInterval(inner)
			if !ok {
				return nil, fmt.Errorf("invalid interval {%s}", inner)
			}
			min, max, err := parseInterval(norm)
			if err != nil {
				return nil, err
			}
			p.i += 1 + end + 1
			return repeatNode{child: atom, min: min, max: max}, nil
		}
		return atom, nil
	}
	if p.p[p.i] != '\\' || p.i+1 >= len(p.p) {
		return atom, nil
	}
	switch p.p[p.i+1] {
	case '+':
		p.i += 2
		return repeatNode{child: atom, min: 1, max: -1}, nil
	case '?':
		p.i += 2
		return repeatNode{child: atom, min: 0, max: 1}, nil
	case '{':
		end := strings.Index(p.p[p.i+2:], `\}`)
		if end < 0 {
			return nil, fmt.Errorf("unmatched \\{")
		}
		inner := p.p[p.i+2 : p.i+2+end]
		norm, ok := normalizeInterval(inner)
		if !ok {
			return nil, fmt.Errorf("invalid interval \\{%s\\}", inner)
		}
		min, max, err := parseInterval(norm)
		if err != nil {
			return nil, err
		}
		p.i += 2 + end + 2
		return repeatNode{child: atom, min: min, max: max}, nil
	}
	return atom, nil
}

func (p *parser) isAlt() bool {
	if p.extended {
		return p.i < len(p.p) && p.p[p.i] == '|'
	}
	return p.i+1 < len(p.p) && p.p[p.i] == '\\' && p.p[p.i+1] == '|'
}

func (p *parser) endsSeq() bool {
	if p.extended {
		return p.p[p.i] == ')' || p.p[p.i] == '|'
	}
	return p.p[p.i] == '\\' && p.i+1 < len(p.p) && (p.p[p.i+1] == ')' || p.p[p.i+1] == '|')
}

func parseInterval(s string) (int, int, error) {
	if comma := strings.IndexByte(s, ','); comma >= 0 {
		min, _ := strconv.Atoi(s[:comma])
		if comma == len(s)-1 {
			return min, -1, nil
		}
		max, _ := strconv.Atoi(s[comma+1:])
		return min, max, nil
	}
	n, _ := strconv.Atoi(s)
	return n, n, nil
}

func (p *btProg) find(s string, n int) ([][]int, error) {
	var out [][]int
	limit := n
	ctx := &btCtx{s: s, limit: maxBacktrackSteps}

	for start := 0; start <= len(s); {
		st, ok := p.pick(p.matchAt(ctx, start))
		if ctx.steps > ctx.limit {
			return nil, &MatchLimitError{Limit: ctx.limit}
		}
		if ok {
			match := make([]int, 2*(p.groups+1))
			for i := range match {
				match[i] = -1
			}
			match[0], match[1] = start, st.pos
			copy(match[2:], st.caps[2:2*(p.groups+1)])
			out = append(out, match)
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
			if st.pos > start {
				start = st.pos
				continue
			}
		}
		if p.anchored {
			break
		}
		if start == len(s) {
			break
		}
		if p.cutf8 {
			_, size := utf8.DecodeRuneInString(s[start:])
			start += size
		} else {
			start++
		}
	}
	return out, nil
}

func (p *btProg) pick(sts []btState) (btState, bool) {
	if len(sts) == 0 {
		return btState{}, false
	}
	best := sts[0]
	if p.longest {
		for _, st := range sts[1:] {
			if st.pos > best.pos {
				best = st
			}
		}
	}
	return best, true
}

func (p *btProg) matchAt(ctx *btCtx, start int) []btState {
	var st btState
	st.pos = start
	for i := range st.caps {
		st.caps[i] = -1
	}
	outs := p.root.match(ctx, st)
	if ctx.steps > ctx.limit {
		return nil
	}
	return outs
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
