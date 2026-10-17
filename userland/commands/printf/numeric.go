package printfcmd

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

func isSpaceByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

func digitValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return 99
}

func cScan(s string) (mag uint64, neg, overflow bool, nDigits int, leftover bool) {
	i := 0
	for i < len(s) && isSpaceByte(s[i]) {
		i++
	}
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	base := 10
	if i < len(s) && s[i] == '0' {
		if i+1 < len(s) && (s[i+1]|0x20) == 'x' && i+2 < len(s) && digitValue(s[i+2]) < 16 {
			base = 16
			i += 2
		} else {

			base = 8
		}
	}
	start := i
	for i < len(s) {
		d := digitValue(s[i])
		if d >= base {
			break
		}
		if !overflow {
			if mag > (math.MaxUint64-uint64(d))/uint64(base) {
				overflow = true
				mag = math.MaxUint64
			} else {
				mag = mag*uint64(base) + uint64(d)
			}
		}
		i++
	}
	nDigits = i - start
	leftover = i < len(s)
	return
}

func cQuote(s string) string { return "'" + s + "'" }

func numDiag(rc *toolcompat.RunContext, s string, nDigits int, overflow, leftover bool) bool {
	switch {
	case nDigits == 0:
		_, _ = fmt.Fprintf(rc.Err, "%s: %s: expected a numeric value\n", cmd.Name, cQuote(s))
	case overflow:
		_, _ = fmt.Fprintf(rc.Err, "%s: %s: Numerical result out of range\n", cmd.Name, cQuote(s))
	case leftover:
		_, _ = fmt.Fprintf(rc.Err, "%s: %s: value not completely converted\n", cmd.Name, cQuote(s))
	default:
		return false
	}
	return true
}

func warnTrailingConst(rc *toolcompat.RunContext, s string) {
	_, _ = fmt.Fprintf(rc.Err, "%s: warning: %s: character(s) following character constant have been ignored\n", cmd.Name, s)
}

func parseSigned(rc *toolcompat.RunContext, s string) (value int64, hadErr bool) {
	if r, ok, warn := charConstant(s); ok {
		if warn {
			warnTrailingConst(rc, s)
		}
		return int64(r), false
	}
	mag, neg, overflow, nDigits, leftover := cScan(s)
	if neg {
		switch {
		case mag > 1<<63:
			value, overflow = math.MinInt64, true
		case mag == 1<<63:
			value = math.MinInt64
		default:
			value = -int64(mag)
		}
	} else if mag > math.MaxInt64 {
		value, overflow = math.MaxInt64, true
	} else {
		value = int64(mag)
	}
	return value, numDiag(rc, s, nDigits, overflow, leftover)
}

func parseUnsigned(rc *toolcompat.RunContext, s string) (value uint64, hadErr bool) {
	if r, ok, warn := charConstant(s); ok {
		if warn {
			warnTrailingConst(rc, s)
		}
		return uint64(r), false
	}
	mag, neg, overflow, nDigits, leftover := cScan(s)
	if neg {
		value = -mag
	} else {
		value = mag
	}
	return value, numDiag(rc, s, nDigits, overflow, leftover)
}

func commonFoldLen(s, lower string) int {
	n := 0
	for n < len(s) && n < len(lower) && (s[n]|0x20) == lower[n] {
		n++
	}
	return n
}

func isDigitByte(c byte) bool    { return c >= '0' && c <= '9' }
func isHexDigitByte(c byte) bool { return digitValue(c) < 16 }

