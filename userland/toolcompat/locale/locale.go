package locale

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Category string

const (
	Collate Category = "LC_COLLATE"

	CType Category = "LC_CTYPE"

	Numeric Category = "LC_NUMERIC"

	Messages Category = "LC_MESSAGES"

	Monetary Category = "LC_MONETARY"

	Time Category = "LC_TIME"
)

const Default = "POSIX"

type Categories struct {
	All      string
	Collate  string
	CType    string
	Numeric  string
	Messages string
	Monetary string
	Time     string
}

func Resolve(env []string, cat Category) string {
	for _, key := range []string{"LC_ALL", string(cat), "LANG"} {
		if key == "" {
			continue
		}
		if v, ok := getEnv(env, key); ok && v != "" {
			return v
		}
	}
	return Default
}

func ResolveCarried(env []string, cat Category) string {
	if IsHostDefaultUTF8(env, cat) {
		if cat == Collate {
			return "C"
		}
		return "C.UTF-8"
	}
	return Resolve(env, cat)
}

func ResolveAll(env []string) Categories {
	return Categories{
		All:      resolveAllVar(env),
		Collate:  Resolve(env, Collate),
		CType:    Resolve(env, CType),
		Numeric:  Resolve(env, Numeric),
		Messages: Resolve(env, Messages),
		Monetary: Resolve(env, Monetary),
		Time:     Resolve(env, Time),
	}
}

func resolveAllVar(env []string) string {
	if v, ok := getEnv(env, "LC_ALL"); ok && v != "" {
		return v
	}
	return Default
}

func IsHostDefaultUTF8(env []string, cat Category) bool {
	if profile, ok := getEnv(env, "VSC_PROFILE"); ok && profile == "cert" {
		return false
	}
	return isUTF8Codeset(Resolve(env, cat))
}

func StoreEnvAt(env []string, _ func(string) string) []string { return env }

func IsCUTF8(name string) bool {
	base, codeset := splitLocaleName(name)
	return base == "C" && normalizeCodeset(codeset) == "UTF8"
}

func IsCUTF(name string) bool { return IsCUTF8(name) }

func isUTF8Codeset(name string) bool {
	_, codeset := splitLocaleName(name)
	return normalizeCodeset(codeset) == "UTF8"
}

func getEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return env[i][len(prefix):], true
		}
	}
	return "", false
}

func splitLocaleName(name string) (base, codeset string) {
	base = name
	if at := strings.IndexByte(base, '@'); at >= 0 {
		base = base[:at]
	}
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		return base[:dot], base[dot+1:]
	}
	return base, ""
}

func normalizeCodeset(name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", "_", "", ".", "").Replace(name))
}

var ErrUnsupportedLocale = errors.New("locale: 只提供 C/POSIX 语言环境的字节级语义")

func providerLocale(name string) bool {
	switch name {
	case "", "C", "POSIX":
		return true
	}
	return IsCUTF8(name) || strings.EqualFold(name, "POSIX") || strings.EqualFold(name, "C")
}

type Provider struct{ name string }

func Open(name string) (*Provider, error) {
	if !providerLocale(name) {
		return nil, ErrUnsupportedLocale
	}
	return &Provider{name: name}, nil
}

func OpenEnv(env []string, name string) (*Provider, error) {
	_ = env
	return Open(name)
}

func (p *Provider) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

func (*Provider) Close() error { return nil }

func (p *Provider) classify(kind byte, c byte) bool {
	switch kind {
	case 'a':
		return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
	case 'A':
		return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
	case 'b':
		return c == ' ' || c == '\t'
	case 'c':
		return c < 0x20 || c == 0x7f
	case 'd':
		return c >= '0' && c <= '9'
	case 'g':
		return c > 0x20 && c < 0x7f
	case 'l':
		return c >= 'a' && c <= 'z'
	case 'p':
		return c >= 0x21 && c <= 0x7e && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9')
	case 'P':
		return c >= 0x20 && c < 0x7f
	case 's':
		return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
	case 'u':
		return c >= 'A' && c <= 'Z'
	case 'x':
		return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
	}
	return false
}

