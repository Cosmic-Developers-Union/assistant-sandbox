package ducmd

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

var cmd = &toolcompat.Tool{
	Name:     "du",
	Synopsis: "Summarize device usage of the set of FILEs, recursively for directories. Supports -D, -H, and -M aliases.",
	Usage:    "du [OPTION]... [FILE]...",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

type duRun struct {
	rc            *toolcompat.RunContext
	all           bool
	apparent      bool
	human         bool
	si            bool
	countLinks    bool
	inodes        bool
	separateDirs  bool
	oneFileSystem bool
	showTime      bool
	timeSel       timeSel
	timeStyle     string
	block         int64
	maxDepth      int
	threshold     int64
	haveThreshold bool
	deref         derefMode
	exit          int
	excludes      []string
	term          string
	outputFailed  bool
}

type derefMode int

const (
	derefNone derefMode = iota
	derefArgs
	derefAll
)

type duEntry struct {
	size int64
	mod  clock.Time
	dir  bool
}

type timeSel int

const (
	timeMtime timeSel = iota
	timeAtime
	timeCtime
)

func run(rc *toolcompat.RunContext, args []string) int {
	args = normalizeBlockSizeArgs(args)

	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			break
		}
		if w, ok := strings.CutPrefix(a, "--time="); ok {
			switch w {
			case "atime", "access", "use", "ctime", "status":
			default:
				return toolcompat.UsageError(rc, cmd, "unsupported --time=%s", w)
			}
		}
	}
	fs := toolcompat.NewFlags(cmd.Name)

	deref := derefNone
	setDeref := func(mode derefMode) func(string) error {
		return func(value string) error {
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			if enabled {
				deref = mode
			}
			return nil
		}
	}
	all := fs.BoolP("all", "a", false, "write counts for all files, not just directories")
	apparentSize := fs.BoolP("apparent-size", "A", false, "print apparent sizes, rather than disk usage (客体没有块数，默认即按大小)")
	bytesMode := fs.BoolP("bytes", "b", false, "equivalent to '--apparent-size --block-size=1'")
	blockSize := fs.StringP("block-size", "B", "", "scale sizes by SIZE before printing them")
	fs.BoolFunc("dereference-args", "dereference only symlinks that are listed on the command line", setDeref(derefArgs))
	fs.BoolFuncP("dereference-args-short", "D", "dereference only symlinks that are listed on the command line", setDeref(derefArgs))
	fs.BoolFuncP("dereference-args-H", "H", "equivalent to --dereference-args", setDeref(derefArgs))
	fs.BoolFuncP("dereference", "L", "dereference all symbolic links", setDeref(derefAll))
	fs.BoolFuncP("no-dereference", "P", "do not follow any symbolic links", setDeref(derefNone))
	kib := fs.BoolP("kilobytes", "k", false, "like --block-size=1K")
	mib := fs.BoolP("megabytes", "m", false, "like --block-size=1M")
	fs.BoolP("megabytes-short", "M", false, "like --block-size=1M")
	total := fs.BoolP("total", "c", false, "produce a grand total")
	countLinks := fs.BoolP("count-links", "l", false, "count sizes many times if hard linked")
	inodes := fs.Bool("inodes", false, "list inode usage information instead of block usage")
	separateDirs := fs.BoolP("separate-dirs", "S", false, "for directories do not include size of subdirectories")
	oneFileSystem := fs.BoolP("one-file-system", "x", false, "skip directories on different file systems")
	thresholdArg := fs.StringP("threshold", "t", "", "exclude entries smaller than SIZE if positive, or greater than SIZE if negative")
	showTime := fs.String("time", "", "show time of the last modification of any file in the directory, or WORD: atime/access/use, ctime/status")
	if f := fs.Lookup("time"); f != nil {
		f.NoOptDefVal = "mtime"
	}
	timeStyle := fs.String("time-style", "", "time/date format with --time: full-iso, long-iso, iso, or +FORMAT")
	verbose := fs.BoolP("verbose", "v", false, "write counts for all files, not just directories")
	maxDepth := fs.IntP("max-depth", "d", -1, "print the total for a directory (or file, with --all) only if it is N or fewer levels below the command line argument")
	human := fs.BoolP("human-readable", "h", false, "print sizes in human readable format (e.g., 1K 234M 2G)")
	si := fs.Bool("si", false, "like -h, but use powers of 1000 not 1024")
	null := fs.BoolP("null", "0", false, "end each output line with NUL, not newline")
	exclude := fs.StringArray("exclude", nil, "exclude files that match PATTERN")
	excludeFrom := fs.StringArrayP("exclude-from", "X", nil, "read exclude patterns from FILE, one per line")
	files0From := fs.String("files0-from", "", "read input file names from FILE, terminated by NUL; if FILE is -, read names from standard input")
	summarize := fs.BoolP("summarize", "s", false, "display only a total for each argument")
	operands, code := toolcompat.ParseRequireOrder(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}

	if *verbose {
		*all = true
	}
	if *oneFileSystem {

		return toolcompat.NotSupported(rc, cmd, "-x/--one-file-system")
	}
	if *summarize && *all {
		return toolcompat.UsageError(rc, cmd, "cannot both summarize and show all entries")
	}
	if *summarize && fs.Changed("max-depth") {
		return toolcompat.UsageError(rc, cmd, "warning: summarizing conflicts with --max-depth=%d", *maxDepth)
	}
	if fs.Changed("max-depth") && *maxDepth < 0 {
		return toolcompat.UsageError(rc, cmd, "invalid maximum depth '%d'", *maxDepth)
	}
	var threshold int64
	if fs.Changed("threshold") {
		var err error
		threshold, err = parseSignedSize(*thresholdArg)
		if err != nil {
			return toolcompat.UsageError(rc, cmd, "invalid threshold %q", *thresholdArg)
		}
	}
	if fs.Changed("time-style") && !fs.Changed("time") {
		*showTime = "mtime"
	}

	sel := timeMtime
	switch *showTime {
	case "atime", "access", "use":
		sel = timeAtime
	case "ctime", "status":
		sel = timeCtime
	}
	if sel != timeMtime && !haveSysTimes {
		return toolcompat.NotSupported(rc, cmd, "--time="+*showTime+" on this platform")
	}
	if fs.Changed("time-style") {
		if err := validateTimeStyle(*timeStyle); err != nil {
			return toolcompat.UsageError(rc, cmd, "%s", err)
		}
	}
	if fs.Changed("files0-from") {
		if len(operands) > 0 {
			return toolcompat.UsageError(rc, cmd, "file operands cannot be combined with --files0-from")
		}
		var err error
		operands, err = readFiles0(rc, *files0From)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "du: %s: %s\n", *files0From, errMsg(err))
			return 1
		}
	}
	patterns := append([]string{}, *exclude...)
	for _, name := range *excludeFrom {
		more, err := readExcludePatterns(rc, name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "du: %s: %s\n", name, errMsg(err))
			return 1
		}
		patterns = append(patterns, more...)
	}

	depth := math.MaxInt
	switch {
	case *summarize:
		depth = 0
	case fs.Changed("max-depth"):
		depth = *maxDepth
	}
	block := int64(512)

	_ = *apparentSize
	if *bytesMode {
		block = 1
	}
	if *kib {
		block = 1024
	}
	if *mib {
		block = 1024 * 1024
	}
	if fs.Changed("block-size") {
		var err error
		block, err = parseBlockSize(*blockSize)
		if err != nil {
			return toolcompat.UsageError(rc, cmd, "invalid block size %q", *blockSize)
		}
	}
	term := "\n"
	if *null {
		term = "\x00"
	}
	d := &duRun{
		rc:  rc,
		all: *all,

		apparent:      true,
		human:         *human,
		si:            *si,
		countLinks:    *countLinks,
		inodes:        *inodes,
		separateDirs:  *separateDirs,
		oneFileSystem: *oneFileSystem,
		showTime:      fs.Changed("time") || fs.Changed("time-style"),
		timeSel:       sel,
		timeStyle:     *timeStyle,
		block:         block,
		maxDepth:      depth,
		threshold:     threshold,
		haveThreshold: fs.Changed("threshold"),
		deref:         deref,

		excludes: patterns,
		term:     term,
	}

	if len(operands) == 0 {
		operands = []string{"."}
	}
	var grand duEntry
	for _, op := range operands {
		e, ok := d.walk(op, rc.Path(op), 0, nil)
		if ok {
			grand.size += e.size
			grand.mod = maxTime(grand.mod, e.mod)
		}
	}
	if *total {
		d.print(grand.size, "total", grand.mod)
	}
	return d.exit
}

