package nlcmd

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"regexp"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
)

var cmd = &toolcompat.Tool{
	Name:     "nl",
	Synopsis: "Number lines of FILE(s), or standard input.",
	Usage:    "nl [OPTION]... [FILE]...",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

type options struct {
	bodyStyle    string
	headerStyle  string
	footerStyle  string
	format       string
	separator    string
	width        int
	start        int
	increment    int
	noRenumber   bool
	delimiter    string
	sectionMatch bool
	blankJoin    int
	bodyRE       *regexp.Regexp
	headerRE     *regexp.Regexp
	footerRE     *regexp.Regexp
}

type numberState struct {
	lineNo   int
	section  string
	blankRun int
}

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	bodyStyle := fs.StringP("body-numbering", "b", "t", "use STYLE for numbering body lines: a (all), t (nonempty), n (none), pBRE (matching lines)")
	headerStyle := fs.StringP("header-numbering", "h", "n", "use STYLE for numbering header lines: a, t, n, pBRE")
	footerStyle := fs.StringP("footer-numbering", "f", "n", "use STYLE for numbering footer lines: a, t, n, pBRE")
	format := fs.StringP("number-format", "n", "rn", "insert line numbers according to FORMAT: ln, rn, rz")
	separator := fs.StringP("number-separator", "s", "\t", "add SEP after each line number")
	width := fs.IntP("number-width", "w", 6, "use WIDTH columns for line numbers")
	start := fs.IntP("starting-line-number", "v", 1, "first line number for each section")
	increment := fs.IntP("line-increment", "i", 1, "line number increment at each line (may be negative)")
	noRenumber := fs.BoolP("no-renumber", "p", false, "do not reset line numbers for each section")
	delimiter := fs.StringP("section-delimiter", "d", "\\:", "use CC for logical page delimiters; empty CC disables section matching")
	blankJoin := fs.IntP("join-blank-lines", "l", 1, "group of NUMBER empty lines counted as one")
	operands, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	o := options{
		bodyStyle: *bodyStyle, headerStyle: *headerStyle, footerStyle: *footerStyle,
		format: *format, separator: *separator, width: *width,
		start: *start, increment: *increment, noRenumber: *noRenumber,
		blankJoin: *blankJoin,
	}
	var err error
	if o.bodyRE, err = validateStyle("body", o.bodyStyle); err != nil {
		return toolcompat.UsageError(rc, cmd, "%v", err)
	}
	if o.headerRE, err = validateStyle("header", o.headerStyle); err != nil {
		return toolcompat.UsageError(rc, cmd, "%v", err)
	}
	if o.footerRE, err = validateStyle("footer", o.footerStyle); err != nil {
		return toolcompat.UsageError(rc, cmd, "%v", err)
	}
	if o.format != "ln" && o.format != "rn" && o.format != "rz" {
		return toolcompat.UsageError(rc, cmd, "invalid line numbering format: %q", o.format)
	}
	if o.width < 1 {
		return toolcompat.UsageError(rc, cmd, "invalid line number field width: %d", o.width)
	}
	if o.blankJoin <= 0 {
		return toolcompat.UsageError(rc, cmd, "invalid blank-line join count: %d", o.blankJoin)
	}

	switch len(*delimiter) {
	case 0:
		o.sectionMatch = false
	case 1:
		o.delimiter = *delimiter + ":"
		o.sectionMatch = true
	default:
		o.delimiter = *delimiter
		o.sectionMatch = true
	}

	files := operands
	if len(files) == 0 {
		files = []string{"-"}
	}
	w := bufio.NewWriter(rc.Out)
	st := &numberState{lineNo: o.start, section: "body"}
	exit := 0
	for _, name := range files {
		r, closer, err := open(rc, name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "nl: %s: %v\n", name, toolcompat.SysErr(err))
			exit = 1
			continue
		}
		if err := number(r, w, o, st); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "nl: %s: %v\n", name, toolcompat.SysErr(err))
			exit = 1
		}
		if closer != nil {
			if err := closer.Close(); err != nil {
				exit = 1
				_, _ = fmt.Fprintf(rc.Err, "nl: %s: %v\n", name, err)
			}
		}
	}
	if err := w.Flush(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "nl: write error: %v\n", err)
		return 1
	}
	return exit
}

func validateStyle(section, style string) (*regexp.Regexp, error) {
	switch style {
	case "a", "t", "n":
		return nil, nil
	}
	if strings.HasPrefix(style, "p") && len(style) > 1 {
		re, err := regexp.Compile(locale.BREToERE(style[1:]))
		if err != nil {
			return nil, fmt.Errorf("invalid %s numbering regexp: %q", section, style[1:])
		}
		return re, nil
	}
	return nil, fmt.Errorf("invalid %s numbering style: %q", section, style)
}

func open(rc *toolcompat.RunContext, name string) (io.Reader, io.Closer, error) {
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

func number(r io.Reader, w *bufio.Writer, o options, st *numberState) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			if next, delimiter := sectionDelimiter(line, o); delimiter {
				st.section = next
				if !o.noRenumber {
					st.lineNo = o.start
				}

				if _, werr := w.WriteString("\n"); werr != nil {
					return werr
				}
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				continue
			}
			style := o.styleFor(st.section)
			should, matchErr := st.shouldNumber(style, o.regexpFor(st.section), line, o.blankJoin)
			if matchErr != nil {
				return matchErr
			}
			if should {
				if _, werr := fmt.Fprint(w, formatNumber(st.lineNo, o)); werr != nil {
					return werr
				}
				st.lineNo += o.increment
			} else {

				if _, werr := fmt.Fprintf(w, "%*s", o.width+len(o.separator), ""); werr != nil {
					return werr
				}
			}
			if _, werr := w.WriteString(line); werr != nil {
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

func (o options) styleFor(section string) string {
	switch section {
	case "header":
		return o.headerStyle
	case "footer":
		return o.footerStyle
	default:
		return o.bodyStyle
	}
}

func (o options) regexpFor(section string) *regexp.Regexp {
	switch section {
	case "header":
		return o.headerRE
	case "footer":
		return o.footerRE
	default:
		return o.bodyRE
	}
}

func sectionDelimiter(line string, o options) (string, bool) {
	if !o.sectionMatch {
		return "", false
	}
	text := strings.TrimRight(line, "\n")
	switch text {
	case o.delimiter + o.delimiter + o.delimiter:
		return "header", true
	case o.delimiter + o.delimiter:
		return "body", true
	case o.delimiter:
		return "footer", true
	default:
		return "", false
	}
}

func (st *numberState) shouldNumber(style string, re *regexp.Regexp, line string, blankJoin int) (bool, error) {
	text := strings.TrimRight(line, "\n")
	blank := text == ""
	switch style {
	case "a":
		if blankJoin > 1 {
			if !blank {
				st.blankRun = 0
				return true, nil
			}
			st.blankRun++
			if st.blankRun == blankJoin {
				st.blankRun = 0
				return true, nil
			}
			return false, nil
		}
		return true, nil
	case "t":
		return !blank, nil
	case "n":
		return false, nil
	default:
		if re == nil {
			return false, nil
		}
		return re.MatchString(text), nil
	}
}

func formatNumber(n int, o options) string {
	switch o.format {
	case "ln":
		return fmt.Sprintf("%-*d%s", o.width, n, o.separator)
	case "rz":
		return fmt.Sprintf("%0*d%s", o.width, n, o.separator)
	default:
		return fmt.Sprintf("%*d%s", o.width, n, o.separator)
	}
}
