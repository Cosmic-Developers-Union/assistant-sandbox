package joincmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

type fakeJoinCtype struct{ closed bool }

func (f *fakeJoinCtype) ToUpper(b []byte) ([]byte, error) {
	out := make([]byte, len(b))
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			out[i] = c - ('a' - 'A')
		case c == 0xE4:
			out[i] = 0xC4
		case c == 0xE9:
			out[i] = 0xC9
		default:
			out[i] = c
		}
	}
	return out, nil
}

func (f *fakeJoinCtype) Close() error { f.closed = true; return nil }

func runJoinWithProviders(t *testing.T, dir string, env []string, open collatorOpener, openCtype ctypeOpener, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx:   context.Background(),
		Dir:   dir,
		Env:   env,
		Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: &out, Err: &errb},
	}
	code := runWithProviders(rc, args, open, openCtype)
	return out.String(), errb.String(), code
}

func mustNotOpenCollator(t *testing.T) collatorOpener {
	return func(string) (stringCollator, error) {
		t.Helper()
		t.Fatal("collator opened on the -i byte path")
		return nil, nil
	}
}

func TestJoinLCCTypeFoldsHighByteForIgnoreCase(t *testing.T) {
	dir := guestDir(t)
	writeJoinFile(t, dir, "f1", string([]byte{0xC4})+" A\n")
	writeJoinFile(t, dir, "f2", string([]byte{0xE4})+" B\n")
	fake := &fakeJoinCtype{}
	opened := ""
	openCtype := func(name string) (ctypeProvider, error) { opened = name; return fake, nil }
	out, errb, code := runJoinWithProviders(t, dir, []string{"LC_CTYPE=de_DE.ISO-8859-1"},
		mustNotOpenCollator(t), openCtype, "-i", "f1", "f2")
	want := string([]byte{0xC4}) + " A B\n"
	if code != 0 || errb != "" || out != want {
		t.Fatalf("run = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, want)
	}
	if opened != "de_DE.ISO-8859-1" || !fake.closed {
		t.Fatalf("opened=%q closed=%v, want opened+closed once", opened, fake.closed)
	}
}

func TestJoinLCCTypeCKeepsASCIIFold(t *testing.T) {
	dir := guestDir(t)
	writeJoinFile(t, dir, "f1", "A x\n"+string([]byte{0xC4})+" y\n")
	writeJoinFile(t, dir, "f2", "a z\n")
	mustNotOpenCtype := func(string) (ctypeProvider, error) {
		t.Fatal("ctype provider opened under C LC_CTYPE")
		return nil, nil
	}
	out, errb, code := runJoinWithProviders(t, dir, []string{"LC_CTYPE=C"},
		mustNotOpenCollator(t), mustNotOpenCtype, "-i", "f1", "f2")

	if code != 0 || errb != "" || out != "A x z\n" {
		t.Fatalf("run = (%q, %q, %d), want ASCII-only fold pairing", out, errb, code)
	}
}

func TestJoinLCCTypeOpenFailureFailsClosed(t *testing.T) {
	dir := guestDir(t)
	writeJoinFile(t, dir, "f1", string([]byte{0xC4})+" A\n")
	writeJoinFile(t, dir, "f2", string([]byte{0xE4})+" B\n")
	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx: context.Background(), Dir: dir,
		Env:   []string{"LC_CTYPE=de_DE.ISO-8859-1"},
		Stdio: toolcompat.Stdio{In: panicReader{}, Out: &out, Err: &errb},
	}
	code := runWithProviders(rc, []string{"-i", "f1", "f2"},
		mustNotOpenCollator(t), func(string) (ctypeProvider, error) {
			return nil, errors.New("provider unavailable")
		})
	if code != 2 || out.Len() != 0 ||
		!strings.Contains(errb.String(), "LC_CTYPE=de_DE.ISO-8859-1") ||
		!strings.Contains(errb.String(), "provider unavailable") {
		t.Fatalf("open failure = (out %q, err %q, code %d), want fail-closed exit 2", out.String(), errb.String(), code)
	}
}

func TestJoinLCCTypeSnapshotFailure(t *testing.T) {
	dir := guestDir(t)
	writeJoinFile(t, dir, "f1", "a A\n")
	writeJoinFile(t, dir, "f2", "a B\n")
	fe := &failingJoinCtype{}
	_, errb, code := runJoinWithProviders(t, dir, []string{"LC_CTYPE=de_DE.ISO-8859-1"},
		mustNotOpenCollator(t), func(string) (ctypeProvider, error) { return fe, nil }, "-i", "f1", "f2")
	if code != 2 || !strings.Contains(errb, "toupper broke") {
		t.Fatalf("snapshot failure = (err %q, code %d), want exit 2", errb, code)
	}
	if !fe.closed {
		t.Fatal("provider was not closed after snapshot failure")
	}
}

type failingJoinCtype struct{ closed bool }

func (failingJoinCtype) ToUpper([]byte) ([]byte, error) { return nil, errors.New("toupper broke") }
func (f *failingJoinCtype) Close() error                { f.closed = true; return nil }

func TestJoinWithoutIgnoreCaseNeverOpensCtype(t *testing.T) {
	dir := guestDir(t)
	writeJoinFile(t, dir, "f1", "a A\n")
	writeJoinFile(t, dir, "f2", "a B\n")
	out, errb, code := runJoinWithProviders(t, dir,
		[]string{"LC_CTYPE=de_DE.ISO-8859-1", "LC_COLLATE=C"},
		mustNotOpenCollator(t), func(string) (ctypeProvider, error) {
			t.Fatal("ctype provider opened without -i")
			return nil, nil
		}, "f1", "f2")
	if code != 0 || errb != "" || out != "a A B\n" {
		t.Fatalf("run = (%q, %q, %d), want plain byte join, no ctype open", out, errb, code)
	}
}
