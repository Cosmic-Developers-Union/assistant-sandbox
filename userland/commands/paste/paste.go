package pastecmd

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
)

var cmd = &toolcompat.Tool{
	Name:     "paste",
	Synopsis: "Write lines consisting of the sequentially corresponding lines from each FILE, separated by TABs, to standard output.",
	Usage:    "paste [OPTION]... [FILE]...",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	posix := pastePOSIXMode(rc.Env)
	delims := fs.StringP("delimiters", "d", "\t", "reuse characters from LIST instead of TABs")
	serial := fs.BoolP("serial", "s", false, "paste one file at a time instead of in parallel")
	zero := fs.BoolP("zero-terminated", "z", false, "line delimiter is NUL, not newline")
	if !posix {
		args = toolcompat.AliasHelpVersion(args)
	}
	var operands []string
	var code int
	if posix {
		operands, code = toolcompat.ParseRequireOrder(rc, cmd, fs, args)
	} else {
		operands, code = toolcompat.Parse(rc, cmd, fs, args)
	}
	if code >= 0 {
		return code
	}

	model, err := resolveCharacterModel(rc.Env)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "paste: %v\n", err)
		return 1
	}
	dl, errMsg := parseDelims(*delims, model)
	if errMsg != "" {
		_, _ = fmt.Fprintf(rc.Err, "paste: %s\n", errMsg)
		return 1
	}
	if len(operands) == 0 {
		operands = []string{"-"}
	}

	var stdinR *bufio.Reader
	open := func(name string) (*bufio.Reader, io.Closer, error) {
		if name == "-" {
			if stdinR == nil {
				stdinR = bufio.NewReader(rc.In)
			}
			return stdinR, nil, nil
		}
		f, err := vfs.Open(rc.Path(name))
		if err != nil {
			return nil, nil, err
		}
		return bufio.NewReader(f), f, nil
	}

	out := bufio.NewWriter(rc.Out)
	dc := &delimCycle{list: dl}
	var status int
	lineEnd := byte('\n')
	if *zero {
		lineEnd = 0
	}
	if *serial {
		status = pasteSerial(rc, operands, open, dc, out, lineEnd)
	} else {
		status = pasteParallel(rc, operands, open, dc, out, lineEnd)
	}
	if err := out.Flush(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "paste: write error: %v\n", err)
		status = 1
	}
	return status
}

func pastePOSIXMode(env []string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, "POSIXLY_CORRECT=") {
			return true
		}
	}
	return false
}

func parseDelims(list string, model *characterModel) ([][]byte, string) {
	if list == "" {
		return nil, "no delimiters specified"
	}

	var out [][]byte
	for i := 0; i < len(list); i++ {
		c := list[i]
		if c != '\\' {
			size := characterSize(list[i:], model)
			out = append(out, []byte(list[i:i+size]))
			i += size - 1
			continue
		}
		i++
		if i >= len(list) {
			return nil, fmt.Sprintf("delimiter list ends with an unescaped backslash: %s", list)
		}
		switch list[i] {
		case '0':
			out = append(out, nil)
		case 'b':
			out = append(out, []byte{'\b'})
		case 'f':
			out = append(out, []byte{'\f'})
		case 'n':
			out = append(out, []byte{'\n'})
		case 'r':
			out = append(out, []byte{'\r'})
		case 't':
			out = append(out, []byte{'\t'})
		case 'v':
			out = append(out, []byte{'\v'})
		case '\\':
			out = append(out, []byte{'\\'})
		default:
			size := characterSize(list[i:], model)
			out = append(out, []byte(list[i:i+size]))
			i += size - 1
		}
	}
	return out, ""
}

type encodingMode int

const (
	encodingSingleByte encodingMode = iota

	encodingUTF8
)

type characterModel struct {
	encoding encodingMode
}

