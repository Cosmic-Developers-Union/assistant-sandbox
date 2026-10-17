package expandcmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
	"github.com/mattn/go-runewidth"
)

var cmd = &toolcompat.Tool{
	Name:     "expand",
	Synopsis: "Convert tabs in each FILE to spaces, writing to standard output.\nWith no FILE, or when FILE is -, read standard input.",
	Usage:    "expand [OPTION]... [FILE]...",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

type encodingMode int

const (
	encodingSingleByte encodingMode = iota
	encodingLatin1
	encodingUTF8
)

type characterModel struct {
	encoding encodingMode
	width    *runewidth.Condition
}

func resolveCharacterModel(env []string) (*characterModel, error) {
	name := locale.Resolve(env, locale.CType)
	base, codeset := splitLocaleName(name)
	switch {
	case (base == "C" || base == "POSIX") && codeset == "":
		return &characterModel{encoding: encodingSingleByte}, nil
	case (base == "C" || base == "POSIX") && normalizeCodeset(codeset) == "UTF8":
		return &characterModel{encoding: encodingUTF8, width: runewidth.NewCondition()}, nil
	case strings.EqualFold(base, "de_DE") && normalizeCodeset(codeset) == "ISO88591":
		return &characterModel{encoding: encodingLatin1}, nil
	default:
		return nil, fmt.Errorf(
			"LC_CTYPE %q is unavailable; supported locales are C/POSIX, their UTF-8 aliases, and de_DE.ISO-8859-1",
			name,
		)
	}
}

func splitLocaleName(name string) (base, codeset string) {
	name, _, _ = strings.Cut(name, "@")
	base, codeset, _ = strings.Cut(name, ".")
	return base, codeset
}

func normalizeCodeset(name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", "_", "").Replace(name))
}

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	tabsValue := fs.StringArrayP("tabs", "t", []string{"8"}, "have tabs N characters apart, not 8; or use comma- or blank-separated LIST of explicit tab positions (repeatable; the last position may be prefixed with '/' for multiples or '+' for an increment)")
	initial := fs.BoolP("initial", "i", false, "do not convert tabs after non blanks")
	noUTF8 := fs.BoolP("no-utf8", "U", false, "interpret input bytes as columns instead of UTF-8 characters")
	var operands []string
	var code int
	if envPresent(rc.Env, "POSIXLY_CORRECT") {

		operands, code = toolcompat.ParseRequireOrder(rc, cmd, fs, args)
	} else {

		operands, code = toolcompat.Parse(rc, cmd, fs, args)
	}
	if code >= 0 {
		return code
	}
	tabs, err := parseTabStops(*tabsValue)
	if err != nil {
		return toolcompat.UsageError(rc, cmd, "%v", err)
	}

	model, err := resolveCharacterModel(rc.Env)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "expand: %v\n", err)
		return 1
	}
	if *noUTF8 {

		model = &characterModel{encoding: encodingSingleByte}
	}
	if len(operands) == 0 {
		operands = []string{"-"}
	}

	out := bufio.NewWriter(rc.Out)
	status := 0
	posix := envPresent(rc.Env, "POSIXLY_CORRECT")
	for _, name := range operands {
		r, closer, err := openInput(rc, name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "expand: %s: %v\n", name, err)
			status = 1
			if posix {
				break
			}
			continue
		}
		err = expandStreamModel(r, out, tabs, *initial, model)
		if closer != nil {
			err = errors.Join(err, closer.Close())
		}
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "expand: %s: %v\n", name, err)
			status = 1
			if posix {
				break
			}
		}
	}
	if err := out.Flush(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "expand: write error: %v\n", err)
		return 1
	}
	return status
}

