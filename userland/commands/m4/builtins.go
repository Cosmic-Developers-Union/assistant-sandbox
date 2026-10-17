package m4

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type builtinFunc func(p *processor, name string, args []argument) (text string, fn *macro)

var builtins = map[string]*macro{}

func builtin(name string, blind bool, fn builtinFunc) {
	builtins[name] = &macro{fn: fn, name: name, blind: blind}
}

func init() {
	builtin("define", true, func(p *processor, _ string, args []argument) (string, *macro) {
		if len(args) == 0 {
			return "", nil
		}
		p.defineFromArgs(args, false)
		return "", nil
	})
	builtin("pushdef", true, func(p *processor, _ string, args []argument) (string, *macro) {
		if len(args) == 0 {
			return "", nil
		}
		p.defineFromArgs(args, true)
		return "", nil
	})
	builtin("undefine", true, func(p *processor, _ string, args []argument) (string, *macro) {
		for _, a := range args {
			delete(p.macros, a.text)
		}
		return "", nil
	})
	builtin("popdef", true, func(p *processor, _ string, args []argument) (string, *macro) {
		for _, a := range args {
			if st := p.macros[a.text]; len(st) > 1 {
				p.macros[a.text] = st[:len(st)-1]
			} else {
				delete(p.macros, a.text)
			}
		}
		return "", nil
	})
	builtin("defn", true, biDefn)
	builtin("ifdef", true, func(p *processor, name string, args []argument) (string, *macro) {
		switch {
		case !p.atLeast(name, args, 2):
		case p.lookup(args[0].text) != nil:
			return args[1].text, nil
		case len(args) > 2:
			return args[2].text, nil
		}
		return "", nil
	})
	builtin("ifelse", true, biIfelse)
	builtin("shift", true, func(p *processor, _ string, args []argument) (string, *macro) {
		if len(args) < 2 {
			return "", nil
		}
		return joinArgs(args[1:], p.lquote, p.rquote), nil
	})
	builtin("dnl", false, func(p *processor, _ string, _ []argument) (string, *macro) {

		for {
			c, ok := p.peekAt(0)
			if !ok {
				break
			}
			p.next()
			if c == '\n' {
				break
			}
		}
		return "", nil
	})
	builtin("changequote", false, func(p *processor, _ string, args []argument) (string, *macro) {

		p.lquote, p.rquote = "`", "'"
		if len(args) > 0 && (len(args) > 1 || args[0].present) {
			p.lquote = args[0].text
			if len(args) > 1 && args[1].text != "" {
				p.rquote = args[1].text
			}
		}
		return "", nil
	})
	builtin("changecom", false, func(p *processor, _ string, args []argument) (string, *macro) {

		p.bcomm, p.ecomm = "", "\n"
		if len(args) > 0 {
			p.bcomm = args[0].text
			if len(args) > 1 && args[1].text != "" {
				p.ecomm = args[1].text
			}
		}
		return "", nil
	})
	builtin("len", true, func(p *processor, _ string, args []argument) (string, *macro) {
		return strconv.Itoa(len(args[0].text)), nil
	})
	builtin("index", true, func(p *processor, name string, args []argument) (string, *macro) {
		if !p.atLeast(name, args, 2) {
			return "", nil
		}
		return strconv.Itoa(strings.Index(args[0].text, args[1].text)), nil
	})
	builtin("substr", true, biSubstr)
	builtin("translit", true, biTranslit)
	builtin("incr", true, func(p *processor, name string, args []argument) (string, *macro) {
		return p.addTo(name, args, 1), nil
	})
	builtin("decr", true, func(p *processor, name string, args []argument) (string, *macro) {
		return p.addTo(name, args, -1), nil
	})
	builtin("eval", true, biEval)
	builtin("divert", false, func(p *processor, name string, args []argument) (string, *macro) {
		n := 0
		ok := true
		if len(args) > 0 && args[0].text != "" {
			n, ok = p.number(name, args[0].text)
		}
		if !ok {
			return "", nil
		}
		p.divnum = n
		return "", nil
	})
	builtin("divnum", false, func(p *processor, _ string, _ []argument) (string, *macro) {
		return strconv.Itoa(p.divnum), nil
	})
	builtin("undivert", false, func(p *processor, name string, args []argument) (string, *macro) {
		undiv := func(n int) {
			if n > 0 && n < len(p.diversions) {
				s := p.diversions[n].String()
				p.diversions[n].Reset()
				p.writeOutput(s)
			}
		}
		if len(args) == 0 {

			for i := 1; i < len(p.diversions); i++ {
				undiv(i)
			}
			return "", nil
		}
		for _, a := range args {
			n, ok := p.number(name, a.text)
			if ok {
				undiv(n)
			}
		}
		return "", nil
	})
	builtin("include", true, func(p *processor, name string, args []argument) (string, *macro) {
		if !p.atLeast(name, args, 1) {
			return "", nil
		}
		data, err := readGuestFile(p.ctx, p.dir, args[0].text)
		if err != nil {

			p.fatalf("%s: %v", args[0].text, err)
		}
		p.pushIncluded(args[0].text, data)
		return "", nil
	})
	builtin("sinclude", true, func(p *processor, _ string, args []argument) (string, *macro) {
		if len(args) == 0 {
			return "", nil
		}
		data, err := readGuestFile(p.ctx, p.dir, args[0].text)
		if err != nil {

			return "", nil
		}
		p.pushIncluded(args[0].text, data)
		return "", nil
	})
	builtin("syscmd", true, func(p *processor, name string, args []argument) (string, *macro) {
		if !p.atLeast(name, args, 1) {
			return "", nil
		}
		_, code := p.runSystem(args[0].text, false)
		p.sysval = code
		return "", nil
	})
	builtin("sysval", false, func(p *processor, _ string, _ []argument) (string, *macro) {
		return strconv.Itoa(p.sysval), nil
	})
	builtin("esyscmd", true, func(p *processor, name string, args []argument) (string, *macro) {
		if !p.atLeast(name, args, 1) {
			return "", nil
		}
		output, code := p.runSystem(args[0].text, true)
		p.sysval = code
		return output, nil
	})
	builtin("maketemp", true, biMaketemp)
	builtin("m4exit", false, func(p *processor, name string, args []argument) (string, *macro) {
		code := 0
		ok := true
		if len(args) > 0 {
			code, ok = p.number(name, args[0].text)
		}
		if ok {
			p.exitSet = true
			p.exitCode = code
			if code != 0 {
				p.failed = true
			}
		}
		panic(abort{})
	})
	builtin("m4wrap", true, func(p *processor, _ string, args []argument) (string, *macro) {
		if len(args) > 0 {
			p.wraps = append(p.wraps, args[0].text)
		}
		return "", nil
	})
	builtin("errprint", true, func(p *processor, _ string, args []argument) (string, *macro) {
		for _, a := range args {
			_, _ = fmt.Fprint(p.stderr, a.text)
		}
		return "", nil
	})
	builtin("dumpdef", false, biDumpdef)

	builtin("__file__", false, func(p *processor, _ string, _ []argument) (string, *macro) {
		return p.file, nil
	})
	builtin("__line__", false, func(p *processor, _ string, _ []argument) (string, *macro) {
		return strconv.Itoa(p.line), nil
	})
}

