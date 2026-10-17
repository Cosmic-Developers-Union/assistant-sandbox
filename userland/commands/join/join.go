package joincmd

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
)

var cmd = &toolcompat.Tool{
	Name:     "join",
	Synopsis: "Join lines of two files on a common field.",
	Usage: "join [OPTION]... FILE1 FILE2\n\n" +
		"When FILE1 or FILE2 (not both) is -, read standard input.\n\n" +
		"  -1 FIELD     join on this FIELD of file 1\n" +
		"  -2 FIELD     join on this FIELD of file 2\n" +
		"  -j FIELD     equivalent to -1 FIELD -2 FIELD\n" +
		"  -a FILENUM   also print unpairable lines from file FILENUM,\n" +
		"               where FILENUM is 1 or 2\n" +
		"  -v FILENUM   like -a FILENUM, but suppress joined output lines\n" +
		"  -e EMPTY     replace missing input fields with EMPTY\n" +
		"  -o FORMAT    obey FORMAT while constructing output lines\n" +
		"  -t CHAR      use CHAR as input and output field separator\n" +
		"  -i           like --ignore-case",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

const (
	tabDefault   = -1
	tabWholeLine = -2
)

type options struct {
	field          [2]int
	tab            int
	printU         [2]bool
	suppressed     [2]bool
	ignoreCase     bool
	fold           *[256]byte
	collator       *collatorAdapter
	seenUnpairable bool
	checkOrder     bool
	nocheckOrder   bool
	header         bool
	zeroTerminated bool
	formats        []string
	empty          string
	orderError     bool
}

func run(rc *toolcompat.RunContext, args []string) int {
	return runWithProviders(rc, args, func(name string) (stringCollator, error) {
		return locale.OpenEnv(locale.StoreEnvAt(rc.Env, rc.Path), name)
	}, openCType)
}

func runWithCollator(rc *toolcompat.RunContext, args []string, open collatorOpener) int {
	return runWithProviders(rc, args, open, openCType)
}

func runWithProviders(rc *toolcompat.RunContext, args []string, open collatorOpener, openCtype ctypeOpener) int {
	opt := options{tab: tabDefault}
	collatorClosed := false
	defer func() {
		if opt.collator != nil && !collatorClosed {
			_ = opt.collator.Close()
		}
	}()
	contractErr := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(rc.Err, "join: %s\n", fmt.Sprintf(format, a...))
		_, _ = fmt.Fprintf(rc.Err, "join: not every GNU flag is implemented in pure-Go coreutils — see 'join --help' for the supported subset\n")
		return 2
	}

	rest := make([]string, 0, len(args))
	for idx := 0; idx < len(args); idx++ {
		a := args[idx]
		if a == "--" {
			rest = append(rest, args[idx:]...)
			break
		}

		if len(a) < 2 || a[0] != '-' {
			rest = append(rest, args[idx:]...)
			break
		}
		if strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		body := a[1:]
	cluster:
		for j := 0; j < len(body); j++ {
			c := body[j]
			switch c {
			case 'h', 'V':
				rest = append(rest, "-"+string(c))
			case 'i':
				opt.ignoreCase = true
			case 'z':
				opt.zeroTerminated = true
			case '1', '2', 'j', 'a', 'v', 't', 'o', 'e':
				var val string
				if j+1 < len(body) {
					val = body[j+1:]
				} else {
					idx++
					if idx >= len(args) {
						return toolcompat.UsageError(rc, cmd, "option requires an argument -- '%c'", c)
					}
					val = args[idx]
				}
				if code := opt.apply(rc, c, val); code >= 0 {
					return code
				}
				break cluster
			default:
				return contractErr("unknown shorthand flag: %q in -%s", string(c), body)
			}
		}
	}

	fs := toolcompat.NewFlags(cmd.Name)
	ignoreCaseLong := fs.Bool("ignore-case", false, "ignore differences in case when comparing fields")
	checkOrder := fs.Bool("check-order", false, "check that the input is correctly sorted")
	nocheckOrder := fs.Bool("nocheck-order", false, "do not check that the input is correctly sorted")
	header := fs.Bool("header", false, "treat the first line of each file as header lines")
	zeroTerminated := fs.BoolP("zero-terminated", "z", false, "line delimiter is NUL, not newline")

	operands, code := toolcompat.ParseRequireOrder(rc, cmd, fs, rest)
	if code >= 0 {
		return code
	}
	opt.ignoreCase = opt.ignoreCase || *ignoreCaseLong
	opt.checkOrder = *checkOrder
	opt.nocheckOrder = *nocheckOrder
	opt.header = *header
	opt.zeroTerminated = opt.zeroTerminated || *zeroTerminated

	switch {
	case len(operands) == 0:
		return toolcompat.UsageError(rc, cmd, "missing operand")
	case len(operands) == 1:
		return toolcompat.UsageError(rc, cmd, "missing operand after '%s'", operands[0])
	case len(operands) > 2:
		return toolcompat.UsageError(rc, cmd, "extra operand '%s'", operands[2])
	}
	if operands[0] == "-" && operands[1] == "-" {
		return toolcompat.UsageError(rc, cmd, "both files cannot be standard input")
	}

	var specs []spec
	if len(opt.formats) > 0 {
		var err error
		specs, err = parseFormats(opt.formats)
		if err != nil {
			return toolcompat.UsageError(rc, cmd, "%v", err)
		}
	}

	if !opt.ignoreCase {
		if name := locale.ResolveCarried(locale.StoreEnvAt(rc.Env, rc.Path), locale.Collate); name != "C" && name != locale.Default {
			provider, err := open(name)
			if err != nil {
				_, _ = fmt.Fprintf(rc.Err, "join: LC_COLLATE=%s: %v\n", name, err)
				return 2
			}
			opt.collator = newCollatorAdapter(provider)
		}
	} else {

		if name := locale.Resolve(rc.Env, locale.CType); name != "C" && name != locale.Default {
			provider, err := openCtype(name)
			if err != nil {
				_, _ = fmt.Fprintf(rc.Err, "join: LC_CTYPE=%s: %v\n", name, err)
				return 2
			}
			table, snapErr := snapshotFoldTable(provider)
			closeErr := provider.Close()
			if snapErr != nil {
				_, _ = fmt.Fprintf(rc.Err, "join: LC_CTYPE=%s: %v\n", name, snapErr)
				return 2
			}
			if closeErr != nil {
				_, _ = fmt.Fprintf(rc.Err, "join: LC_CTYPE=%s: %v\n", name, closeErr)
				return 2
			}
			opt.fold = table
		}
	}

	printJoined := !opt.suppressed[0] && !opt.suppressed[1]
	var files [2]*fileState
	for i, op := range operands {
		lines, err := readLines(rc, op, opt.zeroTerminated)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "join: %s: %v\n", op, pathErr(err))
			return 1
		}
		files[i] = &fileState{name: op, lines: lines, field: opt.field[i], idx: i, opt: &opt, rc: rc}
	}

	var staged bytes.Buffer
	bw := bufio.NewWriter(&staged)
	osep := " "
	if opt.tab >= 0 {
		osep = string([]byte{byte(opt.tab)})
	}

	lineTerm := "\n"
	if opt.zeroTerminated {
		lineTerm = "\x00"
	}
	emit := func(parts []string) {
		_, _ = bw.WriteString(strings.Join(parts, osep))
		_, _ = bw.WriteString(lineTerm)
	}
	emitUnpaired := func(f *fileState, from, to int) {
		opt.seenUnpairable = true
		if !opt.printU[f.idx] {
			return
		}
		for _, l := range f.lines[from:to] {
			flds := opt.splitFields(l)
			if f.idx == 0 {
				emit(buildOutput(flds, nil, f.field, 0, &opt, specs, false))
			} else {
				emit(buildOutput(nil, flds, 0, f.field, &opt, specs, false))
			}
		}
	}

	if opt.header {
		var h1, h2 []string
		if len(files[0].lines) > 0 {
			h1 = opt.splitFields(files[0].lines[0])
			files[0].lines = files[0].lines[1:]
		}
		if len(files[1].lines) > 0 {
			h2 = opt.splitFields(files[1].lines[0])
			files[1].lines = files[1].lines[1:]
		}
		if h1 != nil && h2 != nil {
			emit(buildOutput(h1, h2, opt.field[0], opt.field[1], &opt, specs, true))
		} else if h1 != nil {
			if opt.printU[0] {
				emit(buildOutput(h1, nil, opt.field[0], 0, &opt, specs, false))
			}
		} else if h2 != nil {
			if opt.printU[1] {
				emit(buildOutput(nil, h2, 0, opt.field[1], &opt, specs, false))
			}
		}
	}

	f1, f2 := files[0], files[1]
	g1, g2 := f1.nextGroup(), f2.nextGroup()
	if opt.orderError {
		return 1
	}
	for g1 != nil && g2 != nil {
		d := opt.compareKeys(g1.key, g2.key)
		switch {
		case d < 0:
			emitUnpaired(f1, g1.start, g1.end)
			g1 = f1.nextGroup()
		case d > 0:
			emitUnpaired(f2, g2.start, g2.end)
			g2 = f2.nextGroup()
		default:
			if printJoined {
				for _, l1 := range f1.lines[g1.start:g1.end] {
					flds1 := opt.splitFields(l1)
					for _, l2 := range f2.lines[g2.start:g2.end] {
						flds2 := opt.splitFields(l2)
						emit(buildOutput(flds1, flds2, f1.field, f2.field, &opt, specs, true))
					}
				}
			}
			g1, g2 = f1.nextGroup(), f2.nextGroup()
		}
		if opt.orderError {
			return 1
		}
	}
	for g1 != nil {
		emitUnpaired(f1, g1.start, g1.end)
		g1 = f1.nextGroup()
		if opt.orderError {
			return 1
		}
	}
	for g2 != nil {
		emitUnpaired(f2, g2.start, g2.end)
		g2 = f2.nextGroup()
		if opt.orderError {
			return 1
		}
	}

	_ = bw.Flush()
	if opt.collator != nil {
		if err := opt.collator.Err(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "join: LC_COLLATE comparison failed: %v\n", err)
			return 1
		}
		if err := opt.collator.Close(); err != nil {
			collatorClosed = true
			_, _ = fmt.Fprintf(rc.Err, "join: LC_COLLATE close failed: %v\n", err)
			return 1
		}
		collatorClosed = true
	}
	n, err := rc.Out.Write(staged.Bytes())
	if err == nil && n != staged.Len() {
		err = io.ErrShortWrite
	}
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "join: write failed: %v\n", err)
		return 1
	}
	if f1.warned || f2.warned {
		_, _ = fmt.Fprintln(rc.Err, "join: input is not in sorted order")
		return 1
	}
	return 0
}

