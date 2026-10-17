package datecmd

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

var cmd = &toolcompat.Tool{
	Name:     "date",
	Synopsis: "Display or set date and time using the selected locale.",
	Usage:    "date [-u] [+FORMAT]\n       date [-u] mmddhhmm[[cc]yy]",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	return runWithClock(rc, args, func() (clock.Time, error) { return clock.Now(rc.Ctx) }, setSystemClock)
}

type clockSetter func(clock.Time) error

func runWithClock(rc *toolcompat.RunContext, args []string, now func() (clock.Time, error), setClock clockSetter) int {
	fs := toolcompat.NewFlags(cmd.Name)
	utc := fs.BoolP("utc", "u", false, "print in Coordinated Universal Time (UTC)")
	universal := fs.Bool("universal", false, "same as --utc")
	dstr := fs.StringP("date", "d", "", "display time described by STRING, not 'now'")
	ref := fs.StringP("reference", "r", "", "display the last modification time of FILE")
	dateFile := fs.StringP("file", "f", "", "like --date once for each line of FILE")
	debug := fs.Bool("debug", false, "annotate parsed dates on stderr")
	iso8601 := fs.StringP("iso-8601", "I", "", "output date/time in ISO 8601 format")
	fs.Lookup("iso-8601").NoOptDefVal = "date"
	rfc3339 := fs.String("rfc-3339", "", "output date/time in RFC 3339 format")
	rfcEmail := fs.BoolP("rfc-email", "R", false, "output date and time in RFC 5322 email format")
	rfc822 := fs.Bool("rfc-822", false, "output date and time in RFC 5322 email format")
	fs.Lookup("rfc-822").Hidden = true
	rfc2822 := fs.Bool("rfc-2822", false, "output date and time in RFC 5322 email format")
	fs.Lookup("rfc-2822").Hidden = true
	uct := fs.Bool("uct", false, "print in Coordinated Universal Time (UTC)")
	fs.Lookup("uct").Hidden = true
	resolution := fs.Bool("resolution", false, "output the available timestamp resolution")
	setDate := fs.StringP("set", "s", "", "set time described by STRING")
	operands, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	if *setDate != "" {
		return toolcompat.NotSupported(rc, cmd, "setting the system date")
	}

	loc := clock.Local
	if !*utc && !*universal && !*uct {

		resolved, zoneErr := dateLocation(rc.Env)
		if zoneErr != nil {

			_, _ = fmt.Fprintf(rc.Err, "date: %v\n", zoneErr)
			return 1
		}
		loc = resolved
	}

	names, localeErr := selectDateLocale(rc.Env)
	if *utc || *universal || *uct {
		loc = clock.UTC
	}

	sources := 0
	for _, set := range []bool{*dstr != "", *ref != "", *dateFile != ""} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return toolcompat.UsageError(rc, cmd, "the options to specify dates for printing are mutually exclusive")
	}
	if *resolution {
		if sources > 0 || len(operands) > 0 {
			return toolcompat.UsageError(rc, cmd, "--resolution is mutually exclusive with date formatting")
		}
		return writeOutput(rc, "0.000000001\n")
	}
	if len(operands) > 0 && !strings.HasPrefix(operands[0], "+") {
		if len(operands) > 1 {
			return toolcompat.UsageError(rc, cmd, "extra operand %q", operands[1])
		}
		formatOption := fs.Changed("iso-8601") || *rfc3339 != "" || *rfcEmail || *rfc822 || *rfc2822
		if sources != 0 || formatOption || *debug {
			return toolcompat.UsageError(rc, cmd, "the XSI set-date operand cannot be combined with a date source or output-format option")
		}
		current, err := now()
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "date: %v\n", err)
			return 1
		}
		target, err := parseXSISetDate(operands[0], current.In(loc), loc)
		if err != nil {
			return toolcompat.UsageError(rc, cmd, "invalid XSI set-date operand %q: %v", operands[0], err)
		}

		if code := reportLocale(rc, "%a %b %e %H:%M:%S %Z %Y", localeErr); code >= 0 {
			return code
		}
		if err := setClock(target); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "date: cannot set date: %v\n", err)
			return 1
		}
		return writeOutput(rc, strftime(target.In(loc), "%a %b %e %H:%M:%S %Z %Y", names)+"\n")
	}
	format, code := selectFormat(rc, operands, *iso8601, fs.Changed("iso-8601"), *rfc3339, *rfcEmail || *rfc822 || *rfc2822)
	if code >= 0 {
		return code
	}
	if code := reportLocale(rc, format, localeErr); code >= 0 {
		return code
	}
	if *dateFile != "" {
		data, err := vfs.ReadFile(*dateFile)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "date: %s: %v\n", *dateFile, toolcompat.SysErr(err))
			return 1
		}
		status := 0
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			t, err := parseDateString(line, loc)
			if err != nil {
				_, _ = fmt.Fprintf(rc.Err, "date: invalid date %q (supported: RFC 3339, @EPOCH, \"YYYY-MM-DD [HH:MM[:SS]]\")\n", line)
				status = 1
				continue
			}
			if *debug {
				_, _ = fmt.Fprintf(rc.Err, "date: parsed date %q -> %s\n", line, t.In(loc).Format(clock.RFC3339Nano))
			}
			if writeOutput(rc, strftime(t.In(loc), format, names)+"\n") != 0 {
				return 1
			}
		}
		return status
	}

	t, err := now()
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "date: %v\n", err)
		return 1
	}
	switch {
	case *ref != "":
		fi, err := vfs.Stat(*ref)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "date: %s: %v\n", *ref, err)
			return 1
		}

		t = clock.Unix(fi.ModTime().Unix(), int64(fi.ModTime().Nanosecond()))
	case *dstr != "":
		var err error
		t, err = parseDateString(*dstr, loc)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "date: invalid date %q (supported: RFC 3339, @EPOCH, \"YYYY-MM-DD [HH:MM[:SS]]\")\n", *dstr)
			return 1
		}
	}
	t = t.In(loc)
	if *debug && *dstr != "" {
		_, _ = fmt.Fprintf(rc.Err, "date: parsed date %q -> %s\n", *dstr, t.Format(clock.RFC3339Nano))
	}

	return writeOutput(rc, strftime(t, format, names)+"\n")
}

