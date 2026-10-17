package datecmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	vmclock "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func runTool(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runToolEnv(t, nil, args...)
}

var guestNow = time.Date(2026, 6, 12, 13, 45, 9, 0, time.UTC)

func guestClockContext(t *testing.T, wall time.Time) context.Context {
	t.Helper()
	virtual := &vmclock.Virtual{}
	if err := virtual.SetElapsedTo(uint64(wall.UnixNano())); err != nil {
		t.Fatal(err)
	}
	return proc.WithContext(t.Context(), proc.Proc{ID: 1, Resources: capability.Set{Clock: virtual}})
}

func runToolEnv(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	bindRoot(t)
	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx:   guestClockContext(t, guestNow),
		Dir:   t.TempDir(),
		Env:   env,
		Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: &out, Err: &errb},
	}
	code = cmd.Run(rc, args)
	return out.String(), errb.String(), code
}

func runToolClock(t *testing.T, env []string, now time.Time, setter clockSetter, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	bindRoot(t)
	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{Ctx: guestClockContext(t, now), Dir: t.TempDir(), Env: env, Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: &out, Err: &errb}}
	code = runWithClock(rc, args, func() (clock.Time, error) { return clockFromTime(now), nil }, setter)
	return out.String(), errb.String(), code
}

func TestDateFormats(t *testing.T) {

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-u", "-d", "@0"}, "Thu Jan  1 00:00:00 UTC 1970\n"},
		{[]string{"-u", "-d", "@0", "+%Y-%m-%d %H:%M:%S"}, "1970-01-01 00:00:00\n"},

		{[]string{"-u", "-d", "2026-06-12 13:45:09", "+%a %A %b %B"}, "Fri Friday Jun June\n"},
		{[]string{"-u", "-d", "2026-06-12 13:45:09", "+%y %j %e %T %D %F %R"}, "26 163 12 13:45:09 06/12/26 2026-06-12 13:45\n"},
		{[]string{"-u", "-d", "2026-06-12 13:45:09", "+%I %p %u %w"}, "01 PM 5 5\n"},
		{[]string{"-u", "-d", "2026-06-02", "+[%e]"}, "[ 2]\n"},
		{[]string{"-u", "-d", "@1765432109", "+%s"}, "1765432109\n"},
		{[]string{"-u", "-d", "@0.123456789", "+%N"}, "123456789\n"},
		{[]string{"-u", "-d", "@0", "+%z %Z"}, "+0000 UTC\n"},
		{[]string{"-u", "-d", "@0", "+a%nb%tc%%d"}, "a\nb\tc%d\n"},

		{[]string{"-u", "-d", "2026-06-14", "+%u %w"}, "7 0\n"},

		{[]string{"-u", "-d", "2026-06-12 00:30:00", "+%I %p"}, "12 AM\n"},

		{[]string{"-u", "-d", "@0", "+%c"}, "Thu Jan  1 00:00:00 1970\n"},
		{[]string{"-u", "-d", "@0", "+%C %g %G %r %U %V %W %x %X"}, "19 70 1970 12:00:00 AM 00 01 00 01/01/70 00:00:00\n"},

		{[]string{"-u", "-d", "2023-01-01", "+%U %W %V"}, "01 00 52\n"},
		{[]string{"-u", "-d", "2022-01-01", "+%U %W %V"}, "00 00 52\n"},
		{[]string{"-u", "-d", "2024-01-01", "+%U %W %V"}, "00 01 01\n"},

		{[]string{"-u", "-d", "@0", "+%q"}, "%q\n"},

		{[]string{"-u", "-d", "2026-06-12T10:00:00Z", "+%F %T"}, "2026-06-12 10:00:00\n"},
		{[]string{"-u", "-d", "2026-06-12T10:00:00+02:00", "+%H"}, "08\n"},

		{[]string{"--universal", "-d", "@0", "+%H"}, "00\n"},
	}
	for _, c := range cases {
		out, errb, code := runTool(t, c.args...)
		if out != c.want || code != 0 {
			t.Errorf("date %q = (%q, %q, %d), want (%q, \"\", 0)", c.args, out, errb, code, c.want)
		}
	}
}

