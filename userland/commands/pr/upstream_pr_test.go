package prcmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

type firstWriteSignal struct{ ch chan struct{} }

func (w firstWriteSignal) Write(p []byte) (int, error) {
	select {
	case w.ch <- struct{}{}:
	default:
	}
	return len(p), nil
}

func runPR(t *testing.T, dir, stdin string, args ...string) (string, string, int) {
	return runPREnv(t, dir, stdin, nil, args...)
}

func runPREnv(t *testing.T, dir, stdin string, env []string, args ...string) (string, string, int) {
	t.Helper()
	bindDir(t, dir)
	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx:   guestClockContext(t, guestNow),
		Dir:   dir,
		Env:   env,
		Stdio: toolcompat.Stdio{In: strings.NewReader(stdin), Out: &out, Err: &errb},
	}
	code := cmd.Run(rc, args)
	return out.String(), errb.String(), code
}

func writeFixed(t *testing.T, dir, name, content string) time.Time {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	stamp := time.Date(2020, 1, 2, 3, 4, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return stamp
}

func TestPROmitHeaderPassesContent(t *testing.T) {
	out, errb, code := runPR(t, t.TempDir(), "a\nb\n", "-t")
	if out != "a\nb\n" || errb != "" || code != 0 {
		t.Fatalf("pr -t = (%q, %q, %d)", out, errb, code)
	}
}

func TestPRDefaultPageStructure(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "in", "l1\nl2\nl3\n")
	out, errb, code := runPR(t, dir, "", "in")
	if errb != "" || code != 0 {
		t.Fatalf("pr default = (%q, %d)", errb, code)
	}
	header := "Jan  2 03:04 2020 in Page 1"
	want := "\n\n" + header + "\n\n\n" + "l1\nl2\nl3\n" + strings.Repeat("\n", 58)
	if out != want {
		t.Fatalf("pr default page = %q, want %q", out, want)
	}
	if n := strings.Count(out, "\n"); n != 66 {
		t.Fatalf("pr default page has %d lines, want 66", n)
	}
}

func TestPRHeaderNormalizesToUTCAndOnlyCTimeLocale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "in")
	if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2020, time.March, 2, 8, 4, 0, 0, time.UTC)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	for _, env := range [][]string{
		{"TZ=UTC0"},
		{"TZ=EST5"},
		{"TZ=America/New_York"},
		{"TZ=UTC0", "LC_TIME=C"},
		{"LC_TIME=POSIX"},
		{"LC_TIME=C.UTF-8"},
	} {
		out, errb, code := runPREnv(t, dir, "", env, "in")
		if code != 0 || errb != "" || !strings.Contains(out, "Mar  2 08:04 2020 in Page 1") {
			t.Fatalf("env %v 头行 = (%q, %q, %d), want UTC 归一", env, out, errb, code)
		}
	}

	for _, env := range [][]string{
		{"LC_TIME=de_DE.UTF-8"},
		{"TZ=UTC0", "LC_TIME=de_DE.UTF-8"},
		{"LC_TIME=fr_FR.UTF-8"},
		{"LANG=de_DE.UTF-8"},
	} {
		out, errb, code := runPREnv(t, dir, "", env, "in")
		if code != 1 || out != "" || !strings.Contains(errb, "LC_TIME") {
			t.Fatalf("env %v = (%q, %q, %d), want 如实报错", env, out, errb, code)
		}
	}
}