func writeOutput(rc *toolcompat.RunContext, s string) int {
	n, err := fmt.Fprint(rc.Out, s)
	if err == nil && n != len(s) {
		err = io.ErrShortWrite
	}
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "date: write error: %v\n", err)
		return 1
	}
	return 0
}

func selectFormat(rc *toolcompat.RunContext, operands []string, iso string, isoSet bool, rfc3339 string, rfcEmail bool) (string, int) {
	format := "%a %b %e %H:%M:%S %Z %Y"
	formatCount := 0
	if len(operands) > 0 {
		if len(operands) > 1 {
			return "", toolcompat.UsageError(rc, cmd, "extra operand %q", operands[1])
		}
		if !strings.HasPrefix(operands[0], "+") {
			return "", toolcompat.UsageError(rc, cmd, "invalid date operand %q", operands[0])
		}
		format = operands[0][1:]
		formatCount++
	}
	if isoSet {
		f, code := isoFormat(rc, iso)
		if code >= 0 {
			return "", code
		}
		format = f
		formatCount++
	}
	if rfc3339 != "" {
		f, code := rfc3339Format(rc, rfc3339)
		if code >= 0 {
			return "", code
		}
		format = f
		formatCount++
	}
	if rfcEmail {
		format = "%a, %d %b %Y %H:%M:%S %z"
		formatCount++
	}
	if formatCount > 1 {
		return "", toolcompat.UsageError(rc, cmd, "multiple output formats specified")
	}
	return format, -1
}