func normalizeBlockSizeArgs(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			out = append(out, args[i:]...)
			break
		}
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			if idx := strings.IndexByte(a[1:], 'B'); idx >= 0 {
				idx++
				if idx < len(a)-1 {
					if idx > 1 {
						out = append(out, "-"+a[1:idx])
					}
					out = append(out, "--block-size="+a[idx+1:])
					continue
				}
			}
			if strings.Contains(a, "M") {
				var cluster strings.Builder
				flush := func() {
					if cluster.Len() == 0 {
						return
					}
					out = append(out, "-"+cluster.String())
					cluster.Reset()
				}
				for j := 1; j < len(a); j++ {
					if a[j] == 'M' {
						flush()
						out = append(out, "--block-size=1M")
						continue
					}
					cluster.WriteByte(a[j])
				}
				flush()
				continue
			}
		}
		if a == "-M" {
			out = append(out, "--block-size=1M")
			continue
		}
		out = append(out, a)
	}
	return out
}

func (d *duRun) walk(display, full string, depth int, rootDev *uint64) (duEntry, bool) {
	if d.excluded(display) {
		return duEntry{}, true
	}
	fi, err := d.stat(full, depth == 0)
	if err != nil {
		_, _ = fmt.Fprintf(d.rc.Err, "du: cannot access '%s': %s\n", display, errMsg(err))
		d.exit = 1
		return duEntry{}, false
	}
	if d.oneFileSystem {
		if dev, ok := fileDev(fi); ok {
			if rootDev == nil {
				rootDev = &dev
			} else if depth > 0 && dev != *rootDev {
				return duEntry{}, true
			}
		}
	}
	if fi.IsDir() {

		var total int64
		if d.inodes {
			total = d.amount(fi)
		}
		mod := d.fileTime(fi)
		ents, rerr := vfs.ReadDir(full)
		if rerr != nil {
			_, _ = fmt.Fprintf(d.rc.Err, "du: cannot read directory '%s': %s\n", display, errMsg(rerr))
			d.exit = 1
		}
		for _, de := range ents {
			e, _ := d.walk(joinDisplay(display, de.Name()), path.Join(full, de.Name()), depth+1, rootDev)
			if !d.separateDirs || !e.dir {
				total += e.size
			}
			mod = maxTime(mod, e.mod)
		}
		if depth <= d.maxDepth {
			d.print(total, display, mod)
		}
		return duEntry{size: total, mod: mod, dir: true}, true
	}
	if d.skipHardlink(fi) {
		return duEntry{}, true
	}
	total := d.amount(fi)
	ft := d.fileTime(fi)

	if depth == 0 || (d.all && depth <= d.maxDepth) {
		d.print(total, display, ft)
	}
	return duEntry{size: total, mod: ft}, true
}