func TestPRStreamsCompletePageBeforeEOF(t *testing.T) {

	reader, writer := io.Pipe()
	wrote := make(chan struct{}, 1)
	done := make(chan int, 1)
	rc := &toolcompat.RunContext{
		Ctx: guestClockContext(t, guestNow), Dir: t.TempDir(),
		Stdio: toolcompat.Stdio{In: reader, Out: firstWriteSignal{ch: wrote}, Err: io.Discard},
	}
	go func() { done <- cmd.Run(rc, nil) }()

	if _, err := io.WriteString(writer, strings.Repeat("x\n", 56)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wrote:
	case <-time.After(time.Second):
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("pr produced no complete page while pipe remained open")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("pr exited %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("pr did not finish after pipe EOF")
	}
}

func TestPRMultiColumnStreamsCompletePageBeforeEOF(t *testing.T) {
	for _, tt := range []struct {
		name  string
		args  []string
		lines int
	}{
		{name: "headerless-vertical", args: []string{"-t", "-l", "3", "-2"}, lines: 6},
		{name: "headerless-across", args: []string{"-t", "-l", "3", "-a", "-2"}, lines: 6},
		{name: "paginated-vertical", args: []string{"-l", "12", "-2"}, lines: 4},
		{name: "paginated-across", args: []string{"-l", "12", "-a", "-2"}, lines: 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			wrote := make(chan struct{}, 1)
			done := make(chan int, 1)
			rc := &toolcompat.RunContext{
				Ctx: guestClockContext(t, guestNow), Dir: t.TempDir(),
				Stdio: toolcompat.Stdio{In: reader, Out: firstWriteSignal{ch: wrote}, Err: io.Discard},
			}
			go func() { done <- cmd.Run(rc, tt.args) }()

			if _, err := io.WriteString(writer, strings.Repeat("x\n", tt.lines)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-wrote:
			case <-time.After(time.Second):
				if err := writer.Close(); err != nil {
					t.Error(err)
				}
				t.Fatal("pr produced no multi-column page while pipe remained open")
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != 0 {
					t.Fatalf("pr exited %d", code)
				}
			case <-time.After(time.Second):
				t.Fatal("pr did not finish after pipe EOF")
			}
		})
	}
}

func TestPRSingleColumnNeverTruncatedByDefault(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "in", "abcdef\n")
	out, _, code := runPR(t, dir, "", "-t", "-w", "3", "in")
	if out != "abcdef\n" || code != 0 {
		t.Fatalf("pr -w must not truncate single-column output = (%q, %d)", out, code)
	}
	out, _, code = runPR(t, dir, "", "-t", "-W", "3", "in")
	if out != "abc\n" || code != 0 {
		t.Fatalf("pr -W truncates = (%q, %d), want abc", out, code)
	}
}

func TestPRShortPageLengthImpliesOmitHeader(t *testing.T) {
	out, _, code := runPR(t, t.TempDir(), "a\nb\nc\n", "-l", "3")
	if out != "a\nb\nc\n" || code != 0 {
		t.Fatalf("pr -l3 (<=10 implies -t) = (%q, %d), want passthrough", out, code)
	}
}

func TestPRNumberIndentAndDoubleSpace(t *testing.T) {
	out, errb, code := runPR(t, t.TempDir(), "a\nb\n", "-t", "-n", "-o", "2", "-d")
	want := "      1\ta\n\n      2\tb\n\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("pr line controls = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestPRCustomHeaderAndTOmitPagination(t *testing.T) {
	out, _, code := runPR(t, t.TempDir(), "a\n", "-h", "TITLE", "-w", "50")
	if code != 0 || !strings.Contains(out, "TITLE") {
		t.Fatalf("pr custom header = (%q, %d), want TITLE", out, code)
	}

	out, _, code = runPR(t, t.TempDir(), "a\nb\nc\n", "-T", "-l", "2")
	if out != "a\nb\nc\n" || code != 0 {
		t.Fatalf("pr -T = (%q, %d), want passthrough", out, code)
	}
}

func TestPRPagesRangeAndDateFormat(t *testing.T) {
	out, _, code := runPR(t, t.TempDir(), "a\nb\nc\nd\n", "--pages", "2", "-l", "13", "-D", "%Y")
	if code != 0 {
		t.Fatalf("pr pages exited %d", code)
	}
	if strings.Contains(out, "Page 1") || !strings.Contains(out, "Page 2") || strings.Contains(out, "a\n") || !strings.Contains(out, "d\n") {
		t.Fatalf("pr pages = %q, want only page 2", out)
	}
	if n := strings.Count(out, "\n"); n != 13 {
		t.Fatalf("pr page 2 has %d lines, want 13", n)
	}
}

func TestPRPlusOperandPageRange(t *testing.T) {
	out, _, code := runPR(t, t.TempDir(), "a\nb\nc\nd\n", "-l", "13", "+2")
	if code != 0 || strings.Contains(out, "Page 1") || !strings.Contains(out, "Page 2") {
		t.Fatalf("pr +2 = (%q, %d), want only page 2", out, code)
	}

	_, errb, code := runPR(t, t.TempDir(), "", "+0")
	if code != 2 || !strings.Contains(errb, "invalid page range") {
		t.Fatalf("pr +0 code=%d err=%q", code, errb)
	}
}

func TestPRPlusPageRangeDoesNotEndOptionRecognition(t *testing.T) {
	input := strings.Repeat("line\n", 20)
	out, errb, code := runPR(t, t.TempDir(), input, "+2", "-l", "13", "-t")
	if code != 0 || errb != "" || out == "" {
		t.Fatalf("pr +2 -l 13 -t = (%q, %q, %d), want page-two output", out, errb, code)
	}
}

func TestPRDoubleDashProtectsPlusOperand(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "+2", "protected\n")
	out, errb, code := runPR(t, dir, "", "-t", "--", "+2")
	if out != "protected\n" || errb != "" || code != 0 {
		t.Fatalf("pr -- +2 = (%q, %q, %d), want protected file content", out, errb, code)
	}
}