func (o *options) apply(rc *toolcompat.RunContext, c byte, val string) int {
	switch c {
	case '1', '2':
		n, ok := parsePositive(val)
		if !ok {
			return toolcompat.UsageError(rc, cmd, "invalid field number: '%s'", val)
		}
		o.field[c-'1'] = n - 1
	case 'j':
		n, ok := parsePositive(val)
		if !ok {
			return toolcompat.UsageError(rc, cmd, "invalid field number: '%s'", val)
		}
		o.field[0], o.field[1] = n-1, n-1
	case 'a', 'v':
		if val != "1" && val != "2" {
			return toolcompat.UsageError(rc, cmd, "invalid file number: '%s'", val)
		}
		i := val[0] - '1'
		o.printU[i] = true
		if c == 'v' {
			o.suppressed[i] = true
		}
	case 't':
		switch {
		case val == "":
			o.tab = tabWholeLine
		case val == `\0`:
			o.tab = 0
		case len(val) == 1:
			o.tab = int(val[0])
		default:
			return toolcompat.UsageError(rc, cmd, "multi-character tab '%s'", val)
		}
	case 'o':
		o.formats = append(o.formats, val)
	case 'e':
		o.empty = val
	}
	return -1
}

type spec struct {
	file  int
	field int
}

func parseFormats(formats []string) ([]spec, error) {
	var specs []spec
	for _, s := range formats {
		s = strings.ReplaceAll(s, " ", ",")
		s = strings.ReplaceAll(s, "\t", ",")
		parts := strings.Split(s, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if p == "0" {
				specs = append(specs, spec{file: 0})
				continue
			}
			fileText, fieldText, ok := strings.Cut(p, ".")
			field, fieldOK := parsePositive(fieldText)
			if !ok || fieldText == "" || !fieldOK {
				return nil, fmt.Errorf("invalid field specification: %q", p)
			}
			var file int
			switch fileText {
			case "1":
				file = 1
			case "2":
				file = 2
			default:
				return nil, fmt.Errorf("invalid field specification: %q", p)
			}
			specs = append(specs, spec{file: file, field: field - 1})
		}
	}
	return specs, nil
}

