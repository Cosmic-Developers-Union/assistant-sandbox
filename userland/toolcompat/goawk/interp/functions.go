package interp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/internal/resolver"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/lexer"
)

func (p *interp) callNative(index int, args []value) (value, error) {
	f := p.nativeFuncs[index]
	minIn := len(f.in)
	var variadicType reflect.Type
	if f.isVariadic {
		variadicType = f.in[len(f.in)-1].Elem()
		minIn--
	}

	values := make([]reflect.Value, 0, 7)
	for i, a := range args {
		var argType reflect.Type
		if !f.isVariadic || i < len(f.in)-1 {
			argType = f.in[i]
		} else {

			argType = variadicType
		}
		values = append(values, p.toNative(a, argType))
	}

	for i := len(args); i < minIn; i++ {
		values = append(values, reflect.Zero(f.in[i]))
	}

	outs := f.value.Call(values)
	switch len(outs) {
	case 0:

		return null(), nil
	case 1:

		return fromNative(outs[0], p.decimalPoint), nil
	case 2:

		if !outs[1].IsNil() {
			return null(), outs[1].Interface().(error)
		}
		return fromNative(outs[0], p.decimalPoint), nil
	default:

		panic(fmt.Sprintf("unexpected number of return values: %d", len(outs)))
	}
}

func (p *interp) toNative(v value, typ reflect.Type) reflect.Value {
	switch typ.Kind() {
	case reflect.Bool:
		return reflect.ValueOf(v.boolean())
	case reflect.Int:
		return reflect.ValueOf(int(v.num()))
	case reflect.Int8:
		return reflect.ValueOf(int8(v.num()))
	case reflect.Int16:
		return reflect.ValueOf(int16(v.num()))
	case reflect.Int32:
		return reflect.ValueOf(int32(v.num()))
	case reflect.Int64:
		return reflect.ValueOf(int64(v.num()))
	case reflect.Uint:
		return reflect.ValueOf(int64(v.num())).Convert(typ)
	case reflect.Uint8:
		return reflect.ValueOf(int64(v.num())).Convert(typ)
	case reflect.Uint16:
		return reflect.ValueOf(int64(v.num())).Convert(typ)
	case reflect.Uint32:
		return reflect.ValueOf(int64(v.num())).Convert(typ)
	case reflect.Uint64:
		return reflect.ValueOf(int64(v.num())).Convert(typ)
	case reflect.Float32:
		return reflect.ValueOf(float32(v.num()))
	case reflect.Float64:
		return reflect.ValueOf(v.num())
	case reflect.String:
		return reflect.ValueOf(p.toString(v))
	case reflect.Slice:
		if typ.Elem().Kind() != reflect.Uint8 {

			panic(fmt.Sprintf("unexpected argument slice: %s", typ.Elem().Kind()))
		}
		return reflect.ValueOf([]byte(p.toString(v)))
	default:

		panic(fmt.Sprintf("unexpected argument type: %s", typ.Kind()))
	}
}

