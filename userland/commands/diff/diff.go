package diffcmd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

var cmd = &toolcompat.Tool{
	Name:     "diff",
	Synopsis: "Compare FILES line by line.",
	Usage: "diff [OPTION]... FILES\n" +
		"FILES are 'FILE1 FILE2' or 'DIR1 DIR2' or 'DIR FILE' or 'FILE DIR'.\n" +
		"'-' as a FILE means standard input.\n" +
		"Edit scripts: -e, --ed; -f, --forward-ed\n" +
		"Unified format: -u, -U NUM, --unified[=NUM]\n" +
		"                output NUM (default 3) lines of unified context\n" +
		"Context format: -c, -C NUM, --context[=NUM]\n" +
		"                output NUM (default 3) lines of copied context",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

type options struct {
	unified           bool
	contextStyle      bool
	edStyle           bool
	forwardEdStyle    bool
	context           int
	recursive         bool
	brief             bool
	newFile           bool
	ignoreCase        bool
	ignoreAllSpace    bool
	ignoreSpaceChange bool

	optTokens []string
}

func run(rc *toolcompat.RunContext, args []string) int {
	rest, opts, perr := prescan(args)
	if perr != "" {
		return toolcompat.UsageError(rc, cmd, "%s", perr)
	}
	styles := 0
	for _, enabled := range []bool{opts.unified, opts.contextStyle, opts.edStyle, opts.forwardEdStyle} {
		if enabled {
			styles++
		}
	}
	if styles > 1 {
		return toolcompat.UsageError(rc, cmd, "conflicting output style options")
	}
	flags := toolcompat.NewFlags(cmd.Name)
	recursive := flags.BoolP("recursive", "r", false, "recursively compare any subdirectories found")
	brief := flags.BoolP("brief", "q", false, "report only when files differ")
	newFile := flags.BoolP("new-file", "N", false, "treat absent files as empty")
	ignoreCase := flags.BoolP("ignore-case", "i", false, "ignore case differences in file contents")
	ignoreSpaceChange := flags.BoolP("ignore-space-change", "b", false, "ignore changes in the amount of white space")
	ignoreAllSpace := flags.BoolP("ignore-all-space", "w", false, "ignore all white space")
	operands, code := toolcompat.ParseRequireOrder(rc, cmd, flags, rest)
	if code >= 0 {
		return code
	}
	opts.recursive = *recursive
	opts.brief = *brief
	if opts.brief && styles > 0 {
		return toolcompat.UsageError(rc, cmd, "conflicting output style options")
	}
	opts.newFile = *newFile
	opts.ignoreCase = *ignoreCase
	opts.ignoreSpaceChange = *ignoreSpaceChange
	opts.ignoreAllSpace = *ignoreAllSpace

	switch len(operands) {
	case 0:
		return toolcompat.UsageError(rc, cmd, "missing operand after 'diff'")
	case 1:
		return toolcompat.UsageError(rc, cmd, "missing operand after '%s'", operands[0])
	case 2:
	default:
		return toolcompat.UsageError(rc, cmd, "extra operand '%s'", operands[2])
	}

	bw := bufio.NewWriter(rc.Out)
	brc := &toolcompat.RunContext{Ctx: rc.Ctx, Dir: rc.Dir, Env: rc.Env,
		Stdio: toolcompat.Stdio{In: rc.In, Out: bw, Err: rc.Err}}
	status := dispatch(brc, &opts, operands[0], operands[1])
	if err := bw.Flush(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: write error: %v\n", err)
		if status < 2 {
			status = 2
		}
	}
	return status
}

func prescan(args []string) ([]string, options, string) {
	opts := options{context: 3}
	var rest []string
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if tok == "-" || !strings.HasPrefix(tok, "-") {

			rest = append(rest, args[i:]...)
			break
		}
		if strings.HasPrefix(tok, "--") {
			if tok == "--ed" {
				opts.edStyle = true
				opts.optTokens = append(opts.optTokens, tok)
				continue
			}
			if tok == "--forward-ed" {
				opts.forwardEdStyle = true
				opts.optTokens = append(opts.optTokens, tok)
				continue
			}
			if tok == "--unified" {
				opts.unified = true
				opts.optTokens = append(opts.optTokens, tok)
				continue
			}
			if v, ok := strings.CutPrefix(tok, "--unified="); ok {
				n, perr := parseContext(v)
				if perr != "" {
					return nil, opts, perr
				}
				opts.unified, opts.context = true, n
				opts.optTokens = append(opts.optTokens, tok)
				continue
			}
			if tok == "--context" {
				opts.contextStyle = true
				opts.optTokens = append(opts.optTokens, tok)
				continue
			}
			if v, ok := strings.CutPrefix(tok, "--context="); ok {
				n, perr := parseContext(v)
				if perr != "" {
					return nil, opts, perr
				}
				opts.contextStyle, opts.context = true, n
				opts.optTokens = append(opts.optTokens, tok)
				continue
			}
			rest = append(rest, tok)
			opts.optTokens = append(opts.optTokens, tok)
			continue
		}

		body := tok[1:]
		var keep []byte
		took := false
	scan:
		for j := 0; j < len(body); j++ {
			switch body[j] {
			case 'e':
				opts.edStyle = true
			case 'f':
				opts.forwardEdStyle = true
			case 'u':
				opts.unified = true
			case 'c':
				opts.contextStyle = true
			case 'U', 'C':
				if body[j] == 'U' {
					opts.unified = true
				} else {
					opts.contextStyle = true
				}
				var v string
				switch {
				case j+1 < len(body):
					v = body[j+1:]
				case i+1 < len(args):
					v = args[i+1]
					took = true
				default:
					return nil, opts, fmt.Sprintf("option requires an argument -- '%c'", body[j])
				}
				n, perr := parseContext(v)
				if perr != "" {
					return nil, opts, perr
				}
				opts.context = n
				break scan
			default:
				keep = append(keep, body[j])
			}
		}
		if len(keep) > 0 {
			rest = append(rest, "-"+string(keep))
		}
		opts.optTokens = append(opts.optTokens, tok)
		if took {
			opts.optTokens = append(opts.optTokens, args[i+1])
			i++
		}
	}
	return rest, opts, ""
}