func (d *duRun) fileTime(fi fs.FileInfo) clock.Time {

	modified := fi.ModTime()
	return clock.Unix(modified.Unix(), int64(modified.Nanosecond()))
}

func (d *duRun) stat(full string, root bool) (fs.FileInfo, error) {
	if d.deref == derefAll || (root && d.deref == derefArgs) {
		return vfs.Stat(full)
	}
	return vfs.Lstat(full)
}

func (d *duRun) amount(fi fs.FileInfo) int64 {
	if d.inodes {
		return 1
	}
	return d.usage(fi)
}

func (d *duRun) print(n int64, path string, mod clock.Time) {
	if d.outputFailed {
		return
	}
	if d.haveThreshold {
		if d.threshold >= 0 && n < d.threshold {
			return
		}
		if d.threshold < 0 && n > -d.threshold {
			return
		}
	}
	if d.showTime {
		d.writeOutput(fmt.Sprintf("%s\t%s\t%s%s", d.fmtSize(n), d.fmtTime(mod), path, d.term))
		return
	}

	d.writeOutput(fmt.Sprintf("%s %s%s", d.fmtSize(n), path, d.term))
}

func (d *duRun) writeOutput(s string) {
	n, err := io.WriteString(d.rc.Out, s)
	if err == nil && n != len(s) {
		err = io.ErrShortWrite
	}
	if err == nil {
		return
	}
	d.outputFailed = true
	d.exit = 1
	_, _ = fmt.Fprintf(d.rc.Err, "du: write error: %s\n", errMsg(err))
}