func TestPRFormFeedTrailer(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"-F", []string{"-F"}},
		{"-f", []string{"-f"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, code := runPR(t, t.TempDir(), "a\nb\n", tt.args...)
			if code != 0 || strings.Count(out, "\f") != 1 || !strings.HasSuffix(out, "b\n\f") {
				t.Fatalf("pr %s = (%q, %d), want single trailing form feed", tt.name, out, code)
			}
		})
	}
}

func TestPRFormFeedLowerEqualsUpper(t *testing.T) {
	outF, _, codeF := runPR(t, t.TempDir(), "a\nb\n", "-F")
	outf, _, codef := runPR(t, t.TempDir(), "a\nb\n", "-f")
	if codeF != 0 || codef != 0 {
		t.Fatalf("pr -F/-f exit codes: %d, %d", codeF, codef)
	}
	if outF != outf {
		t.Fatalf("pr -f output differs from pr -F:\n-F: %q\n-f: %q", outF, outf)
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestPRMergeAssumesPOSIXTabExpansionAndReplacement(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "one", "a\tb\n")
	writeFixed(t, dir, "two", "x\n")
	out, errb, code := runPR(t, dir, "", "-m", "-t", "-w", "10", "-s|", "one", "two")
	if code != 0 || errb != "" || out != "a    |x\n" {
		t.Fatalf("pr -m implicit -e/-i = (%q, %q, %d), want %q", out, errb, code, "a    |x\n")
	}
}

func TestPRStreamReadAndShortWriteFailuresAreNonzero(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   io.Reader
		out  io.Writer
		want string
	}{
		{"read", failingReader{err: errors.New("input failure")}, io.Discard, "input failure"},
		{"short-write", strings.NewReader("content\n"), shortWriter{}, io.ErrShortWrite.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errb bytes.Buffer
			rc := &toolcompat.RunContext{
				Ctx: guestClockContext(t, guestNow), Dir: t.TempDir(),
				Stdio: toolcompat.Stdio{In: tc.in, Out: tc.out, Err: &errb},
			}
			if code := cmd.Run(rc, []string{"-t"}); code == 0 || !strings.Contains(strings.ToLower(errb.String()), strings.ToLower(tc.want)) {
				t.Fatalf("pr %s failure = code %d, stderr %q; want nonzero and %q", tc.name, code, errb.String(), tc.want)
			}
		})
	}
}

func TestPRInputFormFeedsBreakPages(t *testing.T) {

	out, _, code := runPR(t, t.TempDir(), "a\fb\n", "-t")
	if out != "a\n\fb\n" || code != 0 {
		t.Fatalf("pr -t form feed = (%q, %d), want %q", out, code, "a\n\fb\n")
	}

	out, _, code = runPR(t, t.TempDir(), "a\fb\n", "-T")
	if out != "a\nb\n" || code != 0 {
		t.Fatalf("pr -T form feed = (%q, %d), want %q", out, code, "a\nb\n")
	}

	out, _, code = runPR(t, t.TempDir(), "a\n\fb\n", "-l", "20")
	if code != 0 || !strings.Contains(out, "Page 2") || strings.Contains(out, "Page 3") {
		t.Fatalf("pr paginated form feed = (%q, %d), want 2 pages", out, code)
	}
	if n := strings.Count(out, "\n"); n != 40 {
		t.Fatalf("pr paginated form feed has %d lines, want 40", n)
	}

	out, _, code = runPR(t, t.TempDir(), "a\n\f\fb\n", "-l", "20")
	if code != 0 || !strings.Contains(out, "Page 3") {
		t.Fatalf("pr double form feed = (%q, %d), want 3 pages", out, code)
	}
	if n := strings.Count(out, "\n"); n != 60 {
		t.Fatalf("pr double form feed has %d lines, want 60", n)
	}

	out, _, code = runPR(t, t.TempDir(), "\fb\n", "-l", "20")
	if code != 0 || !strings.Contains(out, "Page 2") || strings.Count(out, "\n") != 40 {
		t.Fatalf("pr leading form feed = (%q, %d), want empty page then data page", out, code)
	}
}