func TestDateAlternativeModifiers(t *testing.T) {
	const instant = "2026-06-12 13:45:09"
	cases := []struct {
		modified, plain string
		want            string
	}{
		{"%Ec", "%c", "Fri Jun 12 13:45:09 2026"},
		{"%EC", "%C", "20"},
		{"%Ex", "%x", "06/12/26"},
		{"%EX", "%X", "13:45:09"},
		{"%Ey", "%y", "26"},
		{"%EY", "%Y", "2026"},
		{"%Od", "%d", "12"},
		{"%Oe", "%e", "12"},
		{"%OH", "%H", "13"},
		{"%OI", "%I", "01"},
		{"%Om", "%m", "06"},
		{"%OM", "%M", "45"},
		{"%OS", "%S", "09"},
		{"%Ou", "%u", "5"},
		{"%OU", "%U", "23"},
		{"%OV", "%V", "24"},
		{"%Ow", "%w", "5"},
		{"%OW", "%W", "23"},
		{"%Oy", "%y", "26"},
	}
	for _, c := range cases {
		out, errb, code := runTool(t, "-u", "-d", instant, "+"+c.modified)
		if code != 0 || errb != "" || out != c.want+"\n" {
			t.Errorf("date +%s = (%q, %q, %d), want %q", c.modified, out, errb, code, c.want+"\n")
		}
		plain, _, _ := runTool(t, "-u", "-d", instant, "+"+c.plain)
		if out != plain {
			t.Errorf("date +%s = %q, differs from unmodified +%s = %q", c.modified, out, c.plain, plain)
		}
	}

	for _, c := range []struct{ format, want string }{
		{"%Oz", "%Oz"},
		{"%Ed", "%Ed"},
		{"%EO", "%EO"},
		{"%E", "%E"},
		{"%O", "%O"},
	} {
		out, _, code := runTool(t, "-u", "-d", instant, "+"+c.format)
		if code != 0 || out != c.want+"\n" {
			t.Errorf("date +%s = (%q, %d), want %q", c.format, out, code, c.want+"\n")
		}
	}
}

func TestDateTZ(t *testing.T) {
	cases := []struct {
		tz   string
		args []string
		want string
	}{
		{"UTC", []string{"-d", "@0", "+%H %Z %z"}, "00 UTC +0000\n"},
		{"UTC", []string{"-d", "@0"}, "Thu Jan  1 00:00:00 UTC 1970\n"},
		{"UTC+05:00", []string{"-d", "@0", "+%H %Z %z"}, "05 UTC+05:00 +0500\n"},
		{"+0530", []string{"-d", "@0", "+%H:%M %z"}, "05:30 +0530\n"},
		{"-05:00", []string{"-d", "@0", "+%H %z"}, "19 -0500\n"},

		{"UTC+05:00", []string{"-u", "-d", "@0", "+%H %Z"}, "00 UTC\n"},
	}
	for _, c := range cases {
		out, errb, code := runToolEnv(t, []string{"TZ=" + c.tz}, c.args...)
		if code != 0 || errb != "" || out != c.want {
			t.Errorf("TZ=%q date %q = (%q, %q, %d), want %q", c.tz, c.args, out, errb, code, c.want)
		}
	}
	for _, tz := range []string{"EST5", "America/New_York", "Europe/Berlin", "EST5EDT,M3.2.0,M11.1.0", "bogus", "<+0530>-5:30"} {
		out, errb, code := runToolEnv(t, []string{"TZ=" + tz}, "-d", "@0", "+%H")
		if code == 0 || errb == "" || out != "" {
			t.Errorf("TZ=%q 应如实报错, 得到 (%q, %q, %d)", tz, out, errb, code)
		}
	}
	if loc, err := dateLocation([]string{"TZ="}); err != nil || loc != clock.Local {
		t.Fatalf("null TZ location=(%v,%v), want 客体默认（UTC）", loc, err)
	}
	if loc, err := dateLocation(nil); err != nil || loc != clock.Local {
		t.Fatalf("unset TZ location=(%v,%v), want 客体默认（UTC）", loc, err)
	}
}