func parseContext(v string) (int, string) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Sprintf("invalid context length '%s'", v)
	}
	return n, ""
}

func dispatch(rc *toolcompat.RunContext, opts *options, a, b string) int {
	stat := func(name string) (isDir, missing bool, err error) {
		if name == "-" {
			return false, false, nil
		}
		fi, err := vfs.Stat(name)
		if err != nil {
			if opts.newFile && errors.Is(err, fs.ErrNotExist) {
				return false, true, nil
			}
			return false, false, err
		}
		return fi.IsDir(), false, nil
	}
	bad := false
	aIsDir, aMissing, ea := stat(a)
	if ea != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", a, errText(ea))
		bad = true
	}
	bIsDir, bMissing, eb := stat(b)
	if eb != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", b, errText(eb))
		bad = true
	}
	if bad {
		return 2
	}
	if aMissing && bMissing {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: No such file or directory\n", a)
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: No such file or directory\n", b)
		return 2
	}
	switch {
	case aIsDir && bIsDir:
		return compareDirs(rc, opts, a, b)
	case aIsDir:
		if bMissing {
			_, _ = fmt.Fprintf(rc.Err, "diff: %s: No such file or directory\n", b)
			return 2
		}
		a = joinDisplay(a, path.Base(b))
	case bIsDir:
		if aMissing {
			_, _ = fmt.Fprintf(rc.Err, "diff: %s: No such file or directory\n", a)
			return 2
		}
		b = joinDisplay(b, path.Base(a))
	}
	return compareFiles(rc, opts, a, b, false)
}

