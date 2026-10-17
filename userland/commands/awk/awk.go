package awkcmd

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/bre"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/interp"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/lexer"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/parser"
	awkregex "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/regex"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

var assignRegex = regexp.MustCompile(`^([_a-zA-Z][_a-zA-Z0-9]*)=`)

var cmd = &toolcompat.Tool{
	Name:     "awk",
	Synopsis: "Pattern scanning and text processing language, backed by pure-Go GoAWK.",
	Usage:    "awk [-F fs] [-v var=val] [-f progfile | 'program'] [file ...]",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int { return runWithLocales(rc, args) }

func runWithLocales(rc *toolcompat.RunContext, args []string) (status int) {
	fs := toolcompat.NewFlags(cmd.Name)

	fs.SetInterspersed(false)
	fieldSep := fs.StringP("field-separator", "F", "", "use fs for the input field separator")
	var assigns []string
	fs.StringArrayVarP(&assigns, "assign", "v", nil, "assign var=value before program execution")
	var progFiles []string
	fs.StringArrayVarP(&progFiles, "file", "f", nil, "read awk program source from progfile")
	operands, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}

	var source string
	var files []string
	if len(progFiles) > 0 {
		files = operands
	} else {
		if len(operands) == 0 {
			return toolcompat.UsageError(rc, cmd, "missing program")
		}
		source = operands[0]
		files = operands[1:]
	}

	vars := []string{}
	if fs.Changed("field-separator") {

		vars = append(vars, "FS", unescape(*fieldSep))
	}
	for _, assign := range assigns {
		if !assignRegex.MatchString(assign) {
			return toolcompat.UsageError(rc, cmd, "invalid -v assignment %q", assign)
		}
		name, value, _ := strings.Cut(assign, "=")
		vars = append(vars, name, unescape(value))
	}

	env := locale.StoreEnvAt(rc.Env, rc.Path)
	lcNumeric := locale.ResolveCarried(env, locale.Numeric)
	decimalPoint, ok := awkNumericDecimalPoint(lcNumeric)
	if !ok {
		_, _ = fmt.Fprintf(rc.Err, "awk: LC_NUMERIC %q: %v\n", lcNumeric, locale.ErrUnsupportedLocale)
		return 2
	}

	if _, err := locale.Open(locale.ResolveCarried(env, locale.CType)); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "awk: LC_CTYPE: %v\n", err)
		return 2
	}
	if _, err := locale.Open(locale.ResolveCarried(env, locale.Collate)); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "awk: LC_COLLATE: %v\n", err)
		return 2
	}
	lower, upper := asciiCaseMaps()

	if len(progFiles) > 0 {
		src, ok := readProgramFiles(rc, progFiles)
		if !ok {
			return 2
		}
		source = src
	}

	prog, err := parser.ParseProgram([]byte(source), &parser.ParserConfig{
		RegexCompiler: awkERECompiler{},
	})
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "awk: %v\n", err)
		return 2
	}

	argv0 := rc.InvocationName
	if argv0 == "" {
		argv0 = cmd.Name
	}
	status, err = interp.ExecProgram(prog, &interp.Config{
		Stdin:  readerOrEmpty(rc.In),
		Output: rc.Out,

		NewlineOutput: interp.RawNewlineMode,
		Error:         rc.Err,
		DecimalPoint:  decimalPoint,
		ToLower:       caseMapper(lower),
		ToUpper:       caseMapper(upper),
		Argv0:         argv0,
		Args:          resolveFiles(files),
		Vars:          vars,
		Environ:       environPairs(rc.Env),

		NoExec:       true,
		NoFileWrites: true,
		OpenFile:     interp.DenyHostOpenFile,

		OpenInput: openInputFile,
	})
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "awk: %v\n", err)
		return 2
	}
	return status
}

func openInputFile(name string) (io.ReadCloser, error) {
	return vfs.Open(name)
}

func awkNumericDecimalPoint(name string) (byte, bool) {
	if _, err := locale.Open(name); err != nil {
		return 0, false
	}
	return '.', true
}

