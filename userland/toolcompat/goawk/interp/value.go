package interp

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type valueType uint8

const (
	typeNull valueType = iota
	typeStr
	typeNum
	typeNumStr
)

type value struct {
	typ   valueType
	s     string
	n     float64
	radix byte
}

func null() value {
	return value{}
}

func num(n float64) value {
	return value{typ: typeNum, n: n}
}

func localeStr(s string, radix byte) value {
	return value{typ: typeStr, s: s, radix: radix}
}

func localeNumStr(s string, radix byte) value {
	return value{typ: typeNumStr, s: s, radix: radix}
}

func boolean(b bool) value {
	if b {
		return num(1)
	}
	return num(0)
}

func (v value) String() string {
	switch v.typ {
	case typeStr:
		return fmt.Sprintf("str(%q)", v.s)
	case typeNum:
		return fmt.Sprintf("num(%s)", v.str("%.6g"))
	case typeNumStr:
		return fmt.Sprintf("numStr(%q)", v.s)
	default:
		return "null()"
	}
}

func (v value) isTrueStr() (float64, bool) {
	switch v.typ {
	case typeStr:
		return 0, true
	case typeNumStr:
		f, err := parseFloatRadix(v.s, v.radix)
		if err != nil {
			return 0, true
		}
		return f, false
	default:
		return v.n, false
	}
}

func (v value) boolean() bool {
	switch v.typ {
	case typeStr:
		return v.s != ""
	case typeNumStr:
		f, err := parseFloatRadix(v.s, v.radix)
		if err != nil {
			return v.s != ""
		}
		return f != 0
	default:
		return v.n != 0
	}
}

func parseFloat(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if len(s) > 1 && (s[0] == '+' || s[0] == '-') {
		if len(s) == 4 && hasNaNPrefix(s[1:]) {

			n := math.NaN()
			if s[0] == '-' {
				n = math.Copysign(n, -1)
			}
			return n, nil
		}
		if len(s) > 3 && hasHexPrefix(s[1:]) && strings.IndexByte(s, 'p') < 0 && strings.IndexByte(s, 'P') < 0 {
			s += "p0"
		}
	} else if len(s) > 2 && hasHexPrefix(s) && strings.IndexByte(s, 'p') < 0 && strings.IndexByte(s, 'P') < 0 {
		s += "p0"
	}
	n, err := strconv.ParseFloat(s, 64)
	if err == nil && strings.IndexByte(s, '_') >= 0 {

		return 0, strconv.ErrSyntax
	}
	return n, err
}

func parseFloatRadix(s string, radix byte) (float64, error) {
	if radix == 0 || radix == '.' {
		return parseFloat(s)
	}
	if strings.ContainsRune(s, '.') {
		return 0, strconv.ErrSyntax
	}
	return parseFloat(strings.Replace(s, string(radix), ".", 1))
}

func (v value) str(floatFormat string) string {
	if v.typ == typeNum {
		switch {
		case math.IsNaN(v.n):
			if math.Signbit(v.n) {
				return "-nan"
			}
			return "nan"
		case math.IsInf(v.n, 0):
			if v.n < 0 {
				return "-inf"
			} else {
				return "inf"
			}
		case v.n == float64(int64(v.n)):
			return strconv.FormatInt(int64(v.n), 10)
		default:
			if floatFormat == "%.6g" {
				return strconv.FormatFloat(v.n, 'g', 6, 64)
			}

			floatFormat = normalizeDefaultGeneralPrecisions(floatFormat)
			return fmt.Sprintf(floatFormat, v.n)
		}
	}

	return v.s
}

func (v value) num() float64 {
	switch v.typ {
	case typeStr, typeNumStr:

		return parseFloatPrefixRadix(v.s, v.radix)
	default:
		return v.n
	}
}

var asciiSpace = [256]uint8{'\t': 1, '\n': 1, '\v': 1, '\f': 1, '\r': 1, ' ': 1}

func parseFloatPrefix(s string) float64 {

	i := 0
	for i < len(s) && asciiSpace[s[i]] != 0 {
		i++
	}
	start := i

	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	if i+3 <= len(s) {
		if hasNaNPrefix(s[i:]) {
			n := math.NaN()
			if i > start && s[start] == '-' {
				n = math.Copysign(n, -1)
			}
			return n
		}
		if hasInfPrefix(s[i:]) {
			if s[start] == '-' {
				return math.Inf(-1)
			}
			return math.Inf(1)
		}
	}

	if i+2 < len(s) && hasHexPrefix(s[i:]) {
		return parseHexFloatPrefix(s, start, i+2)
	}
	gotDigit := false
	for i < len(s) && isDigit(s[i]) {
		gotDigit = true
		i++
	}
	if i < len(s) && s[i] == '.' {
		i++
	}
	for i < len(s) && isDigit(s[i]) {
		gotDigit = true
		i++
	}
	if !gotDigit {
		return 0
	}

	end := i
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		for i < len(s) && isDigit(s[i]) {
			i++
			end = i
		}
	}

	floatStr := s[start:end]
	f, _ := strconv.ParseFloat(floatStr, 64)
	return f
}

func parseFloatPrefixRadix(s string, radix byte) float64 {
	if radix == 0 || radix == '.' {
		return parseFloatPrefix(s)
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, radix); i >= 0 {
		s = s[:i] + "." + s[i+1:]
	}
	return parseFloatPrefix(s)
}

func hasHexPrefix(s string) bool {
	return s[0] == '0' && (s[1] == 'x' || s[1] == 'X')
}

func hasNaNPrefix(s string) bool {
	return (s[0] == 'n' || s[0] == 'N') && (s[1] == 'a' || s[1] == 'A') && (s[2] == 'n' || s[2] == 'N')
}

func hasInfPrefix(s string) bool {
	return (s[0] == 'i' || s[0] == 'I') && (s[1] == 'n' || s[1] == 'N') && (s[2] == 'f' || s[2] == 'F')
}

func parseHexFloatPrefix(s string, start, i int) float64 {
	gotDigit := false
	for i < len(s) && isHexDigit(s[i]) {
		gotDigit = true
		i++
	}
	if i < len(s) && s[i] == '.' {
		i++
	}
	for i < len(s) && isHexDigit(s[i]) {
		gotDigit = true
		i++
	}
	if !gotDigit {
		return 0
	}

	gotExponent := false
	end := i
	if i < len(s) && (s[i] == 'p' || s[i] == 'P') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		for i < len(s) && isDigit(s[i]) {
			gotExponent = true
			i++
			end = i
		}
	}

	floatStr := s[start:end]
	if !gotExponent {
		floatStr += "p0"
	}
	f, _ := strconv.ParseFloat(floatStr, 64)
	return f
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