func resolveCharacterModel(env []string) (*characterModel, error) {
	name := locale.Resolve(env, locale.CType)
	base, codeset := splitLocaleName(name)
	switch {
	case (base == "C" || base == "POSIX") && codeset == "":
		return &characterModel{encoding: encodingSingleByte}, nil
	case (base == "C" || base == "POSIX") && normalizeCodeset(codeset) == "UTF8":
		return &characterModel{encoding: encodingUTF8}, nil
	case strings.EqualFold(base, "de_DE") && normalizeCodeset(codeset) == "ISO88591":
		return &characterModel{encoding: encodingSingleByte}, nil
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

func characterSize(s string, model *characterModel) int {
	if model.encoding != encodingUTF8 {
		return 1
	}
	_, size := utf8.DecodeRuneInString(s)
	return size
}

type delimCycle struct {
	list [][]byte
	idx  int
	last int
}

func (d *delimCycle) write(buf *[]byte) {
	if len(d.list) == 0 {
		d.last = 0
		return
	}
	cur := d.list[d.idx]
	d.idx = (d.idx + 1) % len(d.list)
	*buf = append(*buf, cur...)
	d.last = len(cur)
}

func (d *delimCycle) trimTrailing(buf *[]byte) {
	*buf = (*buf)[:len(*buf)-d.last]
	d.last = 0
}

func (d *delimCycle) reset() {
	d.idx = 0
	d.last = 0
}

type opener func(name string) (*bufio.Reader, io.Closer, error)

func pasteParallel(rc *toolcompat.RunContext, names []string, open opener, dc *delimCycle, out *bufio.Writer, lineEnd byte) (status int) {
	type input struct {
		r *bufio.Reader
		c io.Closer
	}
	ins := make([]input, 0, len(names))
	closeAll := func() error {
		var closeErr error
		for _, in := range ins {
			if in.c != nil {
				closeErr = errors.Join(closeErr, in.c.Close())
			}
		}
		return closeErr
	}

	for _, name := range names {
		r, c, err := open(name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "paste: %s: %v\n", name, pathErr(err))
			if closeErr := closeAll(); closeErr != nil {
				_, _ = fmt.Fprintf(rc.Err, "paste: close input: %v\n", closeErr)
			}
			return 1
		}
		ins = append(ins, input{r, c})
	}
	defer func() {
		if err := closeAll(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "paste: close input: %v\n", err)
			status = 1
		}
	}()

	eof := make([]bool, len(ins))
	var buf []byte
	for {
		buf = buf[:0]
		dc.reset()
		eofCount := 0
		for i, in := range ins {
			if eof[i] {
				eofCount++
			} else {
				chunk, err := in.r.ReadBytes(lineEnd)
				if len(chunk) == 0 {
					eof[i] = true
					eofCount++
					if err != nil && !errors.Is(err, io.EOF) {
						_, _ = fmt.Fprintf(rc.Err, "paste: %s: %v\n", names[i], err)
						return 1
					}
				} else {
					if chunk[len(chunk)-1] == lineEnd {
						chunk = chunk[:len(chunk)-1]
					}
					buf = append(buf, chunk...)
				}
			}
			dc.write(&buf)
		}
		if eofCount == len(ins) {
			return 0
		}
		dc.trimTrailing(&buf)
		buf = append(buf, lineEnd)
		if _, err := out.Write(buf); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "paste: write error: %v\n", err)
			return 1
		}
	}
}

func pasteSerial(rc *toolcompat.RunContext, names []string, open opener, dc *delimCycle, out *bufio.Writer, lineEnd byte) int {
	status := 0
	var buf []byte
	for _, name := range names {
		r, closer, err := open(name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "paste: %s: %v\n", name, pathErr(err))
			status = 1
			continue
		}
		buf = buf[:0]
		dc.reset()
		failed := false
		for {
			chunk, rerr := r.ReadBytes(lineEnd)
			if len(chunk) == 0 {
				if rerr != nil && !errors.Is(rerr, io.EOF) {
					_, _ = fmt.Fprintf(rc.Err, "paste: %s: %v\n", name, rerr)
					status = 1
					failed = true
				}
				break
			}
			if chunk[len(chunk)-1] == lineEnd {
				chunk = chunk[:len(chunk)-1]
			}
			buf = append(buf, chunk...)
			dc.write(&buf)
		}

		if failed {
			if closer != nil {
				if err := closer.Close(); err != nil {
					_, _ = fmt.Fprintf(rc.Err, "paste: %s: close input: %v\n", name, err)
					status = 1
				}
			}
			continue
		}
		dc.trimTrailing(&buf)
		buf = append(buf, lineEnd)
		if _, err := out.Write(buf); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "paste: write error: %v\n", err)
			status = 1
		}
		if closer != nil {
			if err := closer.Close(); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "paste: %s: close input: %v\n", name, err)
				status = 1
			}
		}
	}
	return status
}

func pathErr(err error) string {
	return toolcompat.SysErrString(err)
}