func envPresent(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

type inputCharacter struct {
	raw   []byte
	rune  rune
	valid bool
}

type characterReader struct {
	r     *bufio.Reader
	model *characterModel
}

func (r *characterReader) next() (inputCharacter, error) {
	if r.model.encoding != encodingUTF8 {
		b, err := r.r.ReadByte()
		if err != nil {
			return inputCharacter{}, err
		}
		return inputCharacter{raw: []byte{b}, rune: rune(b), valid: true}, nil
	}
	ch, size, err := r.r.ReadRune()
	if err != nil {
		return inputCharacter{}, err
	}
	if err := r.r.UnreadRune(); err != nil {
		return inputCharacter{}, err
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(r.r, raw); err != nil {
		return inputCharacter{}, err
	}
	valid := ch != utf8.RuneError || size > 1
	return inputCharacter{raw: raw, rune: ch, valid: valid}, nil
}

func expandStreamModel(
	r io.Reader, w io.Writer, tabs *tabStops, initial bool,
	model *characterModel,
) error {
	reader := &characterReader{r: bufio.NewReader(r), model: model}
	col := 0
	convert := true
	scratch := make([]byte, 0, 64)
	for {
		ch, err := reader.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case len(ch.raw) == 1 && ch.raw[0] == '\t' && convert:
			next, _ := tabs.next(col)
			scratch = scratch[:0]
			for i := col; i < next; i++ {
				scratch = append(scratch, ' ')
			}
			if err := writeFull(w, scratch); err != nil {
				return err
			}
			col = next
		case len(ch.raw) == 1 && ch.raw[0] == '\n':
			if err := writeFull(w, []byte{'\n'}); err != nil {
				return err
			}
			col = 0
			convert = true
		default:
			if err := writeFull(w, ch.raw); err != nil {
				return err
			}
			if !convert {
				continue
			}
			if len(ch.raw) == 1 && ch.raw[0] == '\b' {
				if col > 0 {
					col--
				}
			} else {
				col += charWidth(model, ch)
			}

			if initial && !isBlank(ch) {
				convert = false
			}
		}
	}
}

func isBlank(ch inputCharacter) bool {
	return len(ch.raw) == 1 && (ch.raw[0] == ' ' || ch.raw[0] == '\t')
}

func charWidth(model *characterModel, ch inputCharacter) int {
	if model.encoding != encodingUTF8 || !ch.valid {
		return 1
	}
	w := model.width.RuneWidth(ch.rune)
	if w < 0 {
		return 1
	}
	return w
}

func writeFull(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		return io.ErrShortWrite
	}
	return err
}

type tabStops struct {
	size      int
	stops     []int
	extend    int
	increment int
}

func parseTabStops(list []string) (*tabStops, error) {
	ts := &tabStops{}
	var entries []string
	for _, value := range list {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			fields := strings.Fields(part)
			if len(fields) == 0 {
				return nil, fmt.Errorf("tab size contains invalid character(s): %q", value)
			}
			entries = append(entries, fields...)
		}
	}
	for i, entry := range entries {
		e := entry
		var spec byte
		if e[0] == '/' || e[0] == '+' {
			spec = e[0]
			e = e[1:]
		}
		n := 0
		if e == "" {
			return nil, fmt.Errorf("tab size contains invalid character(s): %q", entry)
		}
		for _, r := range e {
			if r < '0' || r > '9' {
				return nil, fmt.Errorf("tab size contains invalid character(s): %q", entry)
			}
			if n > (1<<30)/10 {
				return nil, fmt.Errorf("tab stop is too large %q", entry)
			}
			n = n*10 + int(r-'0')
			if n > 1<<30 {
				return nil, fmt.Errorf("tab stop is too large %q", entry)
			}
		}
		if n == 0 {
			return nil, fmt.Errorf("tab size cannot be 0")
		}
		switch spec {
		case '/':
			if i != len(entries)-1 {
				return nil, fmt.Errorf("'/' specifier only allowed with the last value")
			}
			ts.extend = n
		case '+':
			if i != len(entries)-1 {
				return nil, fmt.Errorf("'+' specifier only allowed with the last value")
			}
			ts.increment = n
		default:
			if len(ts.stops) > 0 && n <= ts.stops[len(ts.stops)-1] {
				return nil, fmt.Errorf("tab sizes must be ascending")
			}
			ts.stops = append(ts.stops, n)
		}
	}

	if len(ts.stops) == 0 {
		switch {
		case ts.extend > 0:
			ts.size, ts.extend = ts.extend, 0
		case ts.increment > 0:
			ts.size, ts.increment = ts.increment, 0
		default:
			ts.size = 8
		}
	} else if len(ts.stops) == 1 && ts.extend == 0 && ts.increment == 0 {
		ts.size = ts.stops[0]
		ts.stops = nil
	}
	return ts, nil
}

func (ts *tabStops) next(col int) (stop int, last bool) {
	if ts.size > 0 {
		return col + ts.size - col%ts.size, false
	}
	for _, s := range ts.stops {
		if s > col {
			return s, false
		}
	}
	if ts.extend > 0 {
		return col + ts.extend - col%ts.extend, false
	}
	if ts.increment > 0 {
		end := ts.stops[len(ts.stops)-1]
		return col + ts.increment - (col-end)%ts.increment, false
	}
	return col + 1, true
}

func openInput(rc *toolcompat.RunContext, name string) (io.Reader, io.Closer, error) {
	if name == "-" {
		if rc.In == nil {
			return strings.NewReader(""), nil, nil
		}
		return rc.In, nil, nil
	}
	f, err := vfs.Open(rc.Path(name))
	if err != nil {
		return nil, nil, err
	}
	return f, f, nil
}