func (d *duRun) fmtSize(n int64) string {
	if d.inodes {
		return strconv.FormatInt(n, 10)
	}
	switch {
	case d.human:
		return humanSize(uint64(n))
	case d.si:
		return siSize(uint64(n))
	default:
		return strconv.FormatInt(divCeil(n, d.block), 10)
	}
}

func (d *duRun) fmtTime(t clock.Time) string {
	if t.IsZero() {
		t = clock.Unix(0, 0)
	}
	switch d.timeStyle {
	case "", "long-iso":
		return t.Format("2006-01-02 15:04")
	case "full-iso":
		return t.Format("2006-01-02 15:04:05.000000000 -0700")
	case "iso":
		return t.Format("2006-01-02")
	default:
		if strings.HasPrefix(d.timeStyle, "+") {
			return t.Format(goTimeLayout(d.timeStyle[1:]))
		}
		return t.Format("2006-01-02 15:04")
	}
}

func validateTimeStyle(style string) error {
	switch {
	case style == "", style == "full-iso", style == "long-iso", style == "iso":
		return nil
	case strings.HasPrefix(style, "+"):
		return nil
	default:
		return fmt.Errorf("unsupported --time-style=%s", style)
	}
}

func goTimeLayout(format string) string {
	repl := strings.NewReplacer(
		"%Y", "2006",
		"%y", "06",
		"%m", "01",
		"%d", "02",
		"%H", "15",
		"%M", "04",
		"%S", "05",
		"%F", "2006-01-02",
		"%T", "15:04:05",
		"%%", "%",
	)
	return repl.Replace(format)
}

func maxTime(a, b clock.Time) clock.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (d *duRun) excluded(display string) bool {
	if len(d.excludes) == 0 {
		return false
	}
	clean := path.Clean(display)
	base := path.Base(clean)
	for _, pat := range d.excludes {
		pat = path.Clean(pat)
		if pat == "" {
			continue
		}
		if ok, _ := path.Match(pat, clean); ok {
			return true
		}
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
		if pat == clean || pat == base {
			return true
		}
	}
	return false
}