func compareDirs(rc *toolcompat.RunContext, opts *options, da, db string) int {
	la, ea := readDirNames(opts, da)
	lb, eb := readDirNames(opts, db)
	if ea != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", da, errText(ea))
	}
	if eb != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", db, errText(eb))
	}
	if ea != nil || eb != nil {
		return 2
	}
	status := 0
	for _, name := range sortedUnion(la, lb) {
		var st int
		switch {
		case la[name] && lb[name]:
			st = comparePair(rc, opts, joinDisplay(da, name), joinDisplay(db, name))
		case la[name]:
			st = onlyIn(rc, opts, da, name, joinDisplay(da, name), joinDisplay(db, name), true)
		default:
			st = onlyIn(rc, opts, db, name, joinDisplay(db, name), joinDisplay(da, name), false)
		}
		if st > status {
			status = st
		}
	}
	return status
}

func readDirNames(opts *options, dir string) (map[string]bool, error) {
	ents, err := vfs.ReadDir(dir)
	if err != nil {
		if opts.newFile && errors.Is(err, fs.ErrNotExist) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	m := make(map[string]bool, len(ents))
	for _, e := range ents {
		m[e.Name()] = true
	}
	return m, nil
}

func sortedUnion(a, b map[string]bool) []string {
	names := make([]string, 0, len(a)+len(b))
	for n := range a {
		names = append(names, n)
	}
	for n := range b {
		if !a[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func comparePair(rc *toolcompat.RunContext, opts *options, pa, pb string) int {
	fa, err := vfs.Stat(pa)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", pa, errText(err))
		return 2
	}
	fb, err := vfs.Stat(pb)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", pb, errText(err))
		return 2
	}
	switch {
	case fa.IsDir() && fb.IsDir():
		if opts.recursive {
			return compareDirs(rc, opts, pa, pb)
		}
		_, _ = fmt.Fprintf(rc.Out, "Common subdirectories: %s and %s\n", pa, pb)
		return 0
	case fa.IsDir() != fb.IsDir():
		_, _ = fmt.Fprintf(rc.Out, "File %s is %s while file %s is %s\n",
			pa, kindWord(fa), pb, kindWord(fb))
		return 1
	case !fa.Mode().IsRegular() || !fb.Mode().IsRegular():

		_, _ = fmt.Fprintf(rc.Out, "File %s is %s while file %s is %s\n",
			pa, kindWord(fa), pb, kindWord(fb))
		return 1
	default:
		return compareFiles(rc, opts, pa, pb, !opts.brief)
	}
}

func onlyIn(rc *toolcompat.RunContext, opts *options, dir, name, present, absent string, presentIsOld bool) int {
	if !opts.newFile {
		_, _ = fmt.Fprintf(rc.Out, "Only in %s: %s\n", dir, name)
		return 1
	}
	fi, err := vfs.Stat(present)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", present, errText(err))
		return 2
	}
	if fi.IsDir() {
		if opts.recursive {
			if presentIsOld {
				return compareDirs(rc, opts, present, absent)
			}
			return compareDirs(rc, opts, absent, present)
		}
		_, _ = fmt.Fprintf(rc.Out, "Only in %s: %s\n", dir, name)
		return 1
	}
	if presentIsOld {
		return compareFiles(rc, opts, present, absent, !opts.brief)
	}
	return compareFiles(rc, opts, absent, present, !opts.brief)
}

func kindWord(fi fs.FileInfo) string {
	mode := fi.Mode()
	switch {
	case mode.IsDir():
		return "a directory"
	case mode.IsRegular():
		return "a regular file"
	case mode&fs.ModeNamedPipe != 0:
		return "a named pipe"
	case mode&fs.ModeSocket != 0:
		return "a socket"
	case mode&fs.ModeDevice != 0 && mode&fs.ModeCharDevice != 0:
		return "a character special file"
	case mode&fs.ModeDevice != 0:
		return "a block special file"
	default:
		return "a special file"
	}
}

func joinDisplay(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

type side struct {
	name  string
	data  []byte
	lines []string
	keys  []int
	noEOL bool
	mtime clock.Time
}

func loadSide(rc *toolcompat.RunContext, opts *options, name string) (*side, error) {
	s := &side{name: name}
	if name == "-" {
		data, err := io.ReadAll(rc.In)
		if err != nil {
			return nil, err
		}
		s.data = data

		now, err := clock.Now(rc.Ctx)
		if err != nil {
			return nil, err
		}
		s.mtime = now
		return s, nil
	}
	path := name
	fi, err := vfs.Stat(path)
	if err != nil {
		if opts.newFile && errors.Is(err, fs.ErrNotExist) {
			s.mtime = clock.Unix(0, 0)
			return s, nil
		}
		return nil, err
	}
	s.mtime = clock.Unix(fi.ModTime().Unix(), int64(fi.ModTime().Nanosecond()))
	if s.data, err = vfs.ReadFile(path); err != nil {
		return nil, err
	}
	return s, nil
}

func compareFiles(rc *toolcompat.RunContext, opts *options, a, b string, withHeader bool) int {
	sa, err := loadSide(rc, opts, a)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", a, errText(err))
		return 2
	}
	sb, err := loadSide(rc, opts, b)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "diff: %s: %s\n", b, errText(err))
		return 2
	}
	if bytes.Equal(sa.data, sb.data) {
		return 0
	}
	if isBinary(sa.data) || isBinary(sb.data) {
		if opts.brief {
			_, _ = fmt.Fprintf(rc.Out, "Files %s and %s differ\n", a, b)
		} else {
			if withHeader {
				printPairHeader(rc, opts, a, b)
			}
			_, _ = fmt.Fprintf(rc.Out, "Binary files %s and %s differ\n", a, b)
		}
		return 1
	}
	intern := map[string]int{}
	prep(sa, opts, intern)
	prep(sb, opts, intern)
	gs := diffGroups(sa.keys, sb.keys)
	if len(gs) == 0 {
		return 0
	}
	if opts.brief {
		_, _ = fmt.Fprintf(rc.Out, "Files %s and %s differ\n", a, b)
		return 1
	}
	if withHeader {
		printPairHeader(rc, opts, a, b)
	}
	switch {
	case opts.edStyle:
		emitEd(rc.Out, sb, gs)
	case opts.forwardEdStyle:
		emitForwardEd(rc.Out, sb, gs)
	case opts.unified:
		emitUnified(rc.Out, opts, sa, sb, gs, zoneLocation())
	case opts.contextStyle:
		emitContext(rc.Out, opts, sa, sb, gs, zoneLocation())
	default:
		emitNormal(rc.Out, sa, sb, gs)
	}
	if opts.edStyle || opts.forwardEdStyle {
		trouble := false
		if sa.noEOL {
			_, _ = fmt.Fprintf(rc.Err, "diff: %s: No newline at end of file\n\n", a)
			trouble = true
		}
		if sb.noEOL {
			_, _ = fmt.Fprintf(rc.Err, "diff: %s: No newline at end of file\n\n", b)
			trouble = true
		}
		if trouble {
			return 2
		}
	}
	return 1
}

