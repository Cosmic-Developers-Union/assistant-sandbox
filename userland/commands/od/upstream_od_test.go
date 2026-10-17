package odcmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

func runOD(t *testing.T, dir, stdin string, args ...string) (string, string, int) {
	return runODProfile(t, runtimeProfile(), dir, stdin, args...)
}

func runODProfile(t *testing.T, profile platformProfile, dir, stdin string, args ...string) (string, string, int) {
	return runODProfileEnv(t, profile, nil, dir, stdin, args...)
}

func runODProfileEnv(t *testing.T, profile platformProfile, env []string, dir, stdin string, args ...string) (string, string, int) {
	t.Helper()
	bindDir(t, dir)
	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx:   context.Background(),
		Dir:   dir,
		Env:   env,
		Stdio: toolcompat.Stdio{In: strings.NewReader(stdin), Out: &out, Err: &errb},
	}
	code := runWithProfile(rc, args, profile)
	return out.String(), errb.String(), code
}

func TestODCTypeLocaleRenderingAndPrecedence(t *testing.T) {
	profile := runtimeProfile()
	cases := []struct {
		name  string
		env   []string
		input string
		want  string
	}{
		{name: "default-posix", input: "\xc3\xa4", want: " 303 244\n"},
		{name: "c", env: []string{"LC_CTYPE=C"}, input: "\xc3\xa4", want: " 303 244\n"},
		{name: "posix", env: []string{"LC_CTYPE=POSIX"}, input: "\xc3\xa4", want: " 303 244\n"},
		{name: "c-utf8", env: []string{"LC_CTYPE=C.UTF-8"}, input: "\xc3\xa4", want: "   ä  **\n"},
		{name: "posix-utf8-alias", env: []string{"LC_CTYPE=POSIX.utf8"}, input: "\xc3\xa4", want: "   ä  **\n"},
		{name: "german-utf8", env: []string{"LC_CTYPE=de_DE.UTF-8"}, input: "\xc3\xa4", want: "   ä  **\n"},
		{name: "german-latin1", env: []string{"LC_CTYPE=de_DE.ISO-8859-1"}, input: "\xe4", want: "   \xe4\n"},
		{name: "lc-all-wins", env: []string{"LANG=de_DE.UTF-8", "LC_CTYPE=de_DE.UTF-8", "LC_ALL=C"}, input: "\xc3\xa4", want: " 303 244\n"},
		{name: "ctype-wins-lang", env: []string{"LANG=C", "LC_CTYPE=de_DE.UTF-8"}, input: "\xc3\xa4", want: "   ä  **\n"},
		{name: "empty-lc-all-falls-through", env: []string{"LANG=C", "LC_CTYPE=de_DE.UTF-8", "LC_ALL="}, input: "\xc3\xa4", want: "   ä  **\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, errb, code := runODProfileEnv(t, profile, tc.env, guestDir(t), tc.input, "-A", "n", "-t", "c")
			if out != tc.want || errb != "" || code != 0 {
				t.Fatalf("od locale -t c = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, tc.want)
			}
		})
	}
}

func TestODCTypeUTF8ContinuationAcrossOutputGroups(t *testing.T) {
	out, errb, code := runODProfileEnv(t, runtimeProfile(), []string{"LC_CTYPE=C.UTF-8"}, guestDir(t), "XäY", "-A", "n", "-t", "c", "-w", "2")
	if want := "   X   ä\n  **   Y\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od split UTF-8 character = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}

	for _, tc := range []struct {
		name, input, want string
	}{
		{name: "non-printable", input: "\xc2\x85", want: " 302 205\n"},
		{name: "malformed", input: "\xc3X", want: " 303   X\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errb, code := runODProfileEnv(t, runtimeProfile(), []string{"LC_CTYPE=C.UTF-8"}, guestDir(t), tc.input, "-A", "n", "-t", "c")
			if out != tc.want || errb != "" || code != 0 {
				t.Fatalf("od %s UTF-8 = (%q, %q, %d), want (%q, empty, 0)", tc.name, out, errb, code, tc.want)
			}
		})
	}
}

func TestODCTypeRequiredEscapesAndOctalFields(t *testing.T) {
	input := []byte{0, '\\', '\a', '\b', '\f', '\n', '\r', '\t', '\v', 0x1b}
	fields, continuation := renderCFields(input, nil, ctypeModel{encoding: ctypeASCII}, 0)
	want := []string{"\\0", "\\", "\\a", "\\b", "\\f", "\\n", "\\r", "\\t", "\\v", "033"}
	if !equalStrings(fields, want) || continuation != 0 {
		t.Fatalf("C/POSIX -t c fields = (%q, %d), want (%q, 0)", fields, continuation, want)
	}

	fields, continuation = renderCFields([]byte{0x9f, 0xa0, 0xe4}, nil, ctypeModel{encoding: ctypeLatin1}, 0)
	want = []string{"237", string([]byte{0xa0}), string([]byte{0xe4})}
	if !equalStrings(fields, want) || continuation != 0 {
		t.Fatalf("ISO-8859-1 -t c fields = (%q, %d), want (%q, 0)", fields, continuation, want)
	}
}

type channelODWriter struct{ writes chan []byte }

func (w channelODWriter) Write(p []byte) (int, error) {
	copyOfP := append([]byte(nil), p...)
	w.writes <- copyOfP
	return len(p), nil
}

func TestODUTF8FullASCIIGroupDoesNotWaitForLookahead(t *testing.T) {
	reader, writer := io.Pipe()
	writes := make(chan []byte, 32)
	done := make(chan error, 1)
	o := options{
		addrRadix: "n",
		formats:   []dumpFormat{{kind: "c", size: 1}},
		endian:    binary.LittleEndian,
		limit:     -1,
		width:     4,
		ctype:     ctypeModel{encoding: ctypeUTF8},
		radix:     '.',
	}
	go func() {

		out := bufio.NewWriterSize(channelODWriter{writes: writes}, 1)
		err := dump(reader, out, o)
		_ = out.Flush()
		done <- err
	}()
	if _, err := writer.Write([]byte("ABCD")); err != nil {
		t.Fatal(err)
	}

	var got []byte
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for len(got) < len("   A   B   C   D") {
		select {
		case p := <-writes:
			got = append(got, p...)
		case err := <-done:
			t.Fatalf("dump returned before rendering the full group: %v", err)
		case <-timer.C:
			_ = writer.Close()
			t.Fatal("od waited for bytes beyond a complete ASCII output group")
		}
	}
	if string(got) != "   A   B   C   D" {
		t.Fatalf("first streamed group = %q, want ASCII group", got)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("dump after pipe EOF: %v", err)
	}
}