func TestPRVerticalColumns(t *testing.T) {
	input := "a\nbb\nc\ndd\ne\n"
	want := "a    dd\nbb   e\nc\n"
	for _, args := range [][]string{{"-t", "-w", "10", "-2"}, {"-t", "-w", "10", "--columns=2"}, {"-t", "-w", "10", "--column", "2"}} {
		out, errb, code := runPR(t, t.TempDir(), input, args...)
		if out != want || errb != "" || code != 0 {
			t.Errorf("pr %v = (%q, %q, %d), want (%q, \"\", 0)", args, out, errb, code, want)
		}
	}
}

func TestPRVerticalColumnsUnevenFinalPage(t *testing.T) {
	out, errb, code := runPR(t, t.TempDir(), "a\nb\nc\nd\ne\nf\ng\n", "-t", "-l", "3", "-w", "10", "--columns=2")
	want := "a    d\nb    e\nc    f\ng\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("pr vertical uneven final page = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestPRColumnsReserveBlankBetweenFullWidthCells(t *testing.T) {
	input := strings.Repeat("1", 40) + "\n" + strings.Repeat("2", 40) + "\n"
	for _, args := range [][]string{
		{"-t", "-w", "10", "-2"},
		{"-t", "-w", "10", "-2", "-a"},
	} {
		out, errb, code := runPR(t, t.TempDir(), input, args...)
		if code != 0 || errb != "" || !strings.Contains(out, "1111 ") {
			t.Fatalf("pr %v did not reserve a blank column separator: (%q, %q, %d)", args, out, errb, code)
		}
	}
	dir := t.TempDir()
	writeFixed(t, dir, "one", strings.Repeat("1", 40)+"\n")
	writeFixed(t, dir, "two", strings.Repeat("2", 40)+"\n")
	out, errb, code := runPR(t, dir, "", "-m", "-t", "-w", "10", "one", "two")
	if out != "1111 2222\n" || errb != "" || code != 0 {
		t.Fatalf("pr -m did not reserve a blank column separator: (%q, %q, %d)", out, errb, code)
	}
}

func TestPRAcrossColumns(t *testing.T) {
	input := "a\nbb\nc\ndd\ne\n"
	out, errb, code := runPR(t, t.TempDir(), input, "-t", "-w", "10", "-2", "-a")
	if want := "a    bb\nc    dd\ne\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("pr -a = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
	_, errb, code = runPR(t, t.TempDir(), "a\n", "-a", "-m")
	if code != 1 || !strings.Contains(errb, "cannot specify both printing across and printing in parallel") {
		t.Fatalf("pr -am code=%d err=%q", code, errb)
	}
}

func TestPRClusteredColumnWithFlags(t *testing.T) {
	out, errb, code := runPR(t, t.TempDir(), "a\nb\nc\nd\n", "-t", "-w", "10", "-3d")
	if want := "a  c\n\nb  d\n\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("pr -3d = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestPRVerticalColumnsInteractions(t *testing.T) {
	out, errb, code := runPR(t, t.TempDir(), "a\nb\n", "-t", "-w", "10", "-o", "2", "-s:", "-2")
	if want := "  a:b\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("pr vertical separator/margin = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}

	out, errb, code = runPR(t, t.TempDir(), "a\nb\nc\nd\n", "-t", "-w", "10", "-d", "-2")
	if want := "a    c\n\nb    d\n\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("pr vertical double-space = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestPRCustomSeparatorTruncatesColumns(t *testing.T) {
	line := strings.Repeat("x", 20)
	out, errb, code := runPR(t, t.TempDir(), line+"\n"+line+"\n", "-t", "-w", "10", "-sX", "-2", "-a")
	if want := "xxxxXxxxx\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("pr -sX = (%q, %q, %d), want truncated columns %q", out, errb, code, want)
	}
}

func TestPRSeparatorDefaultWidthIs512(t *testing.T) {
	line := strings.Repeat("x", 300)
	out, errb, code := runPR(t, t.TempDir(), line+"\n"+line+"\n", "-t", "-s:", "-2", "-a")
	want := strings.Repeat("x", 255) + ":" + strings.Repeat("x", 255) + "\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("pr -s default width = len %d err %q code %d, want len %d", len(out), errb, code, len(want))
	}
}