func printPairHeader(rc *toolcompat.RunContext, opts *options, a, b string) {
	parts := append([]string{"diff"}, opts.optTokens...)
	parts = append(parts, a, b)
	_, _ = fmt.Fprintln(rc.Out, strings.Join(parts, " "))
}

func isBinary(data []byte) bool { return bytes.IndexByte(data, 0) >= 0 }

func prep(s *side, opts *options, intern map[string]int) {
	if len(s.data) == 0 {
		return
	}
	d := s.data
	if d[len(d)-1] == '\n' {
		d = d[:len(d)-1]
	} else {
		s.noEOL = true
	}
	s.lines = strings.Split(string(d), "\n")
	s.keys = make([]int, len(s.lines))
	for i, ln := range s.lines {
		k := canon(ln, opts)
		if s.noEOL && i == len(s.lines)-1 && !opts.ignoreAllSpace && !opts.ignoreSpaceChange {
			k += "\x00"
		}
		id, ok := intern[k]
		if !ok {
			id = len(intern)
			intern[k] = id
		}
		s.keys[i] = id
	}
}

func canon(line string, opts *options) string {
	switch {
	case opts.ignoreAllSpace:
		b := make([]byte, 0, len(line))
		for i := 0; i < len(line); i++ {
			if !isSpaceByte(line[i]) {
				b = append(b, line[i])
			}
		}
		line = string(b)
	case opts.ignoreSpaceChange:
		b := make([]byte, 0, len(line))
		run := false
		for i := 0; i < len(line); i++ {
			c := line[i]
			if isSpaceByte(c) {
				run = true
				continue
			}
			if run {
				b = append(b, ' ')
				run = false
			}
			b = append(b, c)
		}
		line = string(b)
	}
	if opts.ignoreCase {
		b := []byte(line)
		for i, c := range b {
			if c >= 'A' && c <= 'Z' {
				b[i] = c + 32
			}
		}
		line = string(b)
	}
	return line
}