func selectedField(fields []string, idx int, opt *options) string {
	if idx >= 0 && idx < len(fields) && fields[idx] != "" {
		return fields[idx]
	}
	return opt.empty
}

func buildOutput(flds1, flds2 []string, field1, field2 int, opt *options, specs []spec, isPaired bool) []string {
	if len(specs) == 0 {
		if isPaired {
			res := []string{fieldAt(flds1, field1)}
			res = append(res, otherFields(flds1, field1)...)
			res = append(res, otherFields(flds2, field2)...)
			return res
		}
		if flds1 != nil {
			res := []string{fieldAt(flds1, field1)}
			res = append(res, otherFields(flds1, field1)...)
			return res
		}
		if flds2 != nil {
			res := []string{fieldAt(flds2, field2)}
			res = append(res, otherFields(flds2, field2)...)
			return res
		}
		return nil
	}

	res := make([]string, len(specs))
	joinVal := ""
	if flds1 != nil {
		joinVal = fieldAt(flds1, field1)
	} else if flds2 != nil {
		joinVal = fieldAt(flds2, field2)
	}

	for i, sp := range specs {
		switch sp.file {
		case 0:
			if joinVal == "" {
				res[i] = opt.empty
			} else {
				res[i] = joinVal
			}
		case 1:
			if flds1 != nil {
				res[i] = selectedField(flds1, sp.field, opt)
			} else {
				res[i] = opt.empty
			}
		case 2:
			if flds2 != nil {
				res[i] = selectedField(flds2, sp.field, opt)
			} else {
				res[i] = opt.empty
			}
		}
	}
	return res
}