func joinDisplay(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

func errMsg(err error) string {
	return toolcompat.SysErrString(err)
}

func readFiles0(rc *toolcompat.RunContext, name string) ([]string, error) {
	var data []byte
	var err error
	if name == "-" {
		data, err = io.ReadAll(rc.In)
	} else {
		data, err = vfs.ReadFile(rc.Path(name))
	}
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(data, []byte{0})
	names := make([]string, 0, len(parts))
	for i, p := range parts {
		if len(p) == 0 {
			if i == len(parts)-1 {
				continue
			}
			return nil, fmt.Errorf("invalid zero-length file name")
		}
		names = append(names, string(p))
	}
	return names, nil
}

func readExcludePatterns(rc *toolcompat.RunContext, name string) ([]string, error) {
	var data []byte
	var err error
	if name == "-" {
		data, err = io.ReadAll(rc.In)
	} else {
		data, err = vfs.ReadFile(rc.Path(name))
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	patterns := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		patterns = append(patterns, ln)
	}
	return patterns, nil
}

func parseBlockSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty block size")
	}
	mult := int64(1)
	num := strings.ToUpper(strings.TrimSpace(s))
	switch {
	case strings.HasSuffix(num, "KIB"):
		mult, num = 1024, strings.TrimSuffix(num, "KIB")
	case strings.HasSuffix(num, "MIB"):
		mult, num = 1024*1024, strings.TrimSuffix(num, "MIB")
	case strings.HasSuffix(num, "GIB"):
		mult, num = 1024*1024*1024, strings.TrimSuffix(num, "GIB")
	case strings.HasSuffix(num, "TIB"):
		mult, num = 1024*1024*1024*1024, strings.TrimSuffix(num, "TIB")
	case strings.HasSuffix(num, "KB"):
		mult, num = 1000, strings.TrimSuffix(num, "KB")
	case strings.HasSuffix(num, "MB"):
		mult, num = 1000*1000, strings.TrimSuffix(num, "MB")
	case strings.HasSuffix(num, "GB"):
		mult, num = 1000*1000*1000, strings.TrimSuffix(num, "GB")
	case strings.HasSuffix(num, "TB"):
		mult, num = 1000*1000*1000*1000, strings.TrimSuffix(num, "TB")
	case strings.HasSuffix(num, "K"):
		mult, num = 1024, strings.TrimSuffix(num, "K")
	case strings.HasSuffix(num, "M"):
		mult, num = 1024*1024, strings.TrimSuffix(num, "M")
	case strings.HasSuffix(num, "G"):
		mult, num = 1024*1024*1024, strings.TrimSuffix(num, "G")
	case strings.HasSuffix(num, "T"):
		mult, num = 1024*1024*1024*1024, strings.TrimSuffix(num, "T")
	}
	if num == "" {
		num = "1"
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bad size")
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size overflow")
	}
	return n * mult, nil
}

func parseSignedSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	sign := int64(1)
	if strings.HasPrefix(s, "-") {
		sign = -1
		s = strings.TrimPrefix(s, "-")
	} else if strings.HasPrefix(s, "+") {
		s = strings.TrimPrefix(s, "+")
	}
	n, err := parseBlockSize(s)
	if err != nil {
		return 0, err
	}
	return sign * n, nil
}

func divCeil(n, d int64) int64 {
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

func humanSize(n uint64) string {
	if n < 1024 {
		return strconv.FormatUint(n, 10)
	}
	const units = "KMGTPE"
	div := uint64(1024)
	idx := 0
	for n/div >= 1024 && idx < len(units)-1 {
		div *= 1024
		idx++
	}
	whole, rem := n/div, n%div
	if whole < 10 {
		tenths := whole*10 + (rem*10+div-1)/div
		if tenths < 100 {
			return fmt.Sprintf("%d.%d%c", tenths/10, tenths%10, units[idx])
		}
		return fmt.Sprintf("10%c", units[idx])
	}
	v := whole
	if rem > 0 {
		v++
	}
	if v >= 1024 && idx < len(units)-1 {
		return fmt.Sprintf("1.0%c", units[idx+1])
	}
	return fmt.Sprintf("%d%c", v, units[idx])
}

func siSize(n uint64) string {
	if n < 1000 {
		return strconv.FormatUint(n, 10)
	}
	const units = "KMGTPE"
	div := uint64(1000)
	idx := 0
	for n/div >= 1000 && idx < len(units)-1 {
		div *= 1000
		idx++
	}
	whole, rem := n/div, n%div
	if whole < 10 {
		tenths := whole*10 + (rem*10+div-1)/div
		if tenths < 100 {
			return fmt.Sprintf("%d.%d%c", tenths/10, tenths%10, units[idx])
		}
		return fmt.Sprintf("10%c", units[idx])
	}
	v := whole
	if rem > 0 {
		v++
	}
	if v >= 1000 && idx < len(units)-1 {
		return fmt.Sprintf("1.0%c", units[idx+1])
	}
	return fmt.Sprintf("%d%c", v, units[idx])
}