func isSpaceByte(c byte) bool {
	switch c {
	case ' ', '\t', '\v', '\f', '\r':
		return true
	}
	return false
}

func errText(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "No such file or directory"
	}
	return toolcompat.SysErrString(err)
}

type group struct{ a0, a1, b0, b1 int }

func diffGroups(ka, kb []int) []group {
	ops := myersOps(ka, kb)
	ca := make([]bool, len(ka))
	cb := make([]bool, len(kb))
	ai, bi := 0, 0
	for _, k := range ops {
		switch k {
		case opEq:
			ai++
			bi++
		case opDel:
			ca[ai] = true
			ai++
		case opIns:
			cb[bi] = true
			bi++
		}
	}
	slideDown(ca, ka)
	slideDown(cb, kb)

	var gs []group
	i, j := 0, 0
	for i < len(ka) || j < len(kb) {
		if (i < len(ka) && ca[i]) || (j < len(kb) && cb[j]) {
			g := group{a0: i, b0: j}
			for i < len(ka) && ca[i] {
				i++
			}
			for j < len(kb) && cb[j] {
				j++
			}
			g.a1, g.b1 = i, j
			gs = append(gs, g)
		} else {
			i++
			j++
		}
	}
	return gs
}

func slideDown(changed []bool, keys []int) {
	n := len(keys)
	i := 0
	for i < n {
		if !changed[i] {
			i++
			continue
		}
		start := i
		for i < n && changed[i] {
			i++
		}

		for i < n && !changed[i] && keys[start] == keys[i] {
			changed[start] = false
			changed[i] = true
			start++
			i++
			for i < n && changed[i] {
				i++
			}
		}
	}
}

const noNewline = `\ No newline at end of file`

func emitEd(w io.Writer, sb *side, gs []group) {
	for n := len(gs) - 1; n >= 0; n-- {
		g := gs[n]
		da, db := g.a1-g.a0, g.b1-g.b0
		switch {
		case da > 0 && db > 0:
			_, _ = fmt.Fprintf(w, "%sc\n", normalRange(g.a0, da))
			emitEdBlock(w, sb.lines[g.b0:g.b1])
		case da > 0:
			_, _ = fmt.Fprintf(w, "%sd\n", normalRange(g.a0, da))
		default:
			_, _ = fmt.Fprintf(w, "%da\n", g.a0)
			emitEdBlock(w, sb.lines[g.b0:g.b1])
		}
	}
}

func emitEdBlock(w io.Writer, lines []string) {
	inInput := true
	for i, line := range lines {
		if line != "." {
			_, _ = fmt.Fprintln(w, line)
			continue
		}
		_, _ = fmt.Fprintln(w, "..")
		_, _ = fmt.Fprintln(w, ".")
		_, _ = fmt.Fprintln(w, "s/.//")
		inInput = false
		if i+1 < len(lines) {
			_, _ = fmt.Fprintln(w, "a")
			inInput = true
		}
	}
	if inInput {
		_, _ = fmt.Fprintln(w, ".")
	}
}