func TestDateLCTimePrecedence(t *testing.T) {
	args := []string{"-u", "-d", "2026-03-06", "+%A %B"}
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"default", nil, "Friday March\n"},
		{"c", []string{"LC_TIME=C"}, "Friday March\n"},
		{"posix", []string{"LC_TIME=POSIX"}, "Friday March\n"},
		{"c-utf8", []string{"LC_TIME=C.UTF-8"}, "Friday March\n"},
		{"lc-all-overrides-foreign", []string{"LANG=de_DE.UTF-8", "LC_TIME=de_DE.UTF-8", "LC_ALL=POSIX"}, "Friday March\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errb, code := runToolEnv(t, tc.env, args...)
			if code != 0 || errb != "" || out != tc.want {
				t.Fatalf("date locale env=%q = (%q, %q, %d), want %q", tc.env, out, errb, code, tc.want)
			}
		})
	}

	for _, env := range [][]string{
		{"LANG=de_DE.UTF-8"},
		{"LANG=POSIX", "LC_TIME=de_DE.UTF-8"},
		{"LANG=POSIX", "LC_TIME=POSIX", "LC_ALL=de_DE.UTF-8"},
		{"LANG=POSIX", "LC_TIME=de_DE.iso88591"},
		{"LANG=en_US.UTF-8"},
	} {
		out, errb, code := runToolEnv(t, env, args...)
		if code != 1 || out != "" || !strings.Contains(errb, "LC_TIME") {
			t.Errorf("env=%q 应如实报错, 得到 (%q, %q, %d)", env, out, errb, code)
		}
	}
}

func TestDateLCTimeCompleteFormatsAndUnsupported(t *testing.T) {
	args := []string{"-u", "-d", "2026-03-06 13:45:09", "+%c|%x|%X|%r|%p|%h"}
	out, errOut, code := runToolEnv(t, []string{"LC_TIME=C"}, args...)
	want := "Fri Mar  6 13:45:09 2026|03/06/26|13:45:09|01:45:09 PM|PM|Mar\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("C 语言环境输出=(%q,%q,%d), want %q", out, errOut, code, want)
	}
	out, errOut, code = runToolEnv(t, []string{"LC_TIME=C"}, "-u", "-d", "2026-03-06 13:45:09", "+%Ec|%Ex|%EX|%Od|%OH")
	want = "Fri Mar  6 13:45:09 2026|03/06/26|13:45:09|06|13\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("C 语言环境替代修饰符=(%q,%q,%d), want %q", out, errOut, code, want)
	}
	out, errOut, code = runToolEnv(t, []string{"LC_TIME=de_DE.UTF-8"}, args...)
	if code != 1 || out != "" || !strings.Contains(errOut, "LC_TIME") {
		t.Fatalf("de_DE 应如实报错, 得到 (%q,%q,%d)", out, errOut, code)
	}

	out, errOut, code = runToolEnv(t, []string{"LC_TIME=de_DE.UTF-8"}, "-u", "-d", "2026-03-06", "+%F")
	if code != 0 || errOut != "" || out != "2026-03-06\n" {
		t.Fatalf("无语言环境依赖的格式应照常输出, 得到 (%q,%q,%d)", out, errOut, code)
	}

	called := false
	_, errOut, code = runToolClock(t, []string{"LC_TIME=fr_FR.UTF-8"}, time.Unix(0, 0), func(clock.Time) error { called = true; return nil }, "01010000")
	if code != 1 || called || !strings.Contains(errOut, "LC_TIME") {
		t.Fatalf("unsupported locale=(code %d, setter called %v, stderr %q)", code, called, errOut)
	}
}