func TestODUTF8BoundaryLookaheadIsExact(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block []byte
		want  int
	}{
		{name: "empty", block: nil, want: 0},
		{name: "ascii", block: []byte("ABCD"), want: 0},
		{name: "complete-two-byte", block: []byte("Aä"), want: 0},
		{name: "two-byte-lead", block: []byte{'A', 0xc3}, want: 1},
		{name: "three-byte-lead", block: []byte{'A', 0xe2}, want: 2},
		{name: "three-byte-prefix", block: []byte{'A', 0xe2, 0x82}, want: 1},
		{name: "four-byte-lead", block: []byte{'A', 0xf0}, want: 3},
		{name: "stray-continuation", block: []byte{'A', 0x80}, want: 0},
		{name: "invalid-lead", block: []byte{'A', 0xff}, want: 0},
		{name: "invalid-prefix", block: []byte{'A', 0xe0, 0x80}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingUTF8SuffixBytes(tc.block); got != tc.want {
				t.Fatalf("missingUTF8SuffixBytes(% x) = %d, want %d", tc.block, got, tc.want)
			}
		})
	}
}

func TestODLocaleCategoriesFailClosedOnlyWhenUsed(t *testing.T) {
	profile := runtimeProfile()
	opened := false
	profile.openInput = func(string) (io.ReadCloser, error) {
		opened = true
		return io.NopCloser(strings.NewReader("data")), nil
	}

	_, errb, code := runODProfileEnv(t, profile, []string{"LC_CTYPE=fr_FR.UTF-8"}, guestDir(t), "", "-A", "n", "-t", "c", "input")
	if code != 1 || !strings.Contains(errb, `LC_CTYPE "fr_FR.UTF-8" is unavailable`) || opened {
		t.Fatalf("unsupported LC_CTYPE = (stderr %q, code %d, opened %v), want fail before open", errb, code, opened)
	}

	opened = false
	_, errb, code = runODProfileEnv(t, profile, []string{"LC_NUMERIC=fr_FR.UTF-8"}, guestDir(t), "", "-A", "n", "-t", "fD", "input")
	if code != 1 || !strings.Contains(errb, `LC_NUMERIC "fr_FR.UTF-8" is unavailable`) || opened {
		t.Fatalf("unsupported LC_NUMERIC = (stderr %q, code %d, opened %v), want fail before open", errb, code, opened)
	}

	out, errb, code := runODProfileEnv(t, runtimeProfile(), []string{"LC_CTYPE=fr_FR.UTF-8", "LC_NUMERIC=fr_FR.UTF-8"}, guestDir(t), "A", "-A", "n", "-t", "x1")
	if out != " 41\n" || errb != "" || code != 0 {
		t.Fatalf("irrelevant locale categories = (%q, %q, %d), want hex success", out, errb, code)
	}
}

func TestODNumericLocaleAllFloatingTypesAndABIs(t *testing.T) {
	float32OneAndHalf := []byte{0, 0, 0xc0, 0x3f}
	float64OneAndHalf := []byte{0, 0, 0, 0, 0, 0, 0xf8, 0x3f}
	x87OneAndHalf16 := []byte{0, 0, 0, 0, 0, 0, 0, 0xc0, 0xff, 0x3f, 0, 0, 0, 0, 0, 0}
	x87OneAndHalf12 := x87OneAndHalf16[:12]
	ieee128OneAndHalf := make([]byte, 16)
	binary.LittleEndian.PutUint64(ieee128OneAndHalf[8:], uint64(0x3fff)<<48|uint64(1)<<47)
	ibmOneAndHalf := make([]byte, 16)
	binary.BigEndian.PutUint64(ibmOneAndHalf[:8], math.Float64bits(1.5))

	tests := []struct {
		name    string
		profile platformProfile
		format  string
		data    []byte
	}{
		{name: "fF", profile: platformProfile{abi: abiFor("linux", "amd64"), endian: binary.LittleEndian}, format: "fF", data: float32OneAndHalf},
		{name: "f4", profile: platformProfile{abi: abiFor("linux", "amd64"), endian: binary.LittleEndian}, format: "f4", data: float32OneAndHalf},
		{name: "fD", profile: platformProfile{abi: abiFor("linux", "amd64"), endian: binary.LittleEndian}, format: "fD", data: float64OneAndHalf},
		{name: "f8", profile: platformProfile{abi: abiFor("linux", "amd64"), endian: binary.LittleEndian}, format: "f8", data: float64OneAndHalf},
		{name: "bare-f", profile: platformProfile{abi: abiFor("linux", "amd64"), endian: binary.LittleEndian}, format: "f", data: float64OneAndHalf},
		{name: "x87-16", profile: platformProfile{abi: abiFor("linux", "amd64"), endian: binary.LittleEndian}, format: "fL", data: x87OneAndHalf16},
		{name: "x87-explicit-16", profile: platformProfile{abi: abiFor("linux", "amd64"), endian: binary.LittleEndian}, format: "f16", data: x87OneAndHalf16},
		{name: "x87-12", profile: platformProfile{abi: abiFor("linux", "386"), endian: binary.LittleEndian}, format: "fL", data: x87OneAndHalf12},
		{name: "x87-explicit-12", profile: platformProfile{abi: abiFor("linux", "386"), endian: binary.LittleEndian}, format: "f12", data: x87OneAndHalf12},
		{name: "ieee64-long-double", profile: platformProfile{abi: abiFor("windows", "amd64"), endian: binary.LittleEndian}, format: "fL", data: float64OneAndHalf},
		{name: "ieee128", profile: platformProfile{abi: abiFor("linux", "arm64"), endian: binary.LittleEndian}, format: "fL", data: ieee128OneAndHalf},
		{name: "ieee128-explicit", profile: platformProfile{abi: abiFor("linux", "arm64"), endian: binary.LittleEndian}, format: "f16", data: ieee128OneAndHalf},
		{name: "ibm-double-double", profile: platformProfile{abi: abiFor("linux", "ppc64"), endian: binary.BigEndian}, format: "fL", data: ibmOneAndHalf},
		{name: "ibm-double-double-explicit", profile: platformProfile{abi: abiFor("linux", "ppc64"), endian: binary.BigEndian}, format: "f16", data: ibmOneAndHalf},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.profile.openInput = runtimeProfile().openInput
			out, errb, code := runODProfileEnv(t, tc.profile, []string{"LC_NUMERIC=de_DE.UTF-8"}, guestDir(t), string(tc.data), "-A", "n", "-t", tc.format)
			if out != " 1,5\n" || errb != "" || code != 0 {
				t.Fatalf("od %s German radix = (%q, %q, %d), want (1,5, empty, 0)", tc.format, out, errb, code)
			}
		})
	}
}