func emitForwardEd(w io.Writer, sb *side, gs []group) {
	for _, g := range gs {
		da, db := g.a1-g.a0, g.b1-g.b0
		switch {
		case da > 0 && db > 0:
			_, _ = fmt.Fprintf(w, "c%s\n", forwardEdRange(g.a0, da))
			emitForwardEdBlock(w, sb.lines[g.b0:g.b1])
		case da > 0:
			_, _ = fmt.Fprintf(w, "d%s\n", forwardEdRange(g.a0, da))
		default:
			_, _ = fmt.Fprintf(w, "a%d\n", g.a0)
			emitForwardEdBlock(w, sb.lines[g.b0:g.b1])
		}
	}
}

func forwardEdRange(start0, count int) string {
	if count == 1 {
		return strconv.Itoa(start0 + 1)
	}
	return fmt.Sprintf("%d %d", start0+1, start0+count)
}

func emitForwardEdBlock(w io.Writer, lines []string) {
	for _, line := range lines {
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintln(w, ".")
}

func emitNormal(w io.Writer, sa, sb *side, gs []group) {
	for _, g := range gs {
		da, db := g.a1-g.a0, g.b1-g.b0
		switch {
		case da > 0 && db > 0:
			_, _ = fmt.Fprintf(w, "%sc%s\n", normalRange(g.a0, da), normalRange(g.b0, db))
		case da > 0:
			_, _ = fmt.Fprintf(w, "%sd%d\n", normalRange(g.a0, da), g.b0)
		default:
			_, _ = fmt.Fprintf(w, "%da%s\n", g.a0, normalRange(g.b0, db))
		}
		for i := g.a0; i < g.a1; i++ {
			_, _ = fmt.Fprintf(w, "< %s\n", sa.lines[i])
			if sa.noEOL && i == len(sa.lines)-1 {
				_, _ = fmt.Fprintln(w, noNewline)
			}
		}
		if da > 0 && db > 0 {
			_, _ = fmt.Fprintln(w, "---")
		}
		for i := g.b0; i < g.b1; i++ {
			_, _ = fmt.Fprintf(w, "> %s\n", sb.lines[i])
			if sb.noEOL && i == len(sb.lines)-1 {
				_, _ = fmt.Fprintln(w, noNewline)
			}
		}
	}
}

func normalRange(start0, count int) string {
	if count == 1 {
		return strconv.Itoa(start0 + 1)
	}
	return fmt.Sprintf("%d,%d", start0+1, start0+count)
}

const (
	stampLayout = "2006-01-02 15:04:05.000000000 -0700"

	contextStampLayout = "Mon Jan _2 15:04:05 2006"
)

func zoneLocation() *clock.Location { return clock.UTC }

func emitUnified(w io.Writer, opts *options, sa, sb *side, gs []group, loc *clock.Location) {
	_, _ = fmt.Fprintf(w, "--- %s\t%s\n", sa.name, sa.mtime.In(loc).Format(stampLayout))
	_, _ = fmt.Fprintf(w, "+++ %s\t%s\n", sb.name, sb.mtime.In(loc).Format(stampLayout))
	ctx := opts.context
	for i := 0; i < len(gs); {
		j := i
		for j+1 < len(gs) && gs[j+1].a0-gs[j].a1 <= 2*ctx {
			j++
		}
		hs := gs[i].a0 - ctx
		if hs < 0 {
			hs = 0
		}
		he := gs[j].a1 + ctx
		if he > len(sa.lines) {
			he = len(sa.lines)
		}
		hbs := gs[i].b0 - (gs[i].a0 - hs)
		hbe := gs[j].b1 + (he - gs[j].a1)
		_, _ = fmt.Fprintf(w, "@@ -%s +%s @@\n", unifiedRange(hs, he-hs), unifiedRange(hbs, hbe-hbs))
		ai, bi := hs, hbs

		context := func() {
			_, _ = fmt.Fprintf(w, " %s\n", sa.lines[ai])
			if sa.noEOL && ai == len(sa.lines)-1 {
				_, _ = fmt.Fprintln(w, noNewline)
			}
			ai++
			bi++
		}
		for g := i; g <= j; g++ {
			for ai < gs[g].a0 {
				context()
			}
			for ; ai < gs[g].a1; ai++ {
				_, _ = fmt.Fprintf(w, "-%s\n", sa.lines[ai])
				if sa.noEOL && ai == len(sa.lines)-1 {
					_, _ = fmt.Fprintln(w, noNewline)
				}
			}
			for ; bi < gs[g].b1; bi++ {
				_, _ = fmt.Fprintf(w, "+%s\n", sb.lines[bi])
				if sb.noEOL && bi == len(sb.lines)-1 {
					_, _ = fmt.Fprintln(w, noNewline)
				}
			}
		}
		for ai < he {
			context()
		}
		i = j + 1
	}
}

func unifiedRange(start0, count int) string {
	switch count {
	case 1:
		return strconv.Itoa(start0 + 1)
	case 0:
		return fmt.Sprintf("%d,0", start0)
	default:
		return fmt.Sprintf("%d,%d", start0+1, count)
	}
}

func emitContext(w io.Writer, opts *options, sa, sb *side, gs []group, loc *clock.Location) {
	_, _ = fmt.Fprintf(w, "*** %s\t%s\n", sa.name, sa.mtime.In(loc).Format(contextStampLayout))
	_, _ = fmt.Fprintf(w, "--- %s\t%s\n", sb.name, sb.mtime.In(loc).Format(contextStampLayout))
	line := func(s *side, mark string, i int) {
		_, _ = fmt.Fprintf(w, "%s%s\n", mark, s.lines[i])
		if s.noEOL && i == len(s.lines)-1 {
			_, _ = fmt.Fprintln(w, noNewline)
		}
	}
	ctx := opts.context
	for i := 0; i < len(gs); {
		j := i
		for j+1 < len(gs) && gs[j+1].a0-gs[j].a1 <= 2*ctx {
			j++
		}
		hs := gs[i].a0 - ctx
		if hs < 0 {
			hs = 0
		}
		he := gs[j].a1 + ctx
		if he > len(sa.lines) {
			he = len(sa.lines)
		}
		hbs := gs[i].b0 - (gs[i].a0 - hs)
		hbe := gs[j].b1 + (he - gs[j].a1)
		hasDel, hasIns := false, false
		for g := i; g <= j; g++ {
			hasDel = hasDel || gs[g].a1 > gs[g].a0
			hasIns = hasIns || gs[g].b1 > gs[g].b0
		}
		_, _ = fmt.Fprintln(w, "***************")
		_, _ = fmt.Fprintf(w, "*** %s ****\n", contextRange(hs, he-hs))
		if hasDel {
			ai := hs
			for g := i; g <= j; g++ {
				for ; ai < gs[g].a0; ai++ {
					line(sa, "  ", ai)
				}
				mark := "- "
				if gs[g].b1 > gs[g].b0 {
					mark = "! "
				}
				for ; ai < gs[g].a1; ai++ {
					line(sa, mark, ai)
				}
			}
			for ; ai < he; ai++ {
				line(sa, "  ", ai)
			}
		}
		_, _ = fmt.Fprintf(w, "--- %s ----\n", contextRange(hbs, hbe-hbs))
		if hasIns {
			bi := hbs
			for g := i; g <= j; g++ {
				for ; bi < gs[g].b0; bi++ {
					line(sb, "  ", bi)
				}
				mark := "+ "
				if gs[g].a1 > gs[g].a0 {
					mark = "! "
				}
				for ; bi < gs[g].b1; bi++ {
					line(sb, mark, bi)
				}
			}
			for ; bi < hbe; bi++ {
				line(sb, "  ", bi)
			}
		}
		i = j + 1
	}
}

func contextRange(start0, count int) string {
	if count <= 1 {
		return strconv.Itoa(start0 + count)
	}
	return fmt.Sprintf("%d,%d", start0+1, start0+count)
}
