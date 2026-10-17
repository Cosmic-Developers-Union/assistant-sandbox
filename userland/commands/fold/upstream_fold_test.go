package foldcmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

func runFold(t *testing.T, input string, args ...string) (string, string, int) {
	t.Helper()
	var out, err bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx: context.Background(), Dir: guestDir(t), Env: []string{"LC_ALL=C.UTF-8"},
		Stdio: toolcompat.Stdio{In: strings.NewReader(input), Out: &out, Err: &err},
	}
	code := run(rc, args)
	return out.String(), err.String(), code
}

func TestFoldWidthRunes(t *testing.T) {
	out, stderr, code := runFold(t, "abcdef\n", "-w", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "abc\ndef\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldFirstWideUnitPreservesLeadingBreak(t *testing.T) {
	out, stderr, code := runFold(t, "\tCajkKP", "-w", "1")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "\n\t\nC\na\nj\nk\nK\nP"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldWideUnitAfterTextDoesNotAddEmptySegment(t *testing.T) {
	out, stderr, code := runFold(t, "a\t", "-w", "1")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "a\n\t"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldSpacesAndFile(t *testing.T) {
	dir := guestDir(t)
	name := filepath.Join(dir, "in.txt")
	if err := vfs.WriteFile(name, []byte("alpha beta gamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	rc := &toolcompat.RunContext{Ctx: context.Background(), Dir: dir, Stdio: toolcompat.Stdio{Out: &out, Err: &stderr}}
	code := run(rc, []string{"-s", "-w", "10", "in.txt"})
	if code != 0 || stderr.String() != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}

	if want := "alpha \nbeta gamma\n"; out.String() != want {
		t.Fatalf("out=%q want %q", out.String(), want)
	}
}

func TestFoldSpacesKeepsTrailingBlank(t *testing.T) {

	out, stderr, code := runFold(t, "hello world\n", "-s", "-w", "8")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "hello \nworld\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldSpacesKeepsLeadingBlanks(t *testing.T) {

	out, stderr, code := runFold(t, "aa   bb\n", "-s", "-w", "4")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "aa  \n bb\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldTabAdvancesColumn(t *testing.T) {

	out, stderr, code := runFold(t, "a\tb\n", "-w", "8")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "a\t\nb\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldBackspaceDecrementsColumn(t *testing.T) {
	out, stderr, code := runFold(t, "ab\bcd\n", "-w", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}

	if want := "ab\bcd\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldBackspaceMovesOneColumnAfterWideRune(t *testing.T) {
	out, stderr, code := runFold(t, "界\bXX\n", "-w", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "界\bX\nX\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldCarriageReturnResetsColumn(t *testing.T) {
	out, stderr, code := runFold(t, "abc\rdef\n", "-w", "4")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "abc\rdef\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldBytesCountsControlCharsAsOne(t *testing.T) {

	out, stderr, code := runFold(t, "a\tb\n", "-b", "-w", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "a\tb\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldBytesPreservesUtf8UnitsAtSmallWidth(t *testing.T) {
	out, stderr, code := runFold(t, "界界", "-w", "1", "-b")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "\n界\n界"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldBytesWideUnitAfterTextDoesNotAddEmptySegment(t *testing.T) {
	out, stderr, code := runFold(t, "a界", "-w", "1", "-b")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "a\n界"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldCharactersKeepsUTF8RunesWhole(t *testing.T) {
	out, stderr, code := runFold(t, "ééé\n", "-w", "2", "-c")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "éé\né\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldDefaultUsesDisplayColumns(t *testing.T) {
	out, stderr, code := runFold(t, "界界界\n", "-w", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "界\n界\n界\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}

	out, stderr, code = runFold(t, "界界界\n", "-w", "3", "-c")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "界界界\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldCharactersTabStillAdvances(t *testing.T) {

	out, stderr, code := runFold(t, "a\tb\n", "-c", "-w", "8")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "a\t\nb\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldBytesCountsUTF8EncodingWidth(t *testing.T) {
	out, stderr, code := runFold(t, "éé\n", "-w", "2", "-b")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "é\né\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldObsoleteWidthSyntax(t *testing.T) {
	out, stderr, code := runFold(t, "abcdef\n", "-3")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if want := "abc\ndef\n"; out != want {
		t.Fatalf("out=%q want %q", out, want)
	}
}

func TestFoldPOSIXStopsOptionParsingAtFirstOperand(t *testing.T) {
	dir := guestDir(t)
	for name, content := range map[string]string{
		"first":     "abc\n",
		"-w":        "def\n",
		"--width=1": "ghi\n",
		"-3":        "jkl\n",
	} {
		if err := vfs.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"-w", "--width=1", "-3"} {
		var out, errb bytes.Buffer
		rc := &toolcompat.RunContext{
			Ctx: context.Background(), Dir: dir,
			Env:   []string{"LC_ALL=C", "POSIXLY_CORRECT="},
			Stdio: toolcompat.Stdio{Out: &out, Err: &errb},
		}
		code := run(rc, []string{"first", name})
		want := "abc\n" + map[string]string{
			"-w": "def\n", "--width=1": "ghi\n", "-3": "jkl\n",
		}[name]
		if code != 0 || out.String() != want || errb.String() != "" {
			t.Errorf("fold first %s = (%q, %q, %d), want (%q, empty, 0)", name, out.String(), errb.String(), code, want)
		}
	}
}

func TestFoldGNUOptionsRemainInterspersed(t *testing.T) {
	dir := guestDir(t)
	if err := vfs.WriteFile(filepath.Join(dir, "input"), []byte("abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx: context.Background(), Dir: dir, Env: []string{"LC_ALL=C"},
		Stdio: toolcompat.Stdio{Out: &out, Err: &errb},
	}
	code := run(rc, []string{"input", "-3"})
	if code != 0 || out.String() != "abc\ndef\n" || errb.String() != "" {
		t.Fatalf("GNU interspersed obsolete width = (%q, %q, %d)", out.String(), errb.String(), code)
	}
}

func TestFoldRejectsBadWidth(t *testing.T) {
	_, stderr, code := runFold(t, "", "-w", "0")
	if code != 2 || !strings.Contains(stderr, "invalid number of columns") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestFoldPOSIXOperandsAndErrors(t *testing.T) {
	dir := guestDir(t)
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := vfs.WriteFile(first, []byte("abcd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := vfs.WriteFile(second, []byte("wxyz\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	rc := &toolcompat.RunContext{
		Ctx: context.Background(),
		Dir: dir,
		Stdio: toolcompat.Stdio{
			In:  strings.NewReader("stdin\n"),
			Out: &out,
			Err: &errb,
		},
	}
	code := run(rc, []string{"-w", "2", "first", "-", "second"})
	if code != 0 || errb.String() != "" || out.String() != "ab\ncd\nst\ndi\nn\nwx\nyz\n" {
		t.Fatalf("fold files/stdin: code=%d out=%q err=%q", code, out.String(), errb.String())
	}

	out.Reset()
	errb.Reset()
	code = run(rc, []string{"missing", "first"})
	if code != 1 || !strings.Contains(errb.String(), "missing") || out.String() != "abcd\n" {
		t.Fatalf("fold missing then file: code=%d out=%q err=%q", code, out.String(), errb.String())
	}
}