func isSetDateOperand(s string) bool {
	switch len(s) {
	case 8, 10, 12:
	default:
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func parseXSISetDate(s string, now clock.Time, loc *clock.Location) (clock.Time, error) {
	if !isSetDateOperand(s) {
		return clock.Time{}, fmt.Errorf("expected mmddhhmm, mmddhhmmyy, or mmddhhmmccyy")
	}
	field := func(start int) int {
		n, _ := strconv.Atoi(s[start : start+2])
		return n
	}
	month, day, hour, minute := field(0), field(2), field(4), field(6)
	year := now.Year()
	switch len(s) {
	case 10:
		yy := field(8)
		if yy >= 69 {
			year = 1900 + yy
		} else {
			year = 2000 + yy
		}
	case 12:
		cc, yy := field(8), field(10)
		year = cc*100 + yy
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 {
		return clock.Time{}, fmt.Errorf("field outside its permitted range")
	}
	t := clock.Date(year, clock.Month(month), day, hour, minute, 0, 0, loc)
	if t.Year() != year || int(t.Month()) != month || t.Day() != day || t.Hour() != hour || t.Minute() != minute {
		return clock.Time{}, fmt.Errorf("date does not exist in the selected timezone")
	}
	return t, nil
}

func setSystemClock(clock.Time) error {
	return errors.New("客体不允许设置系统时钟")
}

func resolveZoneValue(value string) (*clock.Location, error) {
	loc, err := clock.LoadLocation(value)
	if err != nil {
		return nil, fmt.Errorf("date: TZ=%q: %w", value, err)
	}
	return loc, nil
}

func dateLocation(env []string) (*clock.Location, error) {
	for i := len(env) - 1; i >= 0; i-- {
		value, ok := strings.CutPrefix(env[i], "TZ=")
		if !ok {
			continue
		}
		if value == "" {
			return clock.Local, nil
		}
		return resolveZoneValue(value)
	}
	return clock.Local, nil
}

func isoFormat(rc *toolcompat.RunContext, spec string) (string, int) {
	switch spec {
	case "", "date":
		return "%Y-%m-%d", -1
	case "hours":
		return "%Y-%m-%dT%H%z", -1
	case "minutes":
		return "%Y-%m-%dT%H:%M%z", -1
	case "seconds":
		return "%Y-%m-%dT%H:%M:%S%z", -1
	case "ns", "nanoseconds":
		return "%Y-%m-%dT%H:%M:%S,%N%z", -1
	default:
		return "", toolcompat.UsageError(rc, cmd, "invalid --iso-8601 timespec: %q", spec)
	}
}

func rfc3339Format(rc *toolcompat.RunContext, spec string) (string, int) {
	switch spec {
	case "date":
		return "%Y-%m-%d", -1
	case "seconds":
		return "%Y-%m-%d %H:%M:%S%:z", -1
	case "ns", "nanoseconds":
		return "%Y-%m-%d %H:%M:%S.%N%:z", -1
	default:
		return "", toolcompat.UsageError(rc, cmd, "invalid --rfc-3339 timespec: %q", spec)
	}
}

func parseDateString(s string, loc *clock.Location) (clock.Time, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "@"); ok {
		secs, frac, _ := strings.Cut(rest, ".")
		sec, err := strconv.ParseInt(secs, 10, 64)
		if err != nil {
			return clock.Time{}, err
		}
		var nsec int64
		if frac != "" {
			f, err := strconv.ParseFloat("0."+frac, 64)
			if err != nil {
				return clock.Time{}, err
			}
			nsec = int64(f * 1e9)
			if sec < 0 {
				nsec = -nsec
			}
		}
		return clock.Unix(sec, nsec), nil
	}

	for _, layout := range []string{clock.RFC3339Nano, "2006-01-02T15:04:05Z07:00"} {
		if t, err := clock.Parse(layout, s); err == nil {
			return t, nil
		}
	}

	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, err := clock.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return clock.Time{}, fmt.Errorf("unparsed date %q", s)
}

const (
	eModified = "cCxXyY"
	oModified = "deHImMSuUVwWy"
)

type dateLocale struct{}

func reportLocale(rc *toolcompat.RunContext, format string, err error) int {
	if err == nil || !formatNeedsLocale(format) {
		return -1
	}
	_, _ = fmt.Fprintf(rc.Err, "date: %v\n", err)
	return 1
}

const localeDependentSpecifiers = "aAbBhcpPrxX"

func formatNeedsLocale(format string) bool {
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			continue
		}
		c := format[i+1]
		if c == '%' {
			i++
			continue
		}
		if c == 'E' || c == 'O' {
			return true
		}
		if strings.ContainsRune(localeDependentSpecifiers, rune(c)) {
			return true
		}
		i++
	}
	return false
}