func parsePositive(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	overflowed := false
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		if overflowed {
			continue
		}
		d := int(s[i] - '0')
		if n > (math.MaxInt-d)/10 {
			overflowed = true
			continue
		}
		n = n*10 + d
	}
	if overflowed {
		return math.MaxInt, true
	}
	return n, n > 0
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

func (o *options) splitFields(line string) []string {
	switch {
	case o.tab == tabWholeLine:
		return []string{line}
	case o.tab >= 0:
		return strings.Split(line, string([]byte{byte(o.tab)}))
	default:
		var out []string
		i := 0
		for i < len(line) {
			for i < len(line) && isBlank(line[i]) {
				i++
			}
			start := i
			for i < len(line) && !isBlank(line[i]) {
				i++
			}
			if i > start {
				out = append(out, line[start:i])
			}
		}
		return out
	}
}

func fieldAt(fields []string, idx int) string {
	if idx < len(fields) {
		return fields[idx]
	}
	return ""
}

func otherFields(fields []string, idx int) []string {
	out := make([]string, 0, len(fields))
	for i, f := range fields {
		if i != idx {
			out = append(out, f)
		}
	}
	return out
}

func upperByte(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}

func (o *options) foldByte(c byte) byte {
	if o.fold != nil {
		return o.fold[c]
	}
	return upperByte(c)
}

func (o *options) compareKeys(a, b string) int {
	if !o.ignoreCase {
		if o.collator != nil {
			return o.collator.Compare(a, b)
		}
		return strings.Compare(a, b)
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	fold := o.foldByte
	for i := 0; i < n; i++ {
		ca, cb := fold(a[i]), fold(b[i])
		switch {
		case ca < cb:
			return -1
		case ca > cb:
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

type fileState struct {
	name    string
	lines   []string
	field   int
	idx     int
	opt     *options
	rc      *toolcompat.RunContext
	pos     int
	checked int
	warned  bool
}

type group struct {
	key        string
	start, end int
}

func (f *fileState) keyOf(i int) string {
	return fieldAt(f.opt.splitFields(f.lines[i]), f.field)
}

func (f *fileState) touch(i int) {
	for f.checked < i {
		f.checked++
		if !f.opt.nocheckOrder {
			isDisordered := f.opt.compareKeys(f.keyOf(f.checked-1), f.keyOf(f.checked)) > 0
			if isDisordered {
				if f.opt.checkOrder {
					_, _ = fmt.Fprintf(f.rc.Err, "join: %s:%d: is not sorted: %s\n", f.name, f.checked+1, f.lines[f.checked])
					f.opt.orderError = true
					return
				}
				if f.opt.seenUnpairable && !f.warned {
					_, _ = fmt.Fprintf(f.rc.Err, "join: %s:%d: is not sorted: %s\n", f.name, f.checked+1, f.lines[f.checked])
					f.warned = true
				}
			}
		}
	}
}

func (f *fileState) nextGroup() *group {
	if f.pos >= len(f.lines) {
		return nil
	}
	g := &group{start: f.pos, key: f.keyOf(f.pos)}
	f.touch(f.pos)
	if f.opt.orderError {
		return nil
	}
	i := f.pos + 1
	for i < len(f.lines) {
		f.touch(i)
		if f.opt.orderError {
			return nil
		}
		if f.opt.compareKeys(f.keyOf(i), g.key) != 0 {
			break
		}
		i++
	}
	g.end = i
	f.pos = i
	return g
}

func readLines(rc *toolcompat.RunContext, operand string, zeroTerminated bool) ([]string, error) {
	var data []byte
	var err error
	if operand == "-" {
		data, err = io.ReadAll(rc.In)
	} else {
		data, err = vfs.ReadFile(rc.Path(operand))
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	sep := "\n"
	if zeroTerminated {
		sep = "\x00"
	}
	return strings.Split(strings.TrimSuffix(string(data), sep), sep), nil
}

func pathErr(err error) error {
	return toolcompat.SysErr(err)
}