func asciiCaseMaps() (lower, upper [256]byte) {
	for i := range 256 {
		lower[i], upper[i] = byte(i), byte(i)
	}
	for b := byte('A'); b <= 'Z'; b++ {
		lower[b] = b + ('a' - 'A')
	}
	for b := byte('a'); b <= 'z'; b++ {
		upper[b] = b - ('a' - 'A')
	}
	return lower, upper
}

func caseMapper(table [256]byte) interp.CaseMapFunc {
	return func(s string) (string, error) {
		mapped := []byte(s)
		for i := range mapped {
			mapped[i] = table[mapped[i]]
		}
		return string(mapped), nil
	}
}

type awkERECompiler struct{}

func (awkERECompiler) Compile(source string) (awkregex.Regexp, error) {
	re, err := bre.CompileEREWithFlags(source, "(?s)")
	if err != nil {
		return nil, err
	}
	re.Longest()
	return &awkERERegexp{source: source, re: re}, nil
}

type awkERERegexp struct {
	source string
	re     *bre.Regexp
}

func (r *awkERERegexp) String() string { return r.source }
func (r *awkERERegexp) MatchString(s string) (bool, error) {
	return r.re.MatchStringErr(s)
}
func (r *awkERERegexp) FindStringIndex(s string) ([]int, error) {
	return r.re.FindStringIndexErr(s)
}
func (r *awkERERegexp) FindAllStringIndex(s string, n int) ([][]int, error) {
	matches, err := r.re.FindAllStringSubmatchIndexErr(s, n)
	if err != nil {
		return nil, err
	}
	indices := make([][]int, len(matches))
	for i, match := range matches {
		indices[i] = match[:2]
	}
	return indices, nil
}
func (r *awkERERegexp) FindIndex(b []byte) ([]int, error) {
	return r.FindStringIndex(string(b))
}
func (r *awkERERegexp) Split(s string, n int) ([]string, error) {
	return splitRegexp(s, n, r.FindAllStringIndex)
}
func (r *awkERERegexp) ReplaceAllStringFunc(s string, repl func(string) string) (string, error) {
	return replaceRegexp(s, repl, r.FindAllStringIndex)
}

type findAllFunc func(string, int) ([][]int, error)

func splitRegexp(s string, n int, findAll findAllFunc) ([]string, error) {
	if n == 0 {
		return nil, nil
	}
	if s == "" {
		return []string{""}, nil
	}
	matches, err := findAll(s, n)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(matches)+1)
	begin, end := 0, 0
	for _, match := range matches {

		if match[0] == match[1] {
			continue
		}
		if n > 0 && len(parts) >= n-1 {
			break
		}
		end = match[0]
		parts = append(parts, s[begin:end])
		begin = match[1]
	}
	if end != len(s) {
		parts = append(parts, s[begin:])
	}
	return parts, nil
}

func replaceRegexp(s string, repl func(string) string, findAll findAllFunc) (string, error) {
	matches, err := findAll(s, -1)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	last := 0
	for _, match := range matches {
		b.WriteString(s[last:match[0]])
		b.WriteString(repl(s[match[0]:match[1]]))
		last = match[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

func readProgramFiles(rc *toolcompat.RunContext, names []string) (string, bool) {
	var b strings.Builder
	for i, name := range names {
		data, err := readProgramFile(rc, name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "awk: %s: %v\n", name, err)
			return "", false
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.Write(data)
	}
	return b.String(), true
}

func readProgramFile(rc *toolcompat.RunContext, name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(readerOrEmpty(rc.In))
	}
	file, err := vfs.Open(name)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return data, err
}

func resolveFiles(files []string) []string {
	return append([]string(nil), files...)
}

func unescape(s string) string {
	u, err := lexer.Unescape(s)
	if err != nil {
		return s
	}
	return u
}

func environPairs(env []string) []string {
	pairs := make([]string, 0, len(env)*2)
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		pairs = append(pairs, name, value)
	}
	return pairs
}

func readerOrEmpty(r io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return strings.NewReader("")
}