func TestPRMerge(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "left", "a\nbb\nc\n")
	writeFixed(t, dir, "right", "1\n")
	out, errb, code := runPR(t, dir, "", "-m", "-t", "-w", "20", "-s:", "left", "right")
	if want := "a:1\nbb:\nc:\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("pr -m = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
	_, errb, code = runPR(t, dir, "", "-m", "-2", "left", "right")
	if code != 1 || !strings.Contains(errb, "cannot specify number of columns") {
		t.Fatalf("pr -m -2 code=%d err=%q", code, errb)
	}
}

func TestPRMergeSeparatorOddWidthDoesNotReservePadding(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "left", "1234567890\n")
	writeFixed(t, dir, "right", "x\n")
	for _, tt := range []struct {
		width string
		want  string
	}{
		{width: "19", want: "123456789:x\n"},
		{width: "21", want: "1234567890:x\n"},
	} {
		out, errb, code := runPR(t, dir, "", "-m", "-t", "-s:", "-w", tt.width, "left", "right")
		if out != tt.want || errb != "" || code != 0 {
			t.Fatalf("pr -m -s -w %s = (%q, %q, %d), want (%q, \"\", 0)", tt.width, out, errb, code, tt.want)
		}
	}
}

func TestPRMergeOpenErrorContinuesAndNewline(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "left", "a\n")
	out, errb, code := runPR(t, dir, "", "-m", "-t", "left", "missing")
	if out != "a\n" || code != 1 {
		t.Fatalf("pr -m missing = (%q, %q, %d), want readable file and exit 1", out, errb, code)
	}
	if !strings.HasSuffix(errb, "\n") || strings.Contains(errb, `\n`) || !strings.Contains(errb, "missing") {
		t.Fatalf("pr -m missing diagnostic malformed: %q", errb)
	}
}

func TestPRBareSeparatorDoesNotConsumeFile(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "in", "from-file\n")
	for _, separator := range []string{"-s", "--separator"} {
		out, errb, code := runPR(t, dir, "from-stdin\n", "-m", "-t", separator, "in")
		if out != "from-file\n" || errb != "" || code != 0 {
			t.Fatalf("pr %s consumed the file operand = (%q, %q, %d)", separator, out, errb, code)
		}
	}
}

func TestPRMergeThreeFilesAndPagination(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "one", "a\nb\nc\nd\n")
	writeFixed(t, dir, "two", "1\n2\n")
	writeFixed(t, dir, "three", "x\ny\nz\n")
	out, errb, code := runPR(t, dir, "", "-m", "-t", "-w", "18", "one", "two", "three")
	if want := "a     1\t    x\nb     2\t    y\nc     \t    z\nd     \t    \n"; out != want || errb != "" || code != 0 {
		t.Fatalf("pr -m three files = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
	out, errb, code = runPR(t, dir, "", "-m", "-l", "13", "-D", "%Y", "one", "two")
	if errb != "" || code != 0 || !strings.Contains(out, "Page 1") || !strings.Contains(out, "Page 2") || strings.Count(out, "\n") != 26 {
		t.Fatalf("paginated pr -m = (%q, %q, %d)", out, errb, code)
	}
}

func TestPRMergeLineNumbering(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "one", "alpha\nbeta\n")
	writeFixed(t, dir, "two", "gamma\n")
	out, errb, code := runPR(t, dir, "", "-m", "-t", "-w", "30", "-nX", "one", "two")
	if code != 0 || errb != "" || !strings.HasPrefix(out, "    1X") || !strings.Contains(out, "\n    2X") {
		t.Fatalf("pr -m -nX = (%q, %q, %d), want one numbered prefix per merged row", out, errb, code)
	}
}