func TestODNumericLocalePrecedenceAndScientificRadix(t *testing.T) {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, math.Float64bits(1.25e20))
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{name: "numeric", env: []string{"LC_NUMERIC=de_DE.ISO-8859-1"}, want: " 1,25e+20\n"},
		{name: "lang", env: []string{"LANG=de_DE.UTF-8"}, want: " 1,25e+20\n"},
		{name: "lc-all-comma", env: []string{"LANG=C", "LC_NUMERIC=C", "LC_ALL=de_DE.UTF-8"}, want: " 1,25e+20\n"},
		{name: "lc-all-posix", env: []string{"LANG=de_DE.UTF-8", "LC_NUMERIC=de_DE.UTF-8", "LC_ALL=POSIX"}, want: " 1.25e+20\n"},
		{name: "empty-lc-all", env: []string{"LANG=C", "LC_NUMERIC=de_DE.UTF-8", "LC_ALL="}, want: " 1,25e+20\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := runtimeProfile()
			profile.abi, profile.endian = abiFor("linux", "amd64"), binary.LittleEndian
			out, errb, code := runODProfileEnv(t, profile, tc.env, guestDir(t), string(data), "-A", "n", "-t", "fD")
			if out != tc.want || errb != "" || code != 0 {
				t.Fatalf("od numeric precedence = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, tc.want)
			}
		})
	}
}

