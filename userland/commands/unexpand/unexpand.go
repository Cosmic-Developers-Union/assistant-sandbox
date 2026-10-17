package unexpandcmd

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
	"github.com/mattn/go-runewidth"
)

var cmd = &toolcompat.Tool{
	Name:     "unexpand",
	Synopsis: "Convert blanks in each FILE to tabs, writing to standard output.\nBy default only leading blanks are converted. With -a, also convert all\nsequences of two or more blanks before a tab stop.\nWith no FILE, or when FILE is -, read standard input.",
	Usage:    "unexpand [OPTION]... [FILE]...",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

type ctypeProvider interface {
	IsBlank(byte) (bool, error)
	Close() error
}

var openCTypeFn = func(name string) (ctypeProvider, error) { return locale.Open(name) }

type columnModel struct {
	byteMode bool
	utf8     bool
	blank    [256]bool
	width    *runewidth.Condition
}

func legacyColumnModel(noUTF8 bool) *columnModel {
	m := &columnModel{byteMode: noUTF8}
	m.blank[' '], m.blank['\t'] = true, true
	return m
}

func loadColumnModel(env []string, posix, noUTF8 bool) (*columnModel, error) {
	if !posix || noUTF8 {
		return legacyColumnModel(noUTF8), nil
	}
	name := locale.Resolve(env, locale.CType)
	if name == "C" || name == "POSIX" {
		return legacyColumnModel(true), nil
	}
	if isUTF8Locale(name) {
		m := legacyColumnModel(false)
		m.utf8 = true
		m.width = runewidth.NewCondition()
		m.width.EastAsianWidth = isEastAsianLocale(name)
		return m, nil
	}
	p, err := openCTypeFn(name)
	if err != nil {
		return nil, fmt.Errorf("LC_CTYPE %q: %w", name, err)
	}
	m := &columnModel{byteMode: true}
	for i := 0; i < 256; i++ {
		ok, classifyErr := p.IsBlank(byte(i))
		if classifyErr != nil {
			err = classifyErr
			break
		}
		m.blank[i] = ok
	}
	if closeErr := p.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("LC_CTYPE %q: %w", name, err)
	}
	return m, nil
}

func isEastAsianLocale(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, "@cjk_narrow") {
		return false
	}
	if len(lower) < 2 || (lower[:2] != "ja" && lower[:2] != "ko" && lower[:2] != "zh") {
		return false
	}
	return len(lower) == 2 || strings.ContainsRune("_.-@", rune(lower[2]))
}

func isUTF8Locale(name string) bool {
	name, _, _ = strings.Cut(name, "@")
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		name = name[dot+1:]
	}
	name = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), "_", ""))
	return name == "UTF8"
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

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	tabsValue := fs.StringArrayP("tabs", "t", []string{"8"}, "have tabs N characters apart instead of 8 (enables -a); or use comma- or blank-separated LIST of explicit tab positions (repeatable; the last position may be prefixed with '/' for multiples or '+' for an increment)")
	all := fs.BoolP("all", "a", false, "convert all blanks, instead of just initial blanks")
	firstOnly := fs.BoolP("first-only", "f", false, "convert only leading sequences of blanks (overrides -a)")
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
	if len(operands) == 0 {
		operands = []string{"-"}
	}
	convertAll := *all || fs.Changed("tabs")
	if *firstOnly {
		convertAll = false
	}
	model, err := loadColumnModel(rc.Env, envPresent(rc.Env, "POSIXLY_CORRECT"), *noUTF8)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "unexpand: %v\n", err)
		return 1
	}

	out := bufio.NewWriter(rc.Out)
	status := 0
	for _, name := range operands {
		r, closer, err := openInput(rc, name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "unexpand: %s: %v\n", name, err)
			status = 1
			continue
		}
		if err := unexpandStreamModel(r, out, tabs, convertAll, model); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "unexpand: %s: %v\n", name, err)
			status = 1
		}
		if closer != nil {
			if err := closer.Close(); err != nil {
				status = 1
				_, _ = fmt.Fprintf(rc.Err, "unexpand: %s: %v\n", name, err)
			}
		}
	}
	if err := out.Flush(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "unexpand: write error: %v\n", err)
		return 1
	}
	return status
}

func unexpandStreamModel(r io.Reader, w io.Writer, tabs *tabStops, all bool, model *columnModel) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			var out string
			if model.byteMode {
				out = unexpandLineBytesModel(line, tabs, all, model)
			} else {
				out = unexpandLineModel(line, tabs, all, model)
			}
			if _, werr := io.WriteString(w, out); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func unexpandLineBytesModel(line string, tabs *tabStops, all bool, model *columnModel) string {
	return unexpandLineCore(line, tabs, all, func(i int) (string, int, bool, int) {
		b := line[i]
		return line[i : i+1], 1, model.blank[b], 1
	})
}

func unexpandLineModel(line string, tabs *tabStops, all bool, model *columnModel) string {
	return unexpandLineCore(line, tabs, all, func(i int) (string, int, bool, int) {
		ch, size := utf8.DecodeRuneInString(line[i:])
		blank := ch == ' ' || ch == '\t'
		width := 1
		if model.utf8 {
			blank = ch == '\t' || unicode.Is(unicode.Zs, ch)
			width = model.width.RuneWidth(ch)
			if width < 0 {
				width = 0
			}
		}
		return line[i : i+size], size, blank, width
	})
}

func unexpandLineCore(line string, tabs *tabStops, all bool, nextChar func(int) (string, int, bool, int)) string {
	var b strings.Builder
	var pending []string
	col := 0
	convert := true
	oneBlankBeforeStop := false
	prevBlank := true
	flush := func() {
		if len(pending) == 0 {
			return
		}
		if len(pending) > 1 && oneBlankBeforeStop {

			pending[0] = "\t"
		}
		for _, p := range pending {
			b.WriteString(p)
		}
		pending = pending[:0]
		oneBlankBeforeStop = false
	}
	for i := 0; i < len(line); {
		text, size, blank, charWidth := nextChar(i)
		i += size
		ch, _ := utf8.DecodeRuneInString(text)
		if !convert {
			b.WriteString(text)
			continue
		}
		writeCh := true
		if blank {
			next, last := tabs.next(col)
			switch {
			case last:

				convert = false
			case ch == '\t':
				col = next

				if len(pending) > 0 {
					pending[0] = "\t"
				}

				if oneBlankBeforeStop {
					pending = pending[:1]
				} else {
					pending = pending[:0]
				}
			default:
				col += charWidth
				if !prevBlank || col < next {
					if col == next {
						oneBlankBeforeStop = true
					}
					pending = append(pending, text)
					prevBlank = true
					continue
				}

				b.WriteByte('\t')
				if oneBlankBeforeStop {
					pending = pending[:1]
					pending[0] = "\t"
				} else {
					pending = pending[:0]
				}
				writeCh = false
			}
		} else if ch == '\b' {
			if col > 0 {
				col--
			}
		} else {
			col += charWidth
		}
		flush()
		prevBlank = blank
		if !all && !blank {
			convert = false
		}
		if writeCh {
			b.WriteString(text)
		}
	}
	flush()
	return b.String()
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
			digit := int(r - '0')
			if n > (int(^uint(0)>>1)-digit)/10 {
				return nil, fmt.Errorf("tab stop is too large %q", entry)
			}
			n = n*10 + digit
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