func isNCharByte(c byte) bool {
	return isDigitByte(c) || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func scanExponent(s string, j int, marker byte) (int, bool) {
	if j >= len(s) || (s[j]|0x20) != marker {
		return 0, false
	}
	k := j + 1
	if k < len(s) && (s[k] == '+' || s[k] == '-') {
		k++
	}
	d := k
	for k < len(s) && isDigitByte(s[k]) {
		k++
	}
	if k == d {
		return 0, false
	}
	return k, true
}

func scanNumericBody(s string, radix byte) (end int, hexNoExp bool) {
	if len(s) > 1 && s[0] == '0' && (s[1]|0x20) == 'x' {
		j, digits := 2, 0
		for j < len(s) && isHexDigitByte(s[j]) {
			j++
			digits++
		}
		if j < len(s) && s[j] == radix {
			j++
			for j < len(s) && isHexDigitByte(s[j]) {
				j++
				digits++
			}
		}
		if digits == 0 {

			return 1, false
		}
		if k, ok := scanExponent(s, j, 'p'); ok {
			return k, false
		}
		return j, true
	}
	j, digits := 0, 0
	for j < len(s) && isDigitByte(s[j]) {
		j++
		digits++
	}
	if j < len(s) && s[j] == radix {
		j++
		for j < len(s) && isDigitByte(s[j]) {
			j++
			digits++
		}
	}
	if digits == 0 {
		return 0, false
	}
	if k, ok := scanExponent(s, j, 'e'); ok {
		return k, false
	}
	return j, false
}

func cStrtod(t string, radix byte) (value float64, consumed int, rangeErr bool) {
	i := 0
	if i < len(t) && (t[i] == '+' || t[i] == '-') {
		i++
	}
	neg := i == 1 && t[0] == '-'
	rest := t[i:]

	if n := commonFoldLen(rest, "infinity"); n >= 3 {

		if n != 8 {
			n = 3
		}
		v := math.Inf(1)
		if neg {
			v = math.Inf(-1)
		}
		return v, i + n, false
	}
	if commonFoldLen(rest, "nan") == 3 {
		n := 3
		if len(rest) > 3 && rest[3] == '(' {
			k := 4
			for k < len(rest) && isNCharByte(rest[k]) {
				k++
			}
			if k < len(rest) && rest[k] == ')' {
				n = k + 1
			}
		}
		v := math.NaN()
		if neg {
			v = math.Copysign(v, -1)
		}
		return v, i + n, false
	}

	body, hexNoExp := scanNumericBody(rest, radix)
	if body == 0 {
		return 0, 0, false
	}
	tok := t[:i+body]
	if radix != '.' {
		tok = strings.Replace(tok, string(radix), ".", 1)
	}
	if hexNoExp {
		tok += "p0"
	}
	v, err := strconv.ParseFloat(tok, 64)
	switch {
	case errors.Is(err, strconv.ErrRange):
		return v, i + body, true
	case err != nil:
		return 0, 0, false
	}
	return v, i + body, false
}

func parseFloat(rc *toolcompat.RunContext, s string) (value float64, hadErr bool) {
	if r, ok, warn := charConstant(s); ok {
		if warn {
			warnTrailingConst(rc, s)
		}
		return float64(r), false
	}
	i := 0
	for i < len(s) && isSpaceByte(s[i]) {
		i++
	}
	t := s[i:]

	v, n, rangeErr := cStrtod(t, numericRadix(rc.Env))
	switch {
	case n == 0:
		_, _ = fmt.Fprintf(rc.Err, "%s: %s: expected a numeric value\n", cmd.Name, cQuote(s))
		return 0, true
	case rangeErr:
		_, _ = fmt.Fprintf(rc.Err, "%s: %s: Numerical result out of range\n", cmd.Name, cQuote(s))
		return v, true
	case n < len(t):
		_, _ = fmt.Fprintf(rc.Err, "%s: %s: value not completely converted\n", cmd.Name, cQuote(s))
		return v, true
	}
	return v, false
}

func writeNonFinite(out *bytes.Buffer, conv byte, flags string, width int, hasWidth bool, fv float64) {
	base := "inf"
	if math.IsNaN(fv) {
		base = "nan"
	}
	switch conv {
	case 'E', 'F', 'G', 'A':
		base = strings.ToUpper(base)
	}
	sign := ""
	switch {
	case math.Signbit(fv):
		sign = "-"
	case strings.ContainsRune(flags, '+'):
		sign = "+"
	case strings.ContainsRune(flags, ' '):
		sign = " "
	}
	body := sign + base
	if !hasWidth || len(body) >= width {
		out.WriteString(body)
		return
	}
	pad := strings.Repeat(" ", width-len(body))
	if strings.ContainsRune(flags, '-') {
		out.WriteString(body)
		out.WriteString(pad)
		return
	}
	out.WriteString(pad)
	out.WriteString(body)
}

func writeHexFloat(out *bytes.Buffer, conv byte, flags string, width int, hasWidth bool, precision int, hasPrecision bool, fv float64) {
	goConv := "x"
	if conv == 'A' {
		goConv = "X"
	}
	var tmp strings.Builder
	verb := buildVerb(flags, 0, false, precision, hasPrecision, goConv)
	fmt.Fprintf(&tmp, verb, fv)
	padNumeric(out, fixHexExp(tmp.String()), flags, width, hasWidth)
}

func numericRadix(env []string) byte {
	name := ""
	for _, key := range []string{"LC_ALL", "LC_NUMERIC", "LANG"} {
		prefix := key + "="
		for i := len(env) - 1; i >= 0; i-- {
			if strings.HasPrefix(env[i], prefix) {
				if value := env[i][len(prefix):]; value != "" {
					name = value
				}
				break
			}
		}
		if name != "" {
			break
		}
	}
	if strings.HasPrefix(strings.ToLower(name), "de_de") {
		return ','
	}
	return '.'
}

func localizeRadix(s string, radix byte) string {
	if radix == '.' {
		return s
	}
	return strings.Replace(s, ".", string(radix), 1)
}

func fixHexExp(s string) string {
	idx := strings.IndexAny(s, "pP")
	if idx < 0 {
		return s
	}
	head, exp := s[:idx+1], s[idx+1:]
	sign := ""
	if len(exp) > 0 && (exp[0] == '+' || exp[0] == '-') {
		sign, exp = exp[:1], exp[1:]
	}
	exp = strings.TrimLeft(exp, "0")
	if exp == "" {
		exp = "0"
	}
	return head + sign + exp
}

func padNumeric(out *bytes.Buffer, s, flags string, width int, hasWidth bool) {
	if !hasWidth || len(s) >= width {
		out.WriteString(s)
		return
	}
	pad := width - len(s)
	if strings.ContainsRune(flags, '-') {
		out.WriteString(s)
		out.WriteString(strings.Repeat(" ", pad))
		return
	}
	if strings.ContainsRune(flags, '0') {
		i := 0
		if i < len(s) && (s[i] == '+' || s[i] == '-' || s[i] == ' ') {
			i++
		}
		if i+1 < len(s) && s[i] == '0' && (s[i+1]|0x20) == 'x' {
			i += 2
		}
		out.WriteString(s[:i])
		out.WriteString(strings.Repeat("0", pad))
		out.WriteString(s[i:])
		return
	}
	out.WriteString(strings.Repeat(" ", pad))
	out.WriteString(s)
}