func TestODDefaultOctalWords(t *testing.T) {
	out, errb, code := runOD(t, guestDir(t), "ABCD")
	want := "0000000 041101 042103\n0000004\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("od default = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODFormatsAndOffsets(t *testing.T) {

	out, _, code := runOD(t, guestDir(t), "abc\n", "-A", "x", "-t", "x1", "-N", "3")
	if want := "000000 61 62 63\n000003\n"; out != want || code != 0 {
		t.Fatalf("od x1 = (%q, %d), want (%q, 0)", out, code, want)
	}
	out, _, code = runOD(t, guestDir(t), "a\n", "-A", "n", "-t", "c")
	if want := "   a  \\n\n"; out != want || code != 0 {
		t.Fatalf("od c no addresses = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODPOSIXNoAddressStartsWithFirstItem(t *testing.T) {
	for _, value := range []string{"1", ""} {
		for _, args := range [][]string{{"-A", "n", "-o"}, {"-An", "-o"}} {
			out, errb, code := runODProfileEnv(t, runtimeProfile(), []string{"POSIXLY_CORRECT=" + value}, guestDir(t), "12345678912345678912345678912\n", args...)
			want := "031061 032063 033065 034067 030471 031462 032464 033466\n034470 031061 032063 033065 034067 030471 005062\n\n"
			if out != want || errb != "" || code != 0 {
				t.Errorf("POSIX od %v = (%q, %q, %d), want (%q, empty, 0)", args, out, errb, code, want)
			}
		}
	}
}

func TestODPOSIXFinalRecord(t *testing.T) {
	cases := []struct {
		name, input string
		args        []string
		posix, gnu  string
	}{
		{"empty", "", []string{"-An", "-tx1"}, "\n", ""},
		{"bytes", "AB", []string{"-An", "-tx1"}, "41 42\n\n", " 41 42\n"},
		{"count", "ABC", []string{"-An", "-tx1", "-N1"}, "41\n\n", " 41\n"},
		{"skip", "ABC", []string{"-An", "-tx1", "-j1", "-N1"}, "42\n\n", " 42\n"},
		{"multiple-formats", "A", []string{"-An", "-tx1", "-tu1"}, "41\n 65\n\n", " 41\n  65\n"},
		{"address", "AB", []string{"-Ad", "-tx1"}, "0000000 41 42\n0000002\n", "0000000 41 42\n0000002\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, posix := range []bool{false, true} {
				var env []string
				want := tc.gnu
				if posix {
					env = []string{"POSIXLY_CORRECT=1"}
					want = tc.posix
				}
				out, errb, code := runODProfileEnv(t, runtimeProfile(), env, guestDir(t), tc.input, tc.args...)
				if out != want || errb != "" || code != 0 {
					t.Errorf("POSIX=%v: got (%q, %q, %d), want (%q, empty, 0)", posix, out, errb, code, want)
				}
			}
		})
	}
}

func TestODTypeAliases(t *testing.T) {
	out, _, code := runOD(t, guestDir(t), "AB", "-A", "n", "-t", "xC")
	if want := " 41 42\n"; out != want || code != 0 {
		t.Fatalf("od -t xC = (%q, %d), want (%q, 0)", out, code, want)
	}
	out, _, code = runOD(t, guestDir(t), "AB", "-A", "n", "-t", "xS")
	if want := " 4241\n"; out != want || code != 0 {
		t.Fatalf("od -t xS = (%q, %d), want (%q, 0)", out, code, want)
	}
	out, _, code = runOD(t, guestDir(t), "a\n", "-A", "n", "-t", "char")
	if want := "   a  \\n\n"; out != want || code != 0 {
		t.Fatalf("od -t char = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODShortAliasesAndWidth(t *testing.T) {
	out, _, code := runOD(t, guestDir(t), "abcd", "-b", "-w", "2")
	if want := "0000000 141 142\n0000002 143 144\n0000004\n"; out != want || code != 0 {
		t.Fatalf("od -b -w = (%q, %d), want (%q, 0)", out, code, want)
	}

	out, _, code = runOD(t, guestDir(t), "AB", "-x")
	if want := "0000000 4241\n0000002\n"; out != want || code != 0 {
		t.Fatalf("od -x = (%q, %d), want (%q, 0)", out, code, want)
	}

	out, _, code = runOD(t, guestDir(t), "AB", "-d")
	if want := "0000000 16961\n0000002\n"; out != want || code != 0 {
		t.Fatalf("od -d = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODSkipAndFiles(t *testing.T) {
	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := runOD(t, dir, "", "-t", "o1", "-j", "2", "in")
	if want := "0000002 143 144\n0000004\n"; out != want || code != 0 {
		t.Fatalf("od skip file = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODMultiFormatEndianStringsAndTraditionalSkip(t *testing.T) {
	out, _, code := runOD(t, guestDir(t), "ABCD", "-A", "x", "-t", "x2", "-t", "u1", "--endian", "big", "-w", "4")
	want := "000000 4142 4344\n        65  66  67  68\n000004\n"
	if out != want || code != 0 {
		t.Fatalf("od multi/endian = (%q, %d), want (%q, 0)", out, code, want)
	}

	out, _, code = runOD(t, guestDir(t), "\x00abc\x00de\x00", "-A", "d", "-S", "3")
	want = "0000001 abc\n"
	if out != want || code != 0 {
		t.Fatalf("od strings = (%q, %d), want (%q, 0)", out, code, want)
	}

	out, _, code = runOD(t, guestDir(t), "abcd", "+2")
	want = "0000002 062143\n0000004\n"
	if out != want || code != 0 {
		t.Fatalf("od traditional skip = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODRejectsBadFormat(t *testing.T) {
	_, errb, code := runOD(t, guestDir(t), "", "-t", "x4")
	if code != 0 || errb != "" {
		t.Fatalf("od x4 should now be supported code=%d err=%q", code, errb)
	}

	_, errb, code = runOD(t, guestDir(t), "", "-t", "z9")
	if code != 2 || !strings.Contains(errb, "unsupported output format") {
		t.Fatalf("od bad format code=%d err=%q", code, errb)
	}
}

func TestODRejectsBadWidth(t *testing.T) {
	_, errb, code := runOD(t, guestDir(t), "", "-w", "0")
	if code != 2 || !strings.Contains(errb, "invalid output width") {
		t.Fatalf("od bad width code=%d err=%q", code, errb)
	}
}

func TestODNamedCharsVsC(t *testing.T) {
	out, _, code := runOD(t, guestDir(t), "a b\n", "-a", "-A", "n")
	if want := "   a  sp   b  nl\n"; out != want || code != 0 {
		t.Fatalf("od -a = (%q, %d), want (%q, 0)", out, code, want)
	}
	out, _, code = runOD(t, guestDir(t), "a b\n\a\v", "-t", "c", "-A", "n")
	if want := "   a       b  \\n  \\a  \\v\n"; out != want || code != 0 {
		t.Fatalf("od -t c = (%q, %d), want (%q, 0)", out, code, want)
	}
	out, _, code = runOD(t, guestDir(t), "\xe1", "-a", "-A", "n")
	if want := "   a\n"; out != want || code != 0 {
		t.Fatalf("od -a high bit = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODDuplicateSuppression(t *testing.T) {
	data := strings.Repeat("\x00", 48)
	out, _, code := runOD(t, guestDir(t), data)
	want := "0000000 000000 000000 000000 000000 000000 000000 000000 000000\n*\n0000060\n"
	if out != want || code != 0 {
		t.Fatalf("od dup = (%q, %d), want (%q, 0)", out, code, want)
	}
	out, _, code = runOD(t, guestDir(t), data, "-v")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 4 || strings.Contains(out, "*") || code != 0 {
		t.Fatalf("od -v = (%q, %d), want 3 data lines + final offset and no *", out, code)
	}
}

func TestODTraditionalOffsetRadix(t *testing.T) {
	data := strings.Repeat("x", 20)
	out, _, code := runOD(t, guestDir(t), data, "-b", "+20")
	if code != 0 || !strings.HasPrefix(out, "0000020 170 170 170 170\n") {
		t.Fatalf("od +20 (octal) = (%q, %d)", out, code)
	}
	out, _, code = runOD(t, guestDir(t), data, "-b", "+16.")
	if code != 0 || !strings.HasPrefix(out, "0000020 170 170 170 170\n") {
		t.Fatalf("od +16. (decimal) = (%q, %d)", out, code)
	}

	out, _, code = runOD(t, guestDir(t), data, "--traditional", "-t", "o1", "+20")
	if code != 0 || !strings.HasPrefix(out, "0000020 170 170 170 170\n") {
		t.Fatalf("od --traditional -t o1 +20 = (%q, %d)", out, code)
	}
}

func TestODSkipPastEOF(t *testing.T) {
	_, errb, code := runOD(t, guestDir(t), "hi", "-j", "100")
	if code != 1 || !strings.Contains(errb, "cannot skip past end of combined input") {
		t.Fatalf("od skip past eof: code=%d err=%q", code, errb)
	}
}

func TestODNewTypeAliases(t *testing.T) {

	out, _, code := runOD(t, guestDir(t), "AB", "-A", "n", "-t", "octal1")
	if want := " 101 102\n"; out != want || code != 0 {
		t.Fatalf("od -t octal1 = (%q, %d), want (%q, 0)", out, code, want)
	}

	out, _, code = runOD(t, guestDir(t), "AB", "-A", "n", "-t", "hex1")
	if want := " 41 42\n"; out != want || code != 0 {
		t.Fatalf("od -t hex1 = (%q, %d), want (%q, 0)", out, code, want)
	}

	out, _, code = runOD(t, guestDir(t), "AB", "-A", "n", "-t", "signed1")
	if want := "   65   66\n"; out != want || code != 0 {
		t.Fatalf("od -t signed1 = (%q, %d), want (%q, 0)", out, code, want)
	}

	out, _, code = runOD(t, guestDir(t), "AB", "-A", "n", "-t", "unsigned1")
	if want := "  65  66\n"; out != want || code != 0 {
		t.Fatalf("od -t unsigned1 = (%q, %d), want (%q, 0)", out, code, want)
	}

	out, _, code = runOD(t, guestDir(t), "ABCD", "-A", "n", "-t", "xchar")
	if want := " 41 42 43 44\n"; out != want || code != 0 {
		t.Fatalf("od -t xchar = (%q, %d), want (%q, 0)", out, code, want)
	}
	out, _, code = runOD(t, guestDir(t), "ABCD", "-A", "n", "-t", "xshort")
	if want := " 4241 4443\n"; out != want || code != 0 {
		t.Fatalf("od -t xshort = (%q, %d), want (%q, 0)", out, code, want)
	}
	out, _, code = runOD(t, guestDir(t), "ABCD", "-A", "n", "-t", "xint")
	if want := " 44434241\n"; out != want || code != 0 {
		t.Fatalf("od -t xint = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODTraditionalOffsetBeforeFile(t *testing.T) {

	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := runOD(t, dir, "", "--traditional", "+2", "-t", "o1", "in")
	if want := "0000002 143 144\n0000004\n"; out != want || code != 0 {
		t.Fatalf("od --traditional +2 file = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODBareTypeDefaultsToIntWidth(t *testing.T) {
	cases := []struct {
		format string
		data   string
		want   string
	}{
		{"d", "ABCD", " 1145258561\n"},
		{"o", "ABCD", " 010420641101\n"},
		{"u", "ABCD", " 1145258561\n"},
		{"x", "ABCD", " 44434241\n"},
		{"f", "\x00\x00\x00\x00\x00\x00\xf0\x3f", " 1\n"},

		{"octal", "ABCD", " 010420641101\n"},
		{"hex", "ABCD", " 44434241\n"},
		{"signed", "ABCD", " 1145258561\n"},
		{"unsigned", "ABCD", " 1145258561\n"},
	}
	for _, tc := range cases {
		out, errb, code := runOD(t, guestDir(t), tc.data, "-A", "n", "-t", tc.format)
		if out != tc.want || errb != "" || code != 0 {
			t.Fatalf("od -t %s = (%q, %q, %d), want (%q, \"\", 0)", tc.format, out, errb, code, tc.want)
		}
	}
}

func TestODByteCountLowercaseSuffixes(t *testing.T) {
	cases := []struct {
		text string
		want int64
	}{
		{"1", 1},
		{"010", 8},
		{"0x10", 16},
		{"0Xff", 255},
		{"1b", 512},
		{"2b", 1024},
		{"1k", 1024},
		{"3k", 3072},
		{"1m", 1048576},
		{"2m", 2097152},
	}
	for _, tc := range cases {
		got, err := parseBytes(tc.text)
		if err != nil || got != tc.want {
			t.Fatalf("parseBytes(%q) = (%d, %v), want (%d, nil)", tc.text, got, err, tc.want)
		}
	}
}

func TestODByteCountOverflowIsUsageError(t *testing.T) {

	if _, err := parseBytes("9223372036854775807G"); err == nil {
		t.Fatalf("parseBytes(overflow) = nil error, want an error")
	}

	_, errb, code := runOD(t, guestDir(t), "hi", "-N", "9223372036854775807G")
	if code != 2 || !strings.Contains(errb, "invalid byte count") {
		t.Fatalf("od -N overflow: code=%d err=%q, want code 2 and invalid byte count", code, errb)
	}

	_, errb, code = runOD(t, guestDir(t), "hi", "-j", "9223372036854775807G")
	if code != 2 || !strings.Contains(errb, "invalid skip count") {
		t.Fatalf("od -j overflow: code=%d err=%q, want code 2 and invalid skip count", code, errb)
	}

	if _, err := parseBytes("9223372036854775807m"); err == nil {
		t.Fatalf("parseBytes(overflow via m) = nil error, want an error")
	}
}

func TestODJSkipWithLowercaseSuffix(t *testing.T) {
	data := strings.Repeat("a", 1024) + "bc"
	out, errb, code := runOD(t, guestDir(t), data, "-A", "n", "-t", "c", "-j", "1k", "-w", "2")
	want := "   b   c\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("od -j 1k = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODByteCountRadixPrefixes(t *testing.T) {
	data := "0123456789abcdefXYZ"
	for _, tc := range []struct {
		name string
		arg  string
		want string
	}{
		{name: "decimal", arg: "10", want: "   a   b   c\n"},
		{name: "octal", arg: "010", want: "   8   9   a\n"},
		{name: "hex-lower", arg: "0xa", want: "   a   b   c\n"},
		{name: "hex-upper", arg: "0X10", want: "   X   Y   Z\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errb, code := runOD(t, guestDir(t), data, "-A", "n", "-t", "c", "-j", tc.arg, "-N", "3")
			if out != tc.want || errb != "" || code != 0 {
				t.Fatalf("od -j %s = (%q, %q, %d), want (%q, empty, 0)", tc.arg, out, errb, code, tc.want)
			}
		})
	}

	for _, arg := range []string{"0x", "018", "0xg", "0X-1"} {
		_, errb, code := runOD(t, guestDir(t), data, "-j", arg)
		if code != 2 || !strings.Contains(errb, "invalid skip count") {
			t.Errorf("od -j %s = (stderr %q, code %d), want invalid skip count/code 2", arg, errb, code)
		}
	}
}

func TestODShortTypeAliases(t *testing.T) {
	cases := []struct {
		flag string
		data string
		want string
	}{
		{"-D", "ABCD", " 1145258561\n"},
		{"-F", "\x00\x00\x00\x00\x00\x00\xf0\x3f", " 1\n"},
		{"-H", "ABCD", " 44434241\n"},
		{"-I", "ABCD", " 1145258561\n"},
		{"-O", "ABCD", " 010420641101\n"},
		{"-X", "ABCD", " 44434241\n"},
		{"-e", "\x00\x00\x00\x00\x00\x00\xf0\x3f", " 1\n"},
		{"-f", "\x00\x00\x80\x3f", " 1\n"},
		{"-i", "ABCD", " 1145258561\n"},
		{"-s", "ABCD", " 16961 17475\n"},
	}
	for _, tc := range cases {
		out, _, code := runOD(t, guestDir(t), tc.data, tc.flag, "-A", "n")
		if out != tc.want || code != 0 {
			t.Fatalf("od %s = (%q, %d), want (%q, 0)", tc.flag, out, code, tc.want)
		}
	}

	out, _, code := runOD(t, guestDir(t), "ABCD", "-x", "-d", "-A", "n")
	if want := " 4241 4443\n 16961 17475\n"; out != want || code != 0 {
		t.Fatalf("od -x -d = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODConcatenatedFormatStrings(t *testing.T) {
	cases := []struct {
		format string
		data   string
		want   string
	}{

		{"x1x2", "ABCD", " 41 42 43 44\n 4241 4443\n"},

		{"x1o1", "AB", " 41 42\n 101 102\n"},

		{"o1d1", "AB", " 101 102\n   65   66\n"},
	}
	for _, tc := range cases {
		out, errb, code := runOD(t, guestDir(t), tc.data, "-A", "n", "-t", tc.format)
		if out != tc.want || errb != "" || code != 0 {
			t.Fatalf("od -t %s = (%q, %q, %d), want (%q, \"\", 0)", tc.format, out, errb, code, tc.want)
		}
	}
}

func TestODCTypeCodeFormats(t *testing.T) {
	cases := []struct {
		format string
		data   string
		want   string
	}{

		{"fF", "\x00\x00\x80\x3f", " 1\n"},
		{"fD", "\x00\x00\x00\x00\x00\x00\xf0\x3f", " 1\n"},

		{"dC", "A", "   65\n"},
		{"dS", "AB", " 16961\n"},
		{"dI", "ABCD", " 1145258561\n"},
		{"dL", "ABCDEFGH", " 5208208757389214273\n"},

		{"xC", "AB", " 41 42\n"},
		{"xS", "ABCD", " 4241 4443\n"},
	}
	for _, tc := range cases {
		out, errb, code := runOD(t, guestDir(t), tc.data, "-A", "n", "-t", tc.format)
		if out != tc.want || errb != "" || code != 0 {
			t.Fatalf("od -t %s = (%q, %q, %d), want (%q, \"\", 0)", tc.format, out, errb, code, tc.want)
		}
	}
}

func TestODMultipleFileConcatenation(t *testing.T) {
	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f1"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f2"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f3"), []byte("C"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, code := runOD(t, dir, "", "-A", "n", "-t", "c", "f1", "f2", "f3")
	if want := "   A   B   C\n"; out != want || code != 0 {
		t.Fatalf("od multi-file = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODStdinDashOperand(t *testing.T) {
	out, _, code := runOD(t, guestDir(t), "hello", "-A", "n", "-t", "c", "-")
	if want := "   h   e   l   l   o\n"; out != want || code != 0 {
		t.Fatalf("od stdin dash = (%q, %d), want (%q, 0)", out, code, want)
	}
}

func TestODCombinedNamedFormats(t *testing.T) {
	cases := []struct {
		format string
		data   string
		want   string
	}{
		{"octal1hex1", "AB", " 101 102\n 41 42\n"},
		{"octal", "ABCD", " 010420641101\n"},
		{"hex", "ABCD", " 44434241\n"},
		{"signed", "ABCD", " 1145258561\n"},
		{"unsigned", "ABCD", " 1145258561\n"},
	}
	for _, tc := range cases {
		out, errb, code := runOD(t, guestDir(t), tc.data, "-A", "n", "-t", tc.format)
		if out != tc.want || errb != "" || code != 0 {
			t.Fatalf("od -t %s = (%q, %q, %d), want (%q, \"\", 0)", tc.format, out, errb, code, tc.want)
		}
	}
}

func TestODByteExactOffsetAndLimit(t *testing.T) {
	data := "0123456789"
	out, _, code := runOD(t, guestDir(t), data, "-A", "d", "-t", "c", "-j", "2", "-N", "3")
	if !strings.Contains(out, "2") && !strings.Contains(out, "234") {

		t.Fatalf("od -j 2 -N 3 = (%q, %d), format not as expected", out, code)
	}
}

func TestODFormatOrderMatchesCommandLine(t *testing.T) {
	cases := []struct {
		name string
		args []string
		data string
		want string
	}{
		{"b then c", []string{"-A", "n", "-b", "-c"}, "A", " 101\n   A\n"},
		{"c then b", []string{"-A", "n", "-c", "-b"}, "A", "   A\n 101\n"},
		{"bundled bc", []string{"-A", "n", "-bc"}, "A", " 101\n   A\n"},
		{"bundled cb", []string{"-A", "n", "-cb"}, "A", "   A\n 101\n"},
		{"t then alias", []string{"-A", "n", "-t", "x1", "-c"}, "A", " 41\n   A\n"},
		{"alias then t", []string{"-A", "n", "-c", "-t", "x1"}, "A", "   A\n 41\n"},
		{"d t o1 x", []string{"-A", "n", "-d", "-t", "o1", "-x"}, "AB", " 16961\n 101 102\n 4241\n"},
	}
	for _, tc := range cases {
		out, errb, code := runOD(t, guestDir(t), tc.data, tc.args...)
		if out != tc.want || errb != "" || code != 0 {
			t.Errorf("od %s = (%q, %q, %d), want (%q, \"\", 0)", tc.name, out, errb, code, tc.want)
		}
	}

	out, errb, code := runOD(t, guestDir(t), "A", "-b", "-c")
	want := "0000000 101\n          A\n0000001\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("od -b -c = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODXSIOffsetGating(t *testing.T) {
	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f1"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errb, code := runOD(t, dir, "", "-t", "o1", "f1", "+2")
	if code != 1 || !strings.Contains(errb, "+2") {
		t.Fatalf("od -t o1 f1 +2 = (stderr %q, code %d), want +2 open error and exit 1", errb, code)
	}
	if want := "0000000 141 142 143 144\n0000004\n"; out != want {
		t.Fatalf("od -t o1 f1 +2 stdout = %q, want %q", out, want)
	}

	for _, extra := range [][]string{{"-A", "o"}, {"-j", "1"}, {"-N", "2"}, {"-v"}} {
		args := append(append([]string{}, extra...), "f1", "+2")
		_, errb, code := runOD(t, dir, "", args...)
		if code != 1 || !strings.Contains(errb, "+2") {
			t.Errorf("od %v f1 +2 = (stderr %q, code %d), want +2 open error and exit 1", extra, errb, code)
		}
	}

	_, errb, code = runOD(t, dir, "", "f1", "f1", "+2")
	if code != 1 || !strings.Contains(errb, "+2") {
		t.Fatalf("od f1 f1 +2 = (stderr %q, code %d), want +2 open error and exit 1", errb, code)
	}

	out, errb, code = runOD(t, dir, "", "-b", "f1", "+2")
	if want := "0000002 143 144\n0000004\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od -b f1 +2 = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODXSITwoOperandNumericOffset(t *testing.T) {
	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("0123456789abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "0000010   8   9   a   b   c   d   e   f\n0000020\n"
	for _, offset := range []string{"10", "8."} {
		out, errb, code := runOD(t, dir, "", "-c", "f", offset)
		if out != want || errb != "" || code != 0 {
			t.Errorf("od -c f %s = (%q, %q, %d), want (%q, \"\", 0)", offset, out, errb, code, want)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "big"), []byte(strings.Repeat("x", 514)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runOD(t, dir, "", "-c", "big", "1b")
	if want := "0001000   x   x\n0001002\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od -c big 1b = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}

	_, errb, code = runOD(t, dir, "", "10")
	if code != 1 || !strings.Contains(errb, "10") {
		t.Fatalf("od 10 = (stderr %q, code %d), want open error for file \"10\" and exit 1", errb, code)
	}

	_, errb, code = runOD(t, dir, "", "f", "12x4")
	if code != 2 || !strings.Contains(errb, "invalid offset") {
		t.Fatalf("od f 12x4 = (stderr %q, code %d), want invalid offset and exit 2", errb, code)
	}
}

func TestODFloatSizeLetters(t *testing.T) {
	float1 := "\x00\x00\x80\x3f"
	double1 := "\x00\x00\x00\x00\x00\x00\xf0\x3f"
	cases := []struct {
		format string
		data   string
		want   string
	}{
		{"fF", float1, " 1\n"},
		{"fD", double1, " 1\n"},
	}
	for _, tc := range cases {
		out, errb, code := runOD(t, guestDir(t), tc.data, "-A", "n", "-t", tc.format)
		if out != tc.want || errb != "" || code != 0 {
			t.Errorf("od -t %s = (%q, %q, %d), want (%q, \"\", 0)", tc.format, out, errb, code, tc.want)
		}
	}
	for _, format := range []string{"fC", "fS", "fI", "dF", "xD", "uF", "oD"} {
		_, errb, code := runOD(t, guestDir(t), "ABCDEFGH", "-t", format)
		if code != 2 || !strings.Contains(errb, "unsupported output format") {
			t.Errorf("od -t %s = (stderr %q, code %d), want unsupported output format and exit 2", format, errb, code)
		}
	}
}

func TestODReadBytesBeyondEOFNotError(t *testing.T) {
	out, errb, code := runOD(t, guestDir(t), "hi", "-N", "100")
	if want := "0000000 064550\n0000002\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od -N 100 = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODOFlagOctal2(t *testing.T) {
	out, errb, code := runOD(t, guestDir(t), "ab", "-o")
	if want := "0000000 061141\n0000002\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od -o = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODHexLongSizeLetter(t *testing.T) {
	out, errb, code := runOD(t, guestDir(t), "ABCDEFGH", "-A", "n", "-t", "xL")
	if want := " 4847464544434241\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od -t xL = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODSkipAndCountAcrossFiles(t *testing.T) {
	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f1"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f2"), []byte("efgh"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errb, code := runOD(t, dir, "", "-t", "c", "-j", "6", "f1", "f2")
	if want := "0000006   g   h\n0000010\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od -j 6 f1 f2 = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}

	out, errb, code = runOD(t, dir, "", "-A", "n", "-t", "c", "-N", "6", "f1", "f2")
	if want := "   a   b   c   d   e   f\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od -N 6 f1 f2 = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}

	out, errb, code = runOD(t, dir, "", "-t", "c", "-j", "2", "-N", "4", "f1", "f2")
	if want := "0000002   c   d   e   f\n0000006\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od -j 2 -N 4 f1 f2 = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODDashMixedWithFiles(t *testing.T) {
	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f1"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f2"), []byte("C"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runOD(t, dir, "B", "-A", "n", "-t", "c", "f1", "-", "f2")
	if want := "   A   B   C\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("od f1 - f2 = (%q, %q, %d), want (%q, \"\", 0)", out, errb, code, want)
	}
}

func TestODDefaultUsesNativeByteOrder(t *testing.T) {
	profile := runtimeProfile()
	profile.abi, profile.endian = abiFor("linux", "s390x"), binary.BigEndian
	out, errb, code := runODProfile(t, profile, guestDir(t), "AB", "-A", "n", "-t", "x2")
	if want := " 4142\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("big-endian native od = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}
	out, errb, code = runODProfile(t, profile, guestDir(t), "AB", "-A", "n", "-t", "x2", "--endian=little")
	if want := " 4241\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("explicit little od = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}
	if nativeEndianFor("s390x") != binary.BigEndian || nativeEndianFor("amd64") != binary.LittleEndian {
		t.Fatal("target byte-order table does not distinguish big- and little-endian architectures")
	}
}

func TestODCTypeSizesFollowTargetABI(t *testing.T) {
	linux := abiFor("linux", "amd64")
	windows := abiFor("windows", "amd64")
	if linux.longSize != 8 || windows.longSize != 4 {
		t.Fatalf("C long sizes: linux/amd64=%d windows/amd64=%d, want 8 and 4", linux.longSize, windows.longSize)
	}
	f, err := formatForType('x', "L", "xL", windows)
	if err != nil || f.size != 4 {
		t.Fatalf("windows xL = (%+v, %v), want size 4", f, err)
	}
	f, err = formatForType('d', "I", "dI", windows)
	if err != nil || f.size != 4 {
		t.Fatalf("windows dI = (%+v, %v), want size 4", f, err)
	}
	for _, goos := range []string{"linux", "darwin", "freebsd", "solaris", "openbsd", "netbsd", "dragonfly"} {
		abi := abiFor(goos, "amd64")
		if abi.longDoubleSize != 16 || abi.longDoubleEncoding != floatX87 {
			t.Errorf("%s/amd64 long double = (%d, %d), want 16-byte x87", goos, abi.longDoubleSize, abi.longDoubleEncoding)
		}
	}
	for _, tc := range []struct {
		goos, goarch string
		size         int
		encoding     floatEncoding
	}{
		{goos: "illumos", goarch: "amd64", size: 16, encoding: floatX87},
		{goos: "ios", goarch: "arm64", size: 8, encoding: floatIEEE64},
		{goos: "linux", goarch: "mips", size: 8, encoding: floatIEEE64},
		{goos: "linux", goarch: "mipsle", size: 8, encoding: floatIEEE64},
		{goos: "aix", goarch: "ppc64", size: 8, encoding: floatIEEE64},
		{goos: "linux", goarch: "ppc64", size: 16, encoding: floatIBMDoubleDouble},
		{goos: "linux", goarch: "ppc64le", size: 16, encoding: floatIBMDoubleDouble},
		{goos: "plan9", goarch: "arm", size: 0, encoding: floatNone},
	} {
		abi := abiFor(tc.goos, tc.goarch)
		if abi.longDoubleSize != tc.size || abi.longDoubleEncoding != tc.encoding {
			t.Errorf("%s/%s long double = (%d, %d), want (%d, %d)", tc.goos, tc.goarch,
				abi.longDoubleSize, abi.longDoubleEncoding, tc.size, tc.encoding)
		}
	}
}

func TestODAIXPPC64LongDoubleIsIEEE64(t *testing.T) {
	profile := runtimeProfile()
	profile.abi, profile.endian = abiFor("aix", "ppc64"), binary.BigEndian
	one := string([]byte{0x3f, 0xf0, 0, 0, 0, 0, 0, 0})
	for _, format := range []string{"fL", "f8"} {
		out, errb, code := runODProfile(t, profile, guestDir(t), one, "-A", "n", "-t", format)
		if want := " 1\n"; out != want || errb != "" || code != 0 {
			t.Errorf("AIX ppc64 %s = (%q, %q, %d), want (%q, empty, 0)", format, out, errb, code, want)
		}
	}
}

func TestODLinuxPPC64IBMDoubleDoubleOracles(t *testing.T) {
	profiles := []struct {
		name    string
		profile platformProfile
		encode  func(uint64) []byte
	}{
		{name: "ppc64", profile: platformProfile{abi: abiFor("linux", "ppc64"), endian: binary.BigEndian}, encode: func(v uint64) []byte {
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, v)
			return b
		}},
		{name: "ppc64le", profile: platformProfile{abi: abiFor("linux", "ppc64le"), endian: binary.LittleEndian}, encode: func(v uint64) []byte {
			b := make([]byte, 8)
			binary.LittleEndian.PutUint64(b, v)
			return b
		}},
	}
	for _, target := range profiles {
		t.Run(target.name, func(t *testing.T) {
			target.profile.openInput = runtimeProfile().openInput
			for _, tc := range []struct {
				name      string
				hi, lo    uint64
				want      string
				wantClass floatClass
				wantNeg   bool
			}{
				{name: "third", hi: 0x3fd5555555555555, lo: 0x3c75555555555556, want: " 0.333333333333333333333333333333335\n"},
				{name: "maximum", hi: 0x7fefffffffffffff, lo: 0x7c8ffffffffffffe, want: " 1.79769313486231580793728971405301e+308\n"},
				{name: "infinity", hi: 0x7ff0000000000000, want: " inf\n", wantClass: floatInfinity},
				{name: "nan", hi: 0x7ff8000000000000, want: " nan\n", wantClass: floatNaN},
				{name: "minimum subnormal", hi: 1, want: " 5e-324\n"},
			} {
				data := append(target.encode(tc.hi), target.encode(tc.lo)...)
				decoded := decodeIBMDoubleDouble(append([]byte(nil), data...), target.profile.endian)
				if decoded.class != tc.wantClass || decoded.negative != tc.wantNeg {
					t.Errorf("%s decode class/sign = (%d, %v), want (%d, %v)", tc.name, decoded.class, decoded.negative, tc.wantClass, tc.wantNeg)
				}
				out, errb, code := runODProfile(t, target.profile, guestDir(t), string(data), "-A", "n", "-t", "fL")
				if out != tc.want || errb != "" || code != 0 {
					t.Errorf("%s = (%q, %q, %d), want (%q, empty, 0)", tc.name, out, errb, code, tc.want)
				}
			}
		})
	}
}

func TestODUbuntuAMD64LongDoubleGNUOracleAndPortableTargets(t *testing.T) {
	profile := runtimeProfile()
	profile.abi, profile.endian = abiFor("linux", "amd64"), binary.LittleEndian
	one := []byte{0, 0, 0, 0, 0, 0, 0, 0x80, 0xff, 0x3f, 0, 0, 0, 0, 0, 0}
	out, errb, code := runODProfile(t, profile, guestDir(t), string(one), "-A", "n", "-t", "fL")
	if want := " 1\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("linux/amd64 fL = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}
	big := append([]byte(nil), one...)
	reverseBytes(big)
	out, errb, code = runODProfile(t, profile, guestDir(t), string(big), "-A", "n", "-t", "fL", "--endian=big")
	if want := " 1\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("big-endian x87 fL = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}

	profile.abi, profile.endian = abiFor("linux", "arm64"), binary.LittleEndian
	quadOne := make([]byte, 16)
	binary.LittleEndian.PutUint64(quadOne[8:], uint64(0x3fff)<<48)
	out, errb, code = runODProfile(t, profile, guestDir(t), string(quadOne), "-A", "n", "-t", "fL")
	if want := " 1\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("linux/arm64 fL = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}

	profile.abi, profile.endian = abiFor("linux", "ppc64"), binary.BigEndian
	ppcOne := []byte{0x3f, 0xf0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	out, errb, code = runODProfile(t, profile, guestDir(t), string(ppcOne), "-A", "n", "-t", "fL")
	if want := " 1\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("linux/ppc64 fL = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}
}

func TestODLongDoublePrecisionOracles(t *testing.T) {
	profile := runtimeProfile()
	profile.abi, profile.endian = abiFor("linux", "amd64"), binary.LittleEndian
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "third", data: []byte{0xab, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa, 0xfd, 0x3f, 0, 0, 0, 0, 0, 0}, want: " 0.33333333333333333334\n"},
		{name: "max finite", data: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe, 0x7f, 0, 0, 0, 0, 0, 0}, want: " 1.189731495357231765e+4932\n"},
	} {
		out, errb, code := runODProfile(t, profile, guestDir(t), string(tc.data), "-A", "n", "-t", "fL")
		if out != tc.want || errb != "" || code != 0 {
			t.Errorf("x87 %s = (%q, %q, %d), want (%q, empty, 0)", tc.name, out, errb, code, tc.want)
		}
	}

	profile.abi, profile.endian = abiFor("linux", "arm64"), binary.LittleEndian
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "third", data: []byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0xfd, 0x3f}, want: " 0.3333333333333333333333333333333333\n"},
		{name: "max finite", data: []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe, 0x7f}, want: " 1.189731495357231765085759326628007e+4932\n"},
	} {
		out, errb, code := runODProfile(t, profile, guestDir(t), string(tc.data), "-A", "n", "-t", "fL")
		if out != tc.want || errb != "" || code != 0 {
			t.Errorf("IEEE128 %s = (%q, %q, %d), want (%q, empty, 0)", tc.name, out, errb, code, tc.want)
		}
	}
}

func TestODPartialItemAppendsNullBytes(t *testing.T) {
	out, errb, code := runOD(t, guestDir(t), "A", "-A", "n", "-t", "x2", "--endian=big")
	if want := " 4100\n"; out != want || errb != "" || code != 0 {
		t.Fatalf("big-endian partial x2 = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}
}

func TestODExactTypeGrammar(t *testing.T) {
	profile := runtimeProfile()
	profile.abi, profile.endian = abiFor("linux", "amd64"), binary.LittleEndian
	for _, format := range []string{"", "a1", "cC", "fI", "fLD", "dD", "xF", "x0", "f3", "x1,x2", "x1 x2", "x1\tx2"} {
		_, errb, code := runODProfile(t, profile, guestDir(t), "0123456789abcdef", "-t", format)
		if code != 2 || !strings.Contains(errb, "unsupported output format") {
			t.Errorf("od -t %s = (stderr %q, code %d), want grammar error/code 2", format, errb, code)
		}
	}
	if _, err := parseFormats([]string{"f16dI"}, profile.abi); err != nil {
		t.Fatalf("valid concatenated f16dI rejected: %v", err)
	}
}

func TestODTraditionalOffsetOverflow(t *testing.T) {
	for _, offset := range []string{"+1000000000000000000b", "+2000000000000000000b"} {
		_, errb, code := runOD(t, guestDir(t), "A", offset)
		if code != 2 || !strings.Contains(errb, "invalid offset") {
			t.Errorf("od %s = (stderr %q, code %d), want invalid offset/code 2", offset, errb, code)
		}
	}
}

type failingODWriter struct{ err error }

func (w failingODWriter) Write([]byte) (int, error) { return 0, w.err }

type errorReadCloser struct {
	io.Reader
	err error
}

func (r errorReadCloser) Close() error { return r.err }

func TestODOutputAndCloseErrorsSetStatus(t *testing.T) {
	var errb bytes.Buffer
	rc := &toolcompat.RunContext{Ctx: context.Background(), Dir: guestDir(t), Stdio: toolcompat.Stdio{
		In: strings.NewReader("A"), Out: failingODWriter{err: errors.New("broken output")}, Err: &errb,
	}}
	if code := cmd.Run(rc, []string{"-A", "n", "-t", "x1"}); code != 1 || !strings.Contains(errb.String(), "write error: broken output") {
		t.Fatalf("output failure = (stderr %q, code %d), want write diagnostic/code 1", errb.String(), code)
	}

	profile := runtimeProfile()
	profile.openInput = func(string) (io.ReadCloser, error) {
		return errorReadCloser{Reader: strings.NewReader("A"), err: errors.New("broken close")}, nil
	}
	out, errText, code := runODProfile(t, profile, guestDir(t), "", "-A", "n", "-t", "x1", "input")
	if out != " 41\n" || code != 1 || !strings.Contains(errText, "input: close error:") || !strings.Contains(errText, "roken close") {
		t.Fatalf("close failure = (%q, %q, %d), want data plus close diagnostic/code 1", out, errText, code)
	}
}

func TestODMissingFileContinues(t *testing.T) {
	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f2"), []byte("efgh"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runOD(t, dir, "", "-A", "n", "-t", "c", "missing", "f2")
	if code != 1 || !strings.Contains(errb, "missing") {
		t.Fatalf("od missing f2 = (stderr %q, code %d), want diagnostic and exit 1", errb, code)
	}
	if want := "   e   f   g   h\n"; out != want {
		t.Fatalf("od missing f2 stdout = %q, want %q", out, want)
	}
}

func TestODStopsOptionParsingAtFirstOperand(t *testing.T) {
	dir := guestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "first"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "-x"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "--for"), []byte("C"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errb, code := runOD(t, dir, "", "-A", "n", "-t", "c", "first", "-x", "--for")
	if code != 0 || errb != "" || out != "   A   B   C\n" {
		t.Fatalf("od option-looking operand = (%q, %q, %d)", out, errb, code)
	}
}

func TestNormalizeTraditionalOffsetStopsAtFirstOperand(t *testing.T) {
	got := normalizeTraditionalLeadingOffset([]string{"--traditional", "first", "+2"})
	if strings.Join(got, "\x00") != strings.Join([]string{"--traditional", "first", "+2"}, "\x00") {
		t.Fatalf("post-operand +offset was rewritten: %q", got)
	}
	got = normalizeTraditionalLeadingOffset([]string{"--traditional", "-t", "o1", "+2", "first"})
	if strings.Join(got, "\x00") != strings.Join([]string{"--traditional", "-t", "o1", "--skip-bytes=2", "first"}, "\x00") {
		t.Fatalf("leading +offset rewrite = %q", got)
	}
}