func (p *processor) atLeast(name string, args []argument, n int) bool {
	if len(args) >= n {
		return true
	}
	p.warnf("内建宏 %s 的参数太少", name)
	return false
}

func (p *processor) number(name, s string) (int, bool) {
	if s == "" {
		p.warnf("内建宏 %s 的空串参数按 0 处理", name)
		return 0, true
	}
	v, err := strconv.ParseInt(strings.TrimLeft(s, " \t\n"), 10, 64)
	if err != nil {
		p.warnf("内建宏 %s 的参数不是数字", name)
		return 0, false
	}
	return int(v), true
}

func (p *processor) defineFromArgs(args []argument, push bool) {
	name := args[0].text
	if name == "" {
		return
	}
	m := &macro{}
	if len(args) > 1 {
		if args[1].fn != nil {

			m = args[1].fn
		} else {
			m.text = args[1].text
		}
	}
	p.define(name, m, push)
}

func biDefn(p *processor, _ string, args []argument) (string, *macro) {
	var b strings.Builder
	for _, a := range args {
		m := p.lookup(a.text)
		switch {
		case m == nil:
		case m.fn != nil:
			if len(args) == 1 {
				return "", m
			}
		default:
			b.WriteString(p.lquote + m.text + p.rquote)
		}
	}
	return b.String(), nil
}