func TestDateFormatAliases(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-u", "-d", "@0", "--iso-8601"}, "1970-01-01\n"},
		{[]string{"-u", "-d", "@0", "--iso-8601=seconds"}, "1970-01-01T00:00:00+0000\n"},
		{[]string{"-u", "-d", "@0", "--rfc-3339=seconds"}, "1970-01-01 00:00:00+00:00\n"},
		{[]string{"-u", "-d", "@0", "--rfc-email"}, "Thu, 01 Jan 1970 00:00:00 +0000\n"},
		{[]string{"-u", "-d", "@0", "--rfc-822"}, "Thu, 01 Jan 1970 00:00:00 +0000\n"},
		{[]string{"-u", "-d", "@0", "--rfc-2822"}, "Thu, 01 Jan 1970 00:00:00 +0000\n"},
		{[]string{"--uct", "-d", "@0", "+%H %Z"}, "00 UTC\n"},
	}
	for _, c := range cases {
		out, errb, code := runTool(t, c.args...)
		if code != 0 || errb != "" || out != c.want {
			t.Fatalf("date %q = (%q, %q, %d), want %q", c.args, out, errb, code, c.want)
		}
	}
}

func TestDateFileDebugAndResolution(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "dates")
	if err := os.WriteFile(file, []byte("@0\n1970-01-02 03:04:05\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runTool(t, "-u", "--debug", "--file", file, "+%F %T")
	if code != 0 || !strings.Contains(errb, "parsed date") {
		t.Fatalf("--file debug code=%d err=%q", code, errb)
	}
	if want := "1970-01-01 00:00:00\n1970-01-02 03:04:05\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}

	out, errb, code = runTool(t, "--resolution")
	if code != 0 || errb != "" || out != "0.000000001\n" {
		t.Fatalf("--resolution = (%q, %q, %d)", out, errb, code)
	}
}

func TestDateDefaultShape(t *testing.T) {
	out, _, code := runTool(t)
	if code != 0 {
		t.Fatalf("date: code=%d", code)
	}

	fields := strings.Fields(strings.TrimSuffix(out, "\n"))
	if len(fields) < 5 || !strings.Contains(out, ":") {
		t.Errorf("default output %q does not match C-locale date shape", out)
	}
}

func TestDateReference(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "stamp")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1700000000, 0)
	if err := os.Chtimes(f, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	bindDir(t, dir)
	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx:   guestClockContext(t, guestNow),
		Dir:   dir,
		Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: &out, Err: &errb},
	}

	code := cmd.Run(rc, []string{"-u", "-r", "stamp", "+%s"})
	if code != 0 || out.String() != "1700000000\n" {
		t.Errorf("-r: code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}

func TestDateXSISetDateOperandRefused(t *testing.T) {

	for _, args := range [][]string{
		{"not-a-date"}, {"13010000"}, {"023000002024"}, {"01012400"}, {"01010060"},
		{"01010000", "+%Y"}, {"-d", "@0", "01010000"}, {"-I", "01010000"}, {"--debug", "01010000"},
	} {
		out, errOut, code := runToolEnv(t, []string{"LC_ALL=C"}, args...)
		if code == 0 || out != "" || errOut == "" {
			t.Errorf("date %q=(out %q, err %q, code %d), want pre-mutation failure", args, out, errOut, code)
		}
	}

	out, errOut, code := runToolClock(t, []string{"LC_ALL=C"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), setSystemClock, "01010000")
	if code != 1 || out != "" || !strings.Contains(errOut, "cannot set date") {
		t.Fatalf("set-date=(%q,%q,%d), want 客体拒绝并给出诊断", out, errOut, code)
	}
	if !strings.Contains(errOut, "不允许设置系统时钟") {
		t.Fatalf("诊断应说明客体没有改时钟的端口: %q", errOut)
	}

	wantErr := os.ErrPermission
	out, errOut, code = runToolClock(t, []string{"LC_ALL=C"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), func(clock.Time) error { return wantErr }, "01010000")
	if code != 1 || out != "" || !strings.Contains(errOut, "cannot set date") || !strings.Contains(errOut, wantErr.Error()) {
		t.Fatalf("setter failure=(%q,%q,%d)", out, errOut, code)
	}
}

func TestDateXSIYearDefaultRejectsMissingZoneData(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)
	var called bool
	_, errOut, code := runToolClock(t, []string{"TZ=EST5", "LC_ALL=C"}, now, func(clock.Time) error { called = true; return nil }, "01010000")
	if code != 1 || called || !strings.Contains(errOut, "TZ=") {
		t.Fatalf("TZ=EST5 设置日期=(code %d, called %v, stderr %q), want 时区数据库缺失诊断", code, called, errOut)
	}

	var target clock.Time
	_, errOut, code = runToolClock(t, []string{"TZ=EST5", "LC_ALL=C"}, now, func(at clock.Time) error { target = at; return nil }, "-u", "01010000")
	if code != 0 || errOut != "" || target.Year() != 2026 {
		t.Fatalf("-u 设置日期=(code %d, stderr %q, year %d), want 2026", code, errOut, target.Year())
	}
}

func TestDateErrors(t *testing.T) {
	_, errb, code := runTool(t, "-d", "next fortnight")
	if code != 1 || !strings.Contains(errb, "invalid date") {
		t.Errorf("invalid -d: code=%d err=%q", code, errb)
	}
	_, errb, code = runTool(t, "-r", "no-such-file")
	if code != 1 || !strings.Contains(errb, "no-such-file") {
		t.Errorf("missing -r file: code=%d err=%q", code, errb)
	}
	_, errb, code = runTool(t, "-d", "@0", "-r", "x")
	if code != 2 || !strings.Contains(errb, "mutually exclusive") {
		t.Errorf("-d with -r: code=%d err=%q", code, errb)
	}
	_, errb, code = runTool(t, "--set", "@0")
	if code != 2 || !strings.Contains(errb, "not supported") {
		t.Errorf("--set: code=%d err=%q", code, errb)
	}
	_, errb, code = runTool(t, "+%Y", "+%m")
	if code != 2 || !strings.Contains(errb, "extra operand") {
		t.Errorf("two formats: code=%d err=%q", code, errb)
	}
	_, errb, code = runTool(t, "--frobnicate")
	if code != 2 || !strings.Contains(errb, "frobnicate") {
		t.Errorf("unknown flag: code=%d err=%q", code, errb)
	}
}

func TestDateInvalidUsageDiagnostics(t *testing.T) {
	cases := [][]string{
		{"-Q"},
		{"--frobnicate"},
		{"--iso-8601=bogus"},
		{"--rfc-3339=bogus"},
		{"--rfc-3339"},
		{"-d", "not a date"},
		{"-f", "no-such-file"},
		{"-r", "no-such-file"},
		{"+%Y", "+%m"},
		{"13311030"},
		{"--set", "@0"},
		{"-d", "@0", "-r", "x"},
		{"--resolution", "+%Y"},
		{"-I", "--rfc-email"},
		{"-d", "@0", "-d", "@1", "@2"},
	}
	for _, args := range cases {
		out, errb, code := runTool(t, args...)
		if code == 0 {
			t.Errorf("date %q: exit 0, want nonzero (out=%q err=%q)", args, out, errb)
		}
		if errb == "" {
			t.Errorf("date %q: no diagnostic on stderr (out=%q code=%d)", args, out, code)
		}
	}
}

func TestDateWriteErrorDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  io.Writer
		want string
	}{
		{"error", failingWriter{err: os.ErrClosed}, "date: write error:"},
		{"short", shortWriter{}, io.ErrShortWrite.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errb bytes.Buffer
			bindRoot(t)
			rc := &toolcompat.RunContext{
				Ctx: guestClockContext(t, guestNow),
				Dir: t.TempDir(),
				Stdio: toolcompat.Stdio{
					In:  strings.NewReader(""),
					Out: tc.out,
					Err: &errb,
				},
			}
			if code := cmd.Run(rc, []string{"-u", "-d", "@0", "+%s"}); code != 1 {
				t.Fatalf("write failure code=%d, want 1", code)
			}
			if got := errb.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("write failure stderr=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestDateHelp(t *testing.T) {
	out, _, code := runTool(t, "--help")
	if code != 0 || !strings.Contains(out, "Usage: date") {
		t.Errorf("--help: code=%d out=%q", code, out)
	}
	for _, hidden := range []string{"rfc-822", "rfc-2822", "uct"} {
		if strings.Contains(out, hidden) {
			t.Errorf("--help contains hidden alias %q in %q", hidden, out)
		}
	}
}

func clockFromTime(t time.Time) clock.Time {
	return clock.Unix(t.Unix(), int64(t.Nanosecond())).In(clock.UTC)
}
