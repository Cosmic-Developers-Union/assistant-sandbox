package iconv

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"mvdan.cc/sh/v3/interp"
)

type codeset int

const (
	codesetUTF8 codeset = iota
	codesetASCII
	codesetLatin1
)

var supportedNames = []string{"8859-1", "ANSI_X3.4-1968", "ASCII", "ISO-8859-1", "US-ASCII", "UTF-8", "latin1"}

func init() { sort.Strings(supportedNames) }

func lookup(name string) (codeset, bool) {
	switch normalize(name) {
	case "UTF8":
		return codesetUTF8, true
	case "ASCII", "USASCII", "ANSIX341968":
		return codesetASCII, true
	case "ISO88591", "88591", "LATIN1":
		return codesetLatin1, true
	}
	return codesetUTF8, false
}

func normalize(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if r == '-' || r == '_' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "iconv")
	from := flags.StringP("from-code", "f", "", "源代码集")
	to := flags.StringP("to-code", "t", "", "目标代码集")
	drop := flags.BoolP("discard", "c", false, "静默丢弃无法转换的序列")
	list := flags.BoolP("list", "l", false, "列出支持的代码集名字")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if *list {

		for _, name := range supportedNames {
			if _, err := fmt.Fprintln(h.Stdout, name); err != nil {
				return err
			}
		}
		return nil
	}
	if *from == "" || *to == "" {
		_, _ = fmt.Fprintln(h.Stderr, "用法: iconv -f 源代码集 -t 目标代码集 [-c] [文件...] | iconv -l")
		return interp.ExitStatus(2)
	}
	source, ok := lookup(*from)
	if !ok {
		_, _ = fmt.Fprintf(h.Stderr, "iconv: %s: 不支持\n", *from)
		return interp.ExitStatus(1)
	}
	target, ok := lookup(*to)
	if !ok {
		_, _ = fmt.Fprintf(h.Stderr, "iconv: %s: 不支持\n", *to)
		return interp.ExitStatus(1)
	}
	return commandio.Inputs(ctx, flags.Args(), func(name, input string) error {
		output, bad := convert(source, target, input, *drop)
		if bad >= 0 {
			_, _ = fmt.Fprintf(h.Stderr, "iconv: 位置 %d: 无法转换\n", bad)
			return interp.ExitStatus(1)
		}
		_, err := io.WriteString(h.Stdout, output)
		return err
	})
}

func convert(source, target codeset, input string, drop bool) (string, int) {
	if source == target {

		return input, -1
	}
	var b strings.Builder
	if target == codesetUTF8 {

		for i := 0; i < len(input); i++ {
			if c := input[i]; c < 0x80 {
				b.WriteByte(c)
			} else {
				b.WriteRune(rune(c))
			}
		}
		return b.String(), -1
	}

	limit := rune(0x7F)
	if target == codesetLatin1 {
		limit = 0xFF
	}
	for i := 0; i < len(input); {
		r, size := utf8.DecodeRuneInString(input[i:])
		if r == utf8.RuneError && size == 1 {

			if !drop {
				return "", i
			}
			i++
			continue
		}
		if r > limit {
			if !drop {
				return "", i
			}
			i += size
			continue
		}
		b.WriteByte(byte(r))
		i += size
	}
	return b.String(), -1
}