func biIfelse(p *processor, name string, args []argument) (string, *macro) {
	if len(args) == 1 {
		return "", nil
	}
	if !p.atLeast(name, args, 3) {
		return "", nil
	}
	for {
		if args[0].text == args[1].text {
			return args[2].text, nil
		}
		switch len(args) {
		case 3:
			return "", nil
		case 4, 5:
			return args[3].text, nil
		}
		args = args[3:]
	}
}

func biSubstr(p *processor, name string, args []argument) (string, *macro) {
	if !p.atLeast(name, args, 2) {
		return "", nil
	}
	s := args[0].text
	start, ok := p.number(name, args[1].text)
	if !ok {
		return "", nil
	}
	length := len(s)
	if len(args) > 2 {
		if length, ok = p.number(name, args[2].text); !ok {
			return "", nil
		}
	}
	if start < 0 || start >= len(s) || length <= 0 {
		return "", nil
	}
	end := start + length
	if end > len(s) {
		end = len(s)
	}
	return s[start:end], nil
}

func biTranslit(p *processor, name string, args []argument) (string, *macro) {
	if !p.atLeast(name, args, 2) {
		return "", nil
	}
	from := expandRanges(args[1].text)
	var to string
	if len(args) > 2 {
		to = expandRanges(args[2].text)
	}
	var table [256]byte
	var mapped [256]bool
	var deleted [256]bool
	for i := 0; i < len(from); i++ {
		if mapped[from[i]] {
			continue
		}
		mapped[from[i]] = true
		if i < len(to) {
			table[from[i]] = to[i]
		} else {
			deleted[from[i]] = true
		}
	}
	var b strings.Builder
	for i := 0; i < len(args[0].text); i++ {
		c := args[0].text[i]
		switch {
		case !mapped[c]:
			b.WriteByte(c)
		case deleted[c]:
		default:
			b.WriteByte(table[c])
		}
	}
	return b.String(), nil
}

func expandRanges(s string) string {
	if !strings.Contains(s, "-") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '-' || i == 0 || i+1 == len(s) {
			b = append(b, s[i])
			continue
		}
		lo, hi := s[i-1], s[i+1]
		i++
		for c := lo; c != hi; {
			if lo < hi {
				c++
			} else {
				c--
			}
			b = append(b, c)
		}
	}
	return string(b)
}

func (p *processor) addTo(name string, args []argument, delta int) string {
	if len(args) == 0 {
		p.atLeast(name, args, 1)
		return ""
	}
	v, ok := p.number(name, args[0].text)
	if !ok {
		return ""
	}
	return strconv.Itoa(v + delta)
}

