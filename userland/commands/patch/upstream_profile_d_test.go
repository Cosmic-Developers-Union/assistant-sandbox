package patchcmd

import (
	"strings"
	"testing"
)

func TestContextRejectContainsOnlyFailedHunk(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "target.txt", "")
	diff := "*** source.txt\n--- target.txt\n***************\n*** 1 ****\n! original\n--- 1 ----\n! changed\n"
	_, stderr, code := runIn(t, dir, diff, "target.txt")
	if code != 1 {
		t.Fatalf("exit=%d stderr=%q, want a rejected hunk", code, stderr)
	}
	reject := readFile(t, dir, "target.txt.rej")
	if !strings.HasPrefix(reject, "***************\n") || strings.Contains(reject, "source.txt") || strings.Contains(reject, "target.txt") {
		t.Fatalf("reject includes filename headers instead of only the failed hunk: %q", reject)
	}
}

func TestContextLineLikeEdCommandStaysAUnifiedDiff(t *testing.T) {
	for _, line := range []string{"a", "c", "d", "i"} {
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", line+"\nbeta\ngamma\n")
		diff := "--- f.txt\n+++ f.txt\n@@ -1,3 +1,3 @@\n " + line + "\n-beta\n+BETA\n gamma\n"
		stdout, stderr, code := runIn(t, dir, diff)
		if code != 0 {
			t.Fatalf("%q context: code=%d stdout=%q stderr=%q", line, code, stdout, stderr)
		}
		if got, want := readFile(t, dir, "f.txt"), line+"\nBETA\ngamma\n"; got != want {
			t.Fatalf("%q context: result=%q want=%q", line, got, want)
		}
	}
}

func TestNormalDiffWithEdLikeDataStaysANormalDiff(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "a\nbeta\n")
	_, stderr, code := runIn(t, dir, "Index: f.txt\n2c2\n< beta\n---\n> BETA\n")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got, want := readFile(t, dir, "f.txt"), "a\nBETA\n"; got != want {
		t.Fatalf("result=%q want=%q", got, want)
	}
}

func TestAddressedEdScriptStillAutoDetects(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "one\ntwo\n")
	_, stderr, code := runIn(t, dir, "Index: f.txt\n2c\nTWO\n.\n")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got, want := readFile(t, dir, "f.txt"), "one\nTWO\n"; got != want {
		t.Fatalf("result=%q want=%q", got, want)
	}
}

func TestStripTreatsLeadingSlashRunAsOneComponent(t *testing.T) {
	for _, prefix := range []string{"/", "//", "///"} {
		dir := t.TempDir()
		writeFile(t, dir, "curds/whey/f.txt", "one\ntwo\n")
		diff := "--- " + prefix + "curds/whey/f.txt\n+++ " + prefix + "curds/whey/f.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"
		_, stderr, code := runIn(t, dir, diff, "-p1")
		if code != 0 {
			t.Fatalf("prefix %q: code=%d stderr=%q", prefix, code, stderr)
		}
		if got, want := readFile(t, dir, "curds/whey/f.txt"), "one\nTWO\n"; got != want {
			t.Fatalf("prefix %q: result=%q want=%q", prefix, got, want)
		}
	}
}

func TestStripSkipsAdjacentInteriorSlashes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "whey/f.txt", "one\ntwo\n")
	diff := "--- curds//whey/f.txt\n+++ curds//whey/f.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"
	if _, stderr, code := runIn(t, dir, diff, "-p1"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if got, want := readFile(t, dir, "whey/f.txt"), "one\nTWO\n"; got != want {
		t.Fatalf("-p1 result=%q want=%q", got, want)
	}

	dir = t.TempDir()
	writeFile(t, dir, "f.txt", "one\ntwo\n")
	if _, stderr, code := runIn(t, dir, diff); code != 0 {
		t.Fatalf("default: code=%d stderr=%q", code, stderr)
	}
	if got, want := readFile(t, dir, "f.txt"), "one\nTWO\n"; got != want {
		t.Fatalf("default result=%q want=%q", got, want)
	}
}

func TestProgressNamesTheStrippedTargetExactly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "one\ntwo\n")
	diff := "--- a/deep/f.txt\n+++ b/deep/f.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"
	stdout, stderr, code := runIn(t, dir, diff)
	if code != 0 || stdout != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stderr != "patching file f.txt\n" {
		t.Fatalf("stderr=%q", stderr)
	}
	fresh := t.TempDir()
	writeFile(t, fresh, "f.txt", "one\ntwo\n")
	if _, stderr, code = runIn(t, fresh, diff, "--dry-run"); code != 0 || stderr != "patching file f.txt (dry run)\n" {
		t.Fatalf("dry run: code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "  ") {
		t.Fatalf("dry-run message has doubled blanks: %q", stderr)
	}
}

func TestStripComponentsPOSIXExamples(t *testing.T) {
	const name = "/curds/whey/src/blurfl/blurfl.c"
	cases := []struct {
		strip int
		want  string
	}{
		{autoStripSentinel, "blurfl.c"},
		{0, name},
		{1, "curds/whey/src/blurfl/blurfl.c"},
		{4, "blurfl/blurfl.c"},
		{6, ""},
	}
	for _, c := range cases {
		if got := targetName(name, c.strip); got != c.want {
			t.Fatalf("targetName(-p %d)=%q want %q", c.strip, got, c.want)
		}
	}
}