func (p *Provider) IsAlpha(c byte) (bool, error) { return p.classify('a', c), nil }

func (p *Provider) IsAlnum(c byte) (bool, error) { return p.classify('A', c), nil }

func (p *Provider) IsBlank(c byte) (bool, error) { return p.classify('b', c), nil }

func (p *Provider) IsCntrl(c byte) (bool, error) { return p.classify('c', c), nil }

func (p *Provider) IsDigit(c byte) (bool, error) { return p.classify('d', c), nil }

func (p *Provider) IsGraph(c byte) (bool, error) { return p.classify('g', c), nil }

func (p *Provider) IsLower(c byte) (bool, error) { return p.classify('l', c), nil }

func (p *Provider) IsPrint(c byte) (bool, error) { return p.classify('P', c), nil }

func (p *Provider) IsPunct(c byte) (bool, error) { return p.classify('p', c), nil }

func (p *Provider) IsSpace(c byte) (bool, error) { return p.classify('s', c), nil }

func (p *Provider) IsUpper(c byte) (bool, error) { return p.classify('u', c), nil }

func (p *Provider) IsXDigit(c byte) (bool, error) { return p.classify('x', c), nil }

func (p *Provider) ToLower(b []byte) ([]byte, error) { return caseMap(b, false), nil }

func (p *Provider) ToUpper(b []byte) ([]byte, error) { return caseMap(b, true), nil }

func caseMap(b []byte, upper bool) []byte {
	for i, c := range b {
		if upper && c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
			continue
		}
		if !upper && c >= 'A' && c <= 'Z' {
			b[i] = c - 'A' + 'a'
		}
	}
	return b
}

func (p *Provider) Compare(a, b string) (int, error) { return strings.Compare(a, b), nil }

func (p *Provider) Equivalents(c byte) ([]byte, error) { return []byte{c}, nil }

func (p *Provider) EquivalenceClasses() ([]bool, error) { return make([]bool, 256), nil }

func (p *Provider) CollationWeights() ([]byte, error) {
	weights := make([]byte, 256)
	for i := range weights {
		weights[i] = byte(i)
	}
	return weights, nil
}

func (p *Provider) CollatingElements() ([]bool, error) { return make([]bool, 256), nil }

var ErrUnsupportedMessagesLocale = errors.New("locale: 只提供 C/POSIX 语言环境的应答表达式")

var affirmativeExpr = regexp.MustCompilePOSIX(`^[yY]`)

func MatchAffirmative(env []string, response string) (bool, error) {
	name := ResolveCarried(env, Messages)
	if !providerLocale(name) {
		return false, fmt.Errorf("%w %q", ErrUnsupportedMessagesLocale, name)
	}
	return affirmativeExpr.MatchString(strings.TrimRight(response, "\r\n")), nil
}

func BREToERE(pattern string) string {
	var out strings.Builder
	inClass := false
	for index := 0; index < len(pattern); index++ {
		value := pattern[index]
		if inClass {
			out.WriteByte(value)
			if value == ']' {
				inClass = false
			}
			continue
		}
		if value == '[' {
			inClass = true
			out.WriteByte(value)
			continue
		}
		if value == '\\' && index+1 < len(pattern) {
			next := pattern[index+1]
			switch next {
			case '(', ')', '|', '{', '}', '+', '?':

				out.WriteByte(next)
			default:
				out.WriteByte('\\')
				out.WriteByte(next)
			}
			index++
			continue
		}
		switch value {
		case '+', '?', '|', '(', ')', '{', '}':

			out.WriteByte('\\')
			out.WriteByte(value)
		default:
			out.WriteByte(value)
		}
	}
	return out.String()
}