func biEval(p *processor, name string, args []argument) (string, *macro) {
	radix, width := 10, 1
	var ok bool
	if len(args) > 1 && args[1].text != "" {
		if radix, ok = p.number(name, args[1].text); !ok {
			return "", nil
		}
		if radix < 1 || radix > 36 {
			p.warnf("内建宏 %s 的进制超出范围 (radix = %d)", name, radix)
			return "", nil
		}
	}
	if len(args) > 2 && args[2].text != "" {
		if width, ok = p.number(name, args[2].text); !ok {
			return "", nil
		}
		if width < 0 {
			p.warnf("内建宏 %s 的补零宽度为负", name)
			return "", nil
		}
		if width > maxEvalWidth {
			p.warnf("内建宏 %s 的补零宽度 %d 超出客体预算", name, width)
			p.failed = true
			return "", nil
		}
	}
	if args[0].text == "" {
		p.warnf("内建宏 %s 的空串参数按 0 处理", name)
	}
	v, evalErr := evalExpr(args[0].text)
	if evalErr != "" {
		p.warnf("eval 求值失败（%s）: %s", evalErr, args[0].text)
		p.failed = true
		return "", nil
	}
	sign := ""
	var mag uint64
	if v < 0 {
		mag, sign = uint64(-int64(v)), "-"
	} else {
		mag = uint64(v)
	}
	var digits string
	if radix == 1 {

		if mag > maxEvalWidth {
			p.warnf("内建宏 %s 进制 1 的输出超出客体预算", name)
			p.failed = true
			return "", nil
		}
		digits = strings.Repeat("1", int(mag))
	} else {
		digits = strings.ToUpper(strconv.FormatUint(mag, radix))
	}
	if pad := width - len(digits); pad > 0 {
		digits = strings.Repeat("0", pad) + digits
	}
	return sign + digits, nil
}

func biMaketemp(p *processor, name string, args []argument) (string, *macro) {
	if !p.atLeast(name, args, 1) {
		return "", nil
	}
	tmpl := args[0].text
	width := len(tmpl) - len(strings.TrimRight(tmpl, "X"))
	if width < 6 {
		p.warnf("内建宏 %s 的模板必须以 XXXXXX 结尾", name)
		p.failed = true
		return "", nil
	}
	random, err := randomOf(p)
	if err != nil {
		p.warnf("内建宏 %s: %v", name, err)
		p.failed = true
		return "", nil
	}
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	prefix := tmpl[:len(tmpl)-width]
	for range 64 {
		raw := make([]byte, width)
		if _, err := random.Read(p.ctx, raw); err != nil {
			p.warnf("内建宏 %s: %v", name, err)
			p.failed = true
			return "", nil
		}
		suffix := make([]byte, width)
		for i, b := range raw {

			suffix[i] = letters[int(b)%len(letters)]
		}

		candidate := prefix + string(suffix)
		file, err := commandio.Open(p.ctx, commandio.Path(p.dir, candidate), vmfs.Create|vmfs.Exclusive|vmfs.WriteOnly, 0o600)
		if err == nil {
			if err := file.Close(); err != nil {
				p.warnf("内建宏 %s: %v", name, err)
				p.failed = true
				return "", nil
			}
			return candidate, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			p.warnf("内建宏 %s: %v", name, err)
			p.failed = true
			return "", nil
		}
	}
	p.warnf("内建宏 %s: 无法创建唯一的临时文件", name)
	p.failed = true
	return "", nil
}

func randomOf(p *processor) (entropy.Source, error) {
	proc0, err := proc.FromContext(p.ctx)
	if err != nil {
		return nil, err
	}
	return proc0.Resources.Random()
}

func biDumpdef(p *processor, _ string, args []argument) (string, *macro) {
	names := make([]string, 0, len(args))
	for _, a := range args {
		names = append(names, a.text)
	}
	if len(names) == 0 {
		names = make([]string, 0, len(p.macros))
		for n := range p.macros {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, n := range names {
		if m := p.lookup(n); m != nil {
			if m.fn != nil {
				_, _ = fmt.Fprintf(p.stderr, "%s:\t<%s>\n", n, m.name)
			} else {
				_, _ = fmt.Fprintf(p.stderr, "%s:\t%s\n", n, m.text)
			}
		}
	}
	return "", nil
}