func TestPRClusteredOptionalOutputTabs(t *testing.T) {
	out, errb, code := runPR(t, t.TempDir(), "a\n", "-adFrtiX", "-2")
	if code != 0 || errb != "" || out == "" {
		t.Fatalf("pr clustered -iX = (%q, %q, %d)", out, errb, code)
	}
}

func TestPRRejectsMoreColumnsThanPageWidth(t *testing.T) {
	_, errb, code := runPR(t, t.TempDir(), "a\n", "-500", "-w", "40")
	if code != 2 || !strings.Contains(errb, "page width too narrow") {
		t.Fatalf("pr -500 -w40 = (%q, %d), want width error", errb, code)
	}
}

func TestPRExpandTabs(t *testing.T) {
	out, _, code := runPR(t, t.TempDir(), "a\tb\n", "-t", "-e")
	if want := "a       b\n"; out != want || code != 0 {
		t.Fatalf("pr expand tabs = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestPROptionalExpandArgument(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "in", "a\tbXc\n")
	for _, tt := range []struct {
		name string
		arg  string
		want string
	}{
		{name: "gap", arg: "-e4", want: "a   bXc\n"},
		{name: "char-gap", arg: "-eX4", want: "a\tb c\n"},
		{name: "char-default-gap", arg: "-eX", want: "a\tb     c\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, errb, code := runPR(t, dir, "from-stdin\n", "-t", tt.arg, "in")
			if out != tt.want || errb != "" || code != 0 {
				t.Fatalf("pr %s = (%q, %q, %d), want (%q, \"\", 0)", tt.arg, out, errb, code, tt.want)
			}
		})
	}
}

func TestPROptionalGapZeroMeansDefault(t *testing.T) {
	for _, tt := range []struct {
		arg  string
		want string
	}{
		{arg: "-e0", want: "a       b\n"},
		{arg: "-eX0", want: "a\tb     c\n"},
		{arg: "-i0", want: "a\tb\n"},
		{arg: "-iX0", want: "aXb\n"},
	} {
		input := "a\tb\n"
		if strings.HasPrefix(tt.arg, "-i") {
			input = "a       b\n"
		} else if strings.HasPrefix(tt.arg, "-eX") {
			input = "a\tbXc\n"
		}
		out, errb, code := runPR(t, t.TempDir(), input, "-t", tt.arg)
		if out != tt.want || errb != "" || code != 0 {
			t.Fatalf("pr %s = (%q, %q, %d), want (%q, \"\", 0)", tt.arg, out, errb, code, tt.want)
		}
	}
}

func TestPRMultiColumnImpliesExpandAndOutputTabs(t *testing.T) {
	out, errb, code := runPR(t, t.TempDir(), "a\tb\nc       d\n", "-t", "-2", "-w", "20")
	if want := "a\tb c\t  d\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("pr multicolumn implied tab handling = (%q, %q, %d), want %q", out, errb, code, want)
	}
}

func TestPROptionalNumberArgument(t *testing.T) {
	for _, tt := range []struct {
		name string
		arg  string
		want string
	}{
		{name: "width", arg: "-n4", want: "   1\ta\n"},
		{name: "char-width", arg: "-nX4", want: "   1Xa\n"},
		{name: "char-default-width", arg: "-nX", want: "    1Xa\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, errb, code := runPR(t, t.TempDir(), "a\n", "-t", tt.arg)
			if out != tt.want || errb != "" || code != 0 {
				t.Fatalf("pr %s = (%q, %q, %d), want (%q, \"\", 0)", tt.arg, out, errb, code, tt.want)
			}
		})
	}
}

func TestPRNoFileWarnings(t *testing.T) {
	out, errb, code := runPR(t, t.TempDir(), "", "-r", "missing")
	if out != "" || errb != "" || code != 1 {
		t.Fatalf("pr -r missing = (%q, %q, %d), want quiet exit 1", out, errb, code)
	}
}

func TestPRRejectsBadLength(t *testing.T) {
	_, errb, code := runPR(t, t.TempDir(), "", "-l", "0")
	if code != 2 || !strings.Contains(errb, "invalid page length") {
		t.Fatalf("pr bad length code=%d err=%q", code, errb)
	}
}

