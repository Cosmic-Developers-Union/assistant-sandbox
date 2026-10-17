package stringscmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
)

var cmd = &toolcompat.Tool{
	Name:     "strings",
	Synopsis: "Print the sequences of printable characters in files.\nWith no FILE, read standard input.",
	Usage:    "strings [OPTION]... [FILE]...",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	minLen := fs.IntP("bytes", "n", 4, "locate and print any sequence of at least NUMBER printable characters")
	radix := fs.StringP("radix", "t", "", "print the offset within the file before each string; RADIX is o (octal), d (decimal) or x (hexadecimal)")
	_ = fs.BoolP("all", "a", false, "scan the entire file, not just the default sections (always true in this implementation)")
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

	if *minLen < 1 {
		return toolcompat.UsageError(rc, cmd, "invalid minimum string length %d", *minLen)
	}
	switch *radix {
	case "", "o", "d", "x":
	default:
		return toolcompat.UsageError(rc, cmd, "invalid radix: %q (must be o, d or x)", *radix)
	}

	isPrint, isUTF8, err := getPrintableFunc(rc)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "strings: %v\n", err)
		return 1
	}

	w := bufio.NewWriter(rc.Out)
	posixOffsets := envPresent(rc.Env, "POSIXLY_CORRECT")
	exit := 0
	if len(operands) == 0 {
		var in = rc.In
		if in == nil {
			in = strings.NewReader("")
		}
		if err := scan(in, w, *minLen, *radix, isPrint, isUTF8, posixOffsets); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "strings: %v\n", err)
			exit = 1
		}
	}
	for _, name := range operands {
		f, err := vfs.Open(rc.Path(name))
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "strings: %s: %v\n", name, sysErr(err))
			exit = 1
			continue
		}
		err = scan(f, w, *minLen, *radix, isPrint, isUTF8, posixOffsets)
		err = errors.Join(err, f.Close())
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "strings: %s: %v\n", name, sysErr(err))
			exit = 1
		}
	}
	if err := w.Flush(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "strings: write error: %v\n", err)
		return 1
	}
	return exit
}

func getPrintableFunc(rc *toolcompat.RunContext) (printable func(rune) bool, utf8Mode bool, err error) {
	loc := locale.Resolve(rc.Env, locale.CType)
	locLower := strings.ToLower(loc)

	if locLower == "c" || locLower == "posix" || loc == "" {
		return func(r rune) bool {
			return r >= 32 && r <= 126
		}, false, nil
	}

	if strings.Contains(locLower, "utf-8") || strings.Contains(locLower, "utf8") {
		return func(r rune) bool {
			return unicode.IsPrint(r)
		}, true, nil
	}

	p, err := locale.Open(loc)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		err = errors.Join(err, p.Close())
	}()

	var isPrint [256]bool
	for i := 0; i < 256; i++ {
		ok, err := p.IsPrint(byte(i))
		if err != nil {
			return nil, false, err
		}
		isPrint[i] = ok
	}

	return func(r rune) bool {
		if r > 255 {
			return false
		}
		return isPrint[r]
	}, false, nil
}

func scan(r io.Reader, w *bufio.Writer, minLen int, radix string, isPrint func(rune) bool, isUTF8, posixOffsets bool) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var runBytes []byte
	var runCharLen int
	var offset, start int64
	flush := func() error {
		if runCharLen >= minLen {
			if posixOffsets {
				switch radix {
				case "o":
					if _, err := fmt.Fprintf(w, "%o ", start); err != nil {
						return err
					}
				case "d":
					if _, err := fmt.Fprintf(w, "%d ", start); err != nil {
						return err
					}
				case "x":
					if _, err := fmt.Fprintf(w, "%x ", start); err != nil {
						return err
					}
				}
			} else {
				switch radix {
				case "o":
					if _, err := fmt.Fprintf(w, "%7o ", start); err != nil {
						return err
					}
				case "d":
					if _, err := fmt.Fprintf(w, "%7d ", start); err != nil {
						return err
					}
				case "x":
					if _, err := fmt.Fprintf(w, "%7x ", start); err != nil {
						return err
					}
				}
			}
			if _, err := w.Write(runBytes); err != nil {
				return err
			}
			if err := w.WriteByte('\n'); err != nil {
				return err
			}
		}
		runBytes = runBytes[:0]
		runCharLen = 0
		return nil
	}
	for {
		if isUTF8 {
			rn, size, err := br.ReadRune()
			if errors.Is(err, io.EOF) {
				return flush()
			}
			if err != nil {
				return err
			}

			valid := rn != utf8.RuneError || size > 1
			if valid && isPrint(rn) {
				if runCharLen == 0 {
					start = offset
				}

				if err := br.UnreadRune(); err != nil {
					return err
				}
				raw, err := br.Peek(size)
				if err != nil {
					return err
				}
				runBytes = append(runBytes, raw...)
				if _, err := br.Discard(size); err != nil {
					return err
				}
				runCharLen++
			} else {
				if ferr := flush(); ferr != nil {
					return ferr
				}
			}
			offset += int64(size)
		} else {
			b, err := br.ReadByte()
			if err == io.EOF {
				return flush()
			}
			if err != nil {
				return err
			}
			if isPrint(rune(b)) {
				if runCharLen == 0 {
					start = offset
				}
				runBytes = append(runBytes, b)
				runCharLen++
			} else {
				if ferr := flush(); ferr != nil {
					return ferr
				}
			}
			offset++
		}
	}
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

func sysErr(err error) error {
	return toolcompat.SysErr(err)
}