func selectDateLocale(env []string) (dateLocale, error) {

	name := locale.Resolve(env, locale.Time)
	name, _, _ = strings.Cut(name, "@")
	base, codeset, _ := strings.Cut(name, ".")
	normalized := strings.ToUpper(strings.NewReplacer("-", "", "_", "").Replace(codeset))
	if base == "C" || base == "POSIX" {
		if normalized == "" || normalized == "UTF8" {
			return dateLocale{}, nil
		}
		return dateLocale{}, fmt.Errorf("LC_TIME locale %q is unavailable", name)
	}
	return dateLocale{}, fmt.Errorf("LC_TIME locale %q is unavailable: 客体只有 C/POSIX 时间名称（没有宿主语言环境数据）", name)
}

func strftime(t clock.Time, f string, loc dateLocale) string {
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		c := f[i]
		if c != '%' || i == len(f)-1 {
			b.WriteByte(c)
			continue
		}
		i++
		if i+1 < len(f) &&
			((f[i] == 'E' && strings.IndexByte(eModified, f[i+1]) >= 0) ||
				(f[i] == 'O' && strings.IndexByte(oModified, f[i+1]) >= 0)) {
			i++
		}
		switch f[i] {
		case 'Y':
			fmt.Fprintf(&b, "%d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'e':
			fmt.Fprintf(&b, "%2d", t.Day())
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&b, "%02d", h)
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'N':
			fmt.Fprintf(&b, "%09d", t.Nanosecond())
		case 's':
			fmt.Fprintf(&b, "%d", t.Unix())
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'A':
			b.WriteString(t.Format("Monday"))
		case 'b', 'h':
			b.WriteString(t.Format("Jan"))
		case 'B':
			b.WriteString(t.Format("January"))
		case 'c':
			b.WriteString(t.Format("Mon Jan _2 15:04:05 2006"))
		case 'C':
			fmt.Fprintf(&b, "%02d", t.Year()/100)
		case 'g':
			y, _ := t.ISOWeek()
			fmt.Fprintf(&b, "%02d", y%100)
		case 'G':
			y, _ := t.ISOWeek()
			fmt.Fprintf(&b, "%04d", y)
		case 'r':
			b.WriteString(t.Format("03:04:05 PM"))
		case 'p':
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
		case 'T':
			fmt.Fprintf(&b, "%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
		case 'R':
			fmt.Fprintf(&b, "%02d:%02d", t.Hour(), t.Minute())
		case 'D':
			fmt.Fprintf(&b, "%02d/%02d/%02d", int(t.Month()), t.Day(), t.Year()%100)
		case 'F':
			fmt.Fprintf(&b, "%d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
		case 'u':

			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			fmt.Fprintf(&b, "%d", wd)
		case 'U':
			fmt.Fprintf(&b, "%02d", weekNumber(t, clock.Sunday))
		case 'V':
			_, w := t.ISOWeek()
			fmt.Fprintf(&b, "%02d", w)
		case 'w':

			fmt.Fprintf(&b, "%d", int(t.Weekday()))
		case 'W':
			fmt.Fprintf(&b, "%02d", weekNumber(t, clock.Monday))
		case 'x':
			b.WriteString(t.Format("01/02/06"))
		case 'X':
			b.WriteString(t.Format("15:04:05"))
		case 'z':
			b.WriteString(t.Format("-0700"))
		case ':':
			if i+1 < len(f) && f[i+1] == 'z' {
				i++
				b.WriteString(t.Format("-07:00"))
			} else {
				b.WriteByte('%')
				b.WriteByte(':')
			}
		case 'Z':
			b.WriteString(t.Format("MST"))
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(f[i])
		}
	}
	return b.String()
}

func weekNumber(t clock.Time, firstDay clock.Weekday) int {
	yd := t.YearDay() - 1
	jan1 := clock.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
	jan1_wd := jan1.Weekday()

	offset := (int(firstDay) - int(jan1_wd) + 7) % 7

	if yd < offset {
		return 0
	}
	return 1 + (yd-offset)/7
}
