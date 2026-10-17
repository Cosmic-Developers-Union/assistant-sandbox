package regex

import "regexp"

type Regexp interface {
	String() string
	MatchString(s string) (bool, error)
	FindStringIndex(s string) ([]int, error)
	FindAllStringIndex(s string, n int) ([][]int, error)
	FindIndex(b []byte) ([]int, error)
	Split(s string, n int) ([]string, error)
	ReplaceAllStringFunc(s string, repl func(string) string) (string, error)
}

type Compiler interface {
	Compile(expr string) (Regexp, error)
}

type defaultCompiler struct{}

type defaultRegexp struct{ *regexp.Regexp }

func (r defaultRegexp) MatchString(s string) (bool, error) { return r.Regexp.MatchString(s), nil }
func (r defaultRegexp) FindStringIndex(s string) ([]int, error) {
	return r.Regexp.FindStringIndex(s), nil
}
func (r defaultRegexp) FindAllStringIndex(s string, n int) ([][]int, error) {
	return r.Regexp.FindAllStringIndex(s, n), nil
}
func (r defaultRegexp) FindIndex(b []byte) ([]int, error)       { return r.Regexp.FindIndex(b), nil }
func (r defaultRegexp) Split(s string, n int) ([]string, error) { return r.Regexp.Split(s, n), nil }
func (r defaultRegexp) ReplaceAllStringFunc(s string, repl func(string) string) (string, error) {
	return r.Regexp.ReplaceAllStringFunc(s, repl), nil
}

func (defaultCompiler) Compile(expr string) (Regexp, error) {
	norm, err := Normalize(expr)
	if err != nil {
		return nil, err
	}

	re, err := regexp.Compile("(?s:" + norm + ")")
	if err != nil {

		return nil, err
	}
	re.Longest()
	return defaultRegexp{re}, nil
}

func DefaultCompiler() Compiler {
	return defaultCompiler{}
}