func fromNative(v reflect.Value, radix byte) value {
	switch v.Kind() {
	case reflect.Bool:
		return boolean(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return num(float64(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return num(float64(v.Uint()))
	case reflect.Float32, reflect.Float64:
		return num(v.Float())
	case reflect.String:
		return localeStr(v.String(), radix)
	case reflect.Slice:
		if b, ok := v.Interface().([]byte); ok {
			return localeStr(string(b), radix)
		}

		panic(fmt.Sprintf("unexpected return slice: %s", v.Type().Elem().Kind()))
	default:

		panic(fmt.Sprintf("unexpected return type: %s", v.Kind()))
	}
}

type nativeFunc struct {
	isVariadic bool
	in         []reflect.Type
	value      reflect.Value
}

func (p *interp) initNativeFuncs(funcs map[string]any) error {
	for name, f := range funcs {
		err := checkNativeFunc(name, f)
		if err != nil {
			return err
		}
	}

	names := make([]string, 0, len(funcs))
	for name := range funcs {
		names = append(names, name)
	}
	sort.Strings(names)
	p.nativeFuncs = make([]nativeFunc, len(names))
	for i, name := range names {
		f := funcs[name]
		typ := reflect.TypeOf(f)
		in := make([]reflect.Type, typ.NumIn())
		for j := 0; j < len(in); j++ {
			in[j] = typ.In(j)
		}
		p.nativeFuncs[i] = nativeFunc{
			isVariadic: typ.IsVariadic(),
			in:         in,
			value:      reflect.ValueOf(f),
		}
	}
	return nil
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()

func checkNativeFunc(name string, f any) error {
	if lexer.KeywordToken(name) != lexer.ILLEGAL {
		return newError("can't use keyword %q as native function name", name)
	}

	typ := reflect.TypeOf(f)
	if typ.Kind() != reflect.Func {
		return newError("native function %q is not a function", name)
	}
	for i := 0; i < typ.NumIn(); i++ {
		param := typ.In(i)
		if typ.IsVariadic() && i == typ.NumIn()-1 {
			param = param.Elem()
		}
		if !validNativeType(param) {
			return newError("native function %q param %d is not int or string", name, i)
		}
	}

	switch typ.NumOut() {
	case 0:

	case 1:

		if !validNativeType(typ.Out(0)) {
			return newError("native function %q return value is not int or string", name)
		}
	case 2:

		if !validNativeType(typ.Out(0)) {
			return newError("native function %q first return value is not int or string", name)
		}
		if typ.Out(1) != errorType {
			return newError("native function %q second return value is not an error", name)
		}
	default:
		return newError("native function %q returns more than two values", name)
	}
	return nil
}

func validNativeType(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool:
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	case reflect.Float32, reflect.Float64:
		return true
	case reflect.String:
		return true
	case reflect.Slice:

		return typ.Elem().Kind() == reflect.Uint8
	default:
		return false
	}
}

func (p *interp) split(s string, scope resolver.Scope, index int, sep string, sepIsRegex bool, mode IOMode) (int, error) {
	var parts []string
	switch {
	case mode == CSVMode || mode == TSVMode:

		splitter := csvSplitter{
			separator: p.csvInputConfig.Separator,
			sepLen:    utf8.RuneLen(p.csvInputConfig.Separator),
			comment:   p.csvInputConfig.Comment,
			fields:    &parts,
		}
		scanner := bufio.NewScanner(strings.NewReader(s))
		scanner.Split(splitter.scan)
		if p.splitBuffer == nil {
			p.splitBuffer = make([]byte, inputBufSize)
		}
		scanner.Buffer(p.splitBuffer, maxRecordLength)

		scanner.Scan()
	case !sepIsRegex && sep == " ":
		parts = strings.Fields(s)
	case s == "":

	case !sepIsRegex && utf8.RuneCountInString(sep) <= 1:
		parts = strings.Split(s, sep)
	default:
		re, err := p.compileRegexStd(sep)
		if err != nil {
			return 0, err
		}
		parts, err = re.Split(s, -1)
		if err != nil {
			return 0, err
		}
	}
	array := make(map[string]value, len(parts))
	for i, part := range parts {
		array[strconv.Itoa(i+1)] = localeNumStr(part, p.decimalPoint)
	}
	p.arrays[p.arrayIndex(scope, index)] = array
	return len(array), nil
}

func (p *interp) sub(regex, repl, in string, global bool) (out string, num int, err error) {
	re, err := p.compileRegexStd(regex)
	if err != nil {
		return "", 0, err
	}
	count := 0
	out, err = re.ReplaceAllStringFunc(in, func(s string) string {

		if !global && count > 0 {
			return s
		}
		count++

		r := make([]byte, 0, 64)
		for i := 0; i < len(repl); i++ {
			switch repl[i] {
			case '&':
				r = append(r, s...)
			case '\\':
				i++
				if i < len(repl) {
					switch repl[i] {
					case '&':
						r = append(r, '&')
					case '\\':
						r = append(r, '\\')
					default:
						r = append(r, '\\', repl[i])
					}
				} else {
					r = append(r, '\\')
				}
			default:
				r = append(r, repl[i])
			}
		}
		return string(r)
	})
	if err != nil {
		return "", 0, err
	}
	return out, count, nil
}

type cachedFormat struct {
	format string
	types  []byte
}

type printfConversion struct {
	hash           bool
	verb           byte
	verbPos        int
	precisionDot   int
	precisionStart int
	precisionEnd   int
	precisionStar  bool
	precisionArg   int
	operandArg     int
}

func scanPrintfConversions(format string) []printfConversion {
	var conversions []printfConversion
	argIndex := 0
	for i := 0; i < len(format); {
		if format[i] != '%' {
			i++
			continue
		}
		i++
		if i >= len(format) {
			break
		}
		if format[i] == '%' {
			i++
			continue
		}

		conv := printfConversion{precisionDot: -1, precisionArg: -1}
		for i < len(format) && strings.IndexByte(" +-#0", format[i]) >= 0 {
			conv.hash = conv.hash || format[i] == '#'
			i++
		}
		if i < len(format) && format[i] == '*' {
			argIndex++
			i++
		} else {
			for i < len(format) && format[i] >= '0' && format[i] <= '9' {
				i++
			}
		}
		if i < len(format) && format[i] == '.' {
			conv.precisionDot = i
			i++
			conv.precisionStart = i
			if i < len(format) && format[i] == '*' {
				conv.precisionStar = true
				conv.precisionArg = argIndex
				argIndex++
				i++
			} else {
				for i < len(format) && format[i] >= '0' && format[i] <= '9' {
					i++
				}
			}
			conv.precisionEnd = i
		}
		if i >= len(format) {
			break
		}
		conv.verb = format[i]
		conv.verbPos = i
		conv.operandArg = argIndex
		argIndex++
		conversions = append(conversions, conv)
		i++
	}
	return conversions
}

func normalizeDefaultGeneralPrecisions(format string) string {
	if !strings.ContainsAny(format, "gG") {
		return format
	}
	var edits []formatEdit
	for _, conv := range scanPrintfConversions(format) {
		if (conv.verb == 'g' || conv.verb == 'G') && conv.precisionDot < 0 {
			edits = append(edits, formatEdit{conv.verbPos, conv.verbPos, ".6"})
		}
	}
	return applyFormatEdits(format, edits)
}

type formatEdit struct {
	start, end  int
	replacement string
}

func applyFormatEdits(format string, edits []formatEdit) string {
	if len(edits) == 0 {
		return format
	}
	var out strings.Builder
	last := 0
	for _, edit := range edits {
		out.WriteString(format[last:edit.start])
		out.WriteString(edit.replacement)
		last = edit.end
	}
	out.WriteString(format[last:])
	return out.String()
}

func dropFormatArgs(args []value, drop []bool) []value {
	filtered := make([]value, 0, len(args))
	for i, arg := range args {
		if !drop[i] {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}

func normalizeNegativeDynamicPrecisions(format string, args []value) (string, []value) {
	if !strings.Contains(format, ".*") {
		return format, args
	}
	var edits []formatEdit
	drop := make([]bool, len(args))
	for _, conv := range scanPrintfConversions(format) {
		if conv.precisionStar && conv.precisionArg < len(args) &&
			int64(args[conv.precisionArg].num()) < 0 {
			edits = append(edits, formatEdit{conv.precisionDot, conv.precisionEnd, ""})
			drop[conv.precisionArg] = true
		}
	}
	if len(edits) == 0 {
		return format, args
	}
	return applyFormatEdits(format, edits), dropFormatArgs(args, drop)
}

func normalizeOctalHashZeroPrecision(format string, args []value) (string, []value) {
	if !strings.Contains(format, "#") {
		return format, args
	}
	var edits []formatEdit
	drop := make([]bool, len(args))
	for _, conv := range scanPrintfConversions(format) {
		if !conv.hash || conv.verb != 'o' || conv.precisionDot < 0 ||
			conv.operandArg >= len(args) || int64(args[conv.operandArg].num()) != 0 {
			continue
		}
		if conv.precisionStar {
			if conv.precisionArg < len(args) && int64(args[conv.precisionArg].num()) == 0 {
				edits = append(edits, formatEdit{conv.precisionStart, conv.precisionEnd, "1"})
				drop[conv.precisionArg] = true
			}
			continue
		}
		zero := true
		for i := conv.precisionStart; i < conv.precisionEnd; i++ {
			zero = zero && format[i] == '0'
		}
		if zero {
			edits = append(edits, formatEdit{conv.precisionStart, conv.precisionEnd, "1"})
		}
	}
	if len(edits) == 0 {
		return format, args
	}
	return applyFormatEdits(format, edits), dropFormatArgs(args, drop)
}

func (p *interp) parseFmtTypes(s string) (format string, types []byte, err error) {
	if item, ok := p.formatCache[s]; ok {
		return item.format, item.types, nil
	}

	out := []byte(s)
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			i++
			if i >= len(s) {
				return "", nil, errors.New("expected type specifier after %")
			}
			if s[i] == '%' {
				continue
			}
			for i < len(s) && strings.IndexByte(" .-+*#0123456789", s[i]) >= 0 {
				if s[i] == '*' {
					types = append(types, 'd')
				}
				i++
			}
			if i >= len(s) {
				return "", nil, errors.New("expected type specifier after %")
			}
			var t byte
			switch s[i] {
			case 's':
				t = 's'
			case 'd':
				t = 'd'
			case 'o', 'x', 'X':
				t = 'u'
			case 'i':
				t = 'd'
				out[i] = 'd'
			case 'a', 'A', 'e', 'E', 'f', 'F', 'g', 'G':

				t = s[i]
			case 'u':
				t = 'u'
				out[i] = 'd'
			case 'c':
				t = 'c'
				out[i] = 's'
			default:
				return "", nil, fmt.Errorf("invalid format type %q", s[i])
			}
			types = append(types, t)
		}
	}

	format = string(out)
	if len(p.formatCache) < maxCachedFormats {
		p.formatCache[s] = cachedFormat{format, types}
	}
	return format, types, nil
}

func (p *interp) sprintf(format string, args []value) (string, error) {
	format, args = normalizeNegativeDynamicPrecisions(format, args)
	format = normalizeDefaultGeneralPrecisions(format)
	format, args = normalizeOctalHashZeroPrecision(format, args)
	format, types, err := p.parseFmtTypes(format)
	if err != nil {
		return "", newError("format error: %s", err)
	}
	if len(types) > len(args) {
		return "", newError("format error: got %d args, expected %d", len(args), len(types))
	}
	converted := make([]any, 0, 7)
	for i, t := range types {
		a := args[i]
		var v any
		switch t {
		case 's':
			v = p.toString(a)
		case 'd':
			v = int64(a.num())
		case 'a', 'A', 'e', 'E', 'f', 'F', 'g', 'G':
			if p.decimalPoint == 0 || p.decimalPoint == '.' {
				v = awkFloat(a.num())
			} else {
				v = localizedAwkFloat{a.num(), p.decimalPoint}
			}
		case 'u':
			v = reflect.ValueOf(int64(a.num())).Convert(reflect.TypeOf(uint64(0))).Uint()
		case 'c':
			var c []byte
			n, isStr := a.isTrueStr()
			if isStr {
				s := p.toString(a)
				if len(s) == 0 {
					c = []byte{0}
				} else if p.chars {
					_, size := utf8.DecodeRuneInString(s)
					c = []byte(s[:size])
				} else {
					c = []byte{s[0]}
				}
			} else {
				if p.chars {
					buf := make([]byte, utf8.UTFMax)
					size := utf8.EncodeRune(buf, rune(n))
					c = buf[:size]
				} else {
					c = []byte{byte(n)}
				}
			}
			v = c
		}
		converted = append(converted, v)
	}
	return fmt.Sprintf(format, converted...), nil
}

type awkFloat float64

func (v awkFloat) Format(state fmt.State, verb rune) {
	formatAwkFloat(state, verb, float64(v), '.')
}

type localizedAwkFloat struct {
	n     float64
	radix byte
}

func (v localizedAwkFloat) Format(state fmt.State, verb rune) {
	formatAwkFloat(state, verb, v.n, v.radix)
}

func formatAwkFloat(state fmt.State, verb rune, n float64, radix byte) {
	upper := verb == 'A' || verb == 'F'
	special := math.IsNaN(n) || math.IsInf(n, 0)
	if !special && verb != 'a' && verb != 'A' && verb != 'F' {
		var format strings.Builder
		format.WriteByte('%')
		for _, flag := range "#0+- " {
			if state.Flag(int(flag)) {
				format.WriteRune(flag)
			}
		}
		if width, ok := state.Width(); ok {
			format.WriteString(strconv.Itoa(width))
		}
		if precision, ok := state.Precision(); ok {
			format.WriteByte('.')
			format.WriteString(strconv.Itoa(precision))
		}
		format.WriteRune(verb)
		_, _ = io.WriteString(state, localizeDecimal(fmt.Sprintf(format.String(), n), radix))
		return
	}
	upper = upper || verb == 'E' || verb == 'G'

	sign := ""
	switch {
	case math.Signbit(n):
		sign = "-"
		n = math.Abs(n)
	case state.Flag('+'):
		sign = "+"
	case state.Flag(' '):
		sign = " "
	}

	precision, hasPrecision := state.Precision()
	var body string
	switch {
	case math.IsNaN(n):
		body = "nan"
	case math.IsInf(n, 1):
		body = "inf"
	case verb == 'f' || verb == 'F':
		if !hasPrecision {
			precision = 6
		}
		body = strconv.FormatFloat(n, 'f', precision, 64)
		if state.Flag('#') && precision == 0 {
			body += "."
		}
	case verb == 'a' || verb == 'A':
		body = formatHexFloat(n, precision, hasPrecision)
		body = trimHexExponentZeros(body)
		if state.Flag('#') {
			if exponent := strings.IndexByte(body, 'p'); exponent >= 0 &&
				!strings.Contains(body[:exponent], ".") {
				body = body[:exponent] + "." + body[exponent:]
			}
		}
	default:
		panic("unreachable non-special awk float verb")
	}
	if upper {
		body = strings.ToUpper(body)
	}
	body = localizeDecimal(body, radix)

	width, hasWidth := state.Width()
	padding := 0
	if hasWidth && width > len(sign)+len(body) {
		padding = width - len(sign) - len(body)
	}
	if state.Flag('-') {
		_, _ = io.WriteString(state, sign)
		_, _ = io.WriteString(state, body)
		writePadding(state, ' ', padding)
		return
	}
	if state.Flag('0') && !special {
		_, _ = io.WriteString(state, sign)
		if (verb == 'a' || verb == 'A') && len(body) >= 2 &&
			(body[:2] == "0x" || body[:2] == "0X") {
			_, _ = io.WriteString(state, body[:2])
			writePadding(state, '0', padding)
			_, _ = io.WriteString(state, body[2:])
			return
		}
		writePadding(state, '0', padding)
		_, _ = io.WriteString(state, body)
		return
	}
	writePadding(state, ' ', padding)
	_, _ = io.WriteString(state, sign)
	_, _ = io.WriteString(state, body)
}

func formatHexFloat(n float64, precision int, hasPrecision bool) string {
	if !hasPrecision {
		precision = -1
	}
	body := strconv.FormatFloat(n, 'x', precision, 64)
	if !hasPrecision || n == 0 {
		return body
	}

	_, exponent := math.Frexp(n)
	wantExponent := exponent - 1
	p := strings.IndexByte(body, 'p')
	if p < 0 || !strings.HasPrefix(body, "0x1") {
		return body
	}
	gotExponent, err := strconv.Atoi(body[p+1:])
	if err != nil || gotExponent != wantExponent+1 {
		return body
	}
	body = body[:2] + "2" + body[3:p] + "p"
	if wantExponent >= 0 {
		body += "+"
	}
	return body + strconv.Itoa(wantExponent)
}

func trimHexExponentZeros(s string) string {
	exponent := strings.IndexByte(s, 'p')
	if exponent < 0 || exponent+2 >= len(s) {
		return s
	}
	digits := exponent + 2
	for digits+1 < len(s) && s[digits] == '0' {
		digits++
	}
	return s[:exponent+2] + s[digits:]
}

func writePadding(w io.Writer, b byte, count int) {
	if count > 0 {
		_, _ = io.WriteString(w, strings.Repeat(string(b), count))
	}
}

func substrChars(s string, pos int) string {

	chars := 1
	start := 0
	for start = range s {
		chars++
		if chars > pos {
			break
		}
	}
	if pos >= chars {
		start = len(s)
	}
	return s[start:]
}

func substrLengthChars(s string, pos, length int) string {

	chars := 1
	start := 0
	for start = range s {
		chars++
		if chars > pos {
			break
		}
	}
	if pos >= chars {
		start = len(s)
	}

	chars = 0
	end := 0
	for end = range s[start:] {
		chars++
		if chars > length {
			break
		}
	}
	if length >= chars {
		end = len(s)
	} else {
		end += start
	}

	return s[start:end]
}