func TestPRRejectsBadIndent(t *testing.T) {
	_, errb, code := runPR(t, t.TempDir(), "", "-o", "-1")
	if code != 2 || !strings.Contains(errb, "invalid indent") {
		t.Fatalf("pr bad indent code=%d err=%q", code, errb)
	}
}

func TestPRFirstLineNumber(t *testing.T) {
	out, _, code := runPR(t, t.TempDir(), "a\nb\n", "-t", "-n", "-N", "10")
	want := "   10\ta\n   11\tb\n"
	if out != want || code != 0 {
		t.Fatalf("pr -N 10 = (%q, %d), want (%q, 0)", out, code, want)
	}

	_, errb, code := runPR(t, t.TempDir(), "", "-N", "0", "a")
	if code != 2 || !strings.Contains(errb, "invalid first line number") {
		t.Fatalf("pr -N 0 code=%d err=%q", code, errb)
	}
}

func TestPRNewFlagAliases(t *testing.T) {

	out, errb, code := runPR(t, t.TempDir(), "a\nb\n", "-t", "-w", "10", "--column", "2")
	if out != "a    b\n" || errb != "" || code != 0 {
		t.Fatalf("pr --column 2 = (%q, %q, %d)", out, errb, code)
	}

	out, _, code = runPR(t, t.TempDir(), "a\nb\n", "-J", "-t")
	if out != "a\nb\n" || code != 0 {
		t.Fatalf("pr -J = (%q, %d)", out, code)
	}

	out, _, code = runPR(t, t.TempDir(), "a\n", "-i", "-t")
	if out != "a\n" || code != 0 {
		t.Fatalf("pr -i = (%q, %d)", out, code)
	}
}

func TestPROutputTabsAtAdjacentStop(t *testing.T) {
	o := options{outputTabs: true, outputTabChar: '\t', outputTabWidth: 8}
	if got := tabifyLine("1234567 90\n", o); got != "1234567\t90\n" {
		t.Fatalf("one-space tab-stop replacement = %q", got)
	}
}

func TestPRStopsOptionAndPageParsingAtFirstOperand(t *testing.T) {
	dir := t.TempDir()
	writeFixed(t, dir, "first", "one\n")
	writeFixed(t, dir, "-x", "two\n")
	writeFixed(t, dir, "+2", "three\n")
	writeFixed(t, dir, "--wid", "four\n")
	out, errb, code := runPR(t, dir, "", "-t", "first", "-x", "+2", "--wid")
	if code != 0 || errb != "" || out != "one\ntwo\nthree\nfour\n" {
		t.Fatalf("pr option-looking operands = (%q, %q, %d)", out, errb, code)
	}
}

func TestPRPOSIXLYCorrectDifferentials(t *testing.T) {
	if os.Getenv("COREUTILS_SYSTEM_DIFFERENTIALS") != "1" {
		t.Skip("set COREUTILS_SYSTEM_DIFFERENTIALS=1 to compare with host pr")
	}
	if runtime.GOOS != "linux" {
		t.Skip("GNU POSIXLY_CORRECT differential is only required on Linux/Ubuntu hosts")
	}
	if out, err := exec.Command("/usr/bin/pr", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "GNU coreutils") {
		t.Skip("GNU pr reference not available")
	}
	dir := t.TempDir()
	writeFixed(t, dir, "one", "a\nbb\n")
	writeFixed(t, dir, "two", "1\n")
	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{name: "header", args: []string{"one"}},
		{name: "merge-s", args: []string{"-m", "-t", "-w", "20", "-s:", "one", "two"}},
		{name: "columns", stdin: "a\tb\nc       d\n", args: []string{"-t", "-2", "-w", "20"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCmd := exec.Command("/usr/bin/pr", tc.args...)
			wantCmd.Dir = dir
			wantCmd.Stdin = strings.NewReader(tc.stdin)
			wantCmd.Env = append(os.Environ(), "POSIXLY_CORRECT=1", "LC_ALL=C")
			want, err := wantCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("GNU pr failed: %v output=%q", err, want)
			}
			out, errb, code := runPR(t, dir, tc.stdin, tc.args...)
			if code != 0 || errb != "" || out != string(want) {
				t.Fatalf("candidate differs from GNU POSIXLY_CORRECT:\nout=%q\nerr=%q\ncode=%d\nwant=%q", out, errb, code, want)
			}
		})
	}
}
