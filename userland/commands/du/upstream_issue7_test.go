package ducmd

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestDuIssue7DefaultOperandIsWorkingDirectory(t *testing.T) {
	dir := mkTree(t)
	out, _, code := runToolAt(t, dir, "-b")
	if code != 0 {
		t.Fatalf("du (no operands) code = %d, want 0", code)
	}
	var paths []string
	for _, ln := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		parts := strings.SplitN(ln, " ", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed du line %q", ln)
		}
		paths = append(paths, parts[1])
	}

	sort.Strings(paths)
	if strings.Join(paths, " ") != ". ./tree ./tree/sub" {
		t.Errorf("du (no operands) paths = %v, want the complete hierarchy", paths)
	}
}

func TestDuIssue7StdoutFormatRoundsUpToBlocks(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f", strings.Repeat("x", 5000))
	out, errb, code := runToolAt(t, dir, "-A", "f")
	if code != 0 || errb != "" || out != "10 f\n" {
		t.Errorf("du -A f = (%q, %q, %d), want exactly \"10 f\\n\" in 512-byte units", out, errb, code)
	}
	out, errb, code = runToolAt(t, dir, "-A", "-k", "f")
	if code != 0 || errb != "" || out != "5 f\n" {
		t.Errorf("du -A -k f = (%q, %q, %d), want exactly \"5 f\\n\" in 1024-byte units", out, errb, code)
	}
}

func TestDuIssue7SummarizeFileOperand(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f", strings.Repeat("x", 3000))
	out, errb, code := runToolAt(t, dir, "-A", "-s", "f")
	if code != 0 || errb != "" || out != "6 f\n" {
		t.Errorf("du -A -s f = (%q, %q, %d), want \"6 f\\n\" (ceil(3000/512))", out, errb, code)
	}
}

func TestDuIssue7OneFileSystemIsRefused(t *testing.T) {
	dir := mkTree(t)
	if _, _, code := runToolAt(t, dir, "-b", "tree"); code != 0 {
		t.Fatalf("du -b tree code = %d", code)
	}
	out, errb, code := runToolAt(t, dir, "-b", "-x", "tree")
	if code != 2 || out != "" || !strings.Contains(errb, "one-file-system") {
		t.Fatalf("du -b -x tree = (%q, %q, %d), want 明确拒绝", out, errb, code)
	}
}

func TestDuIssue7SymlinkNotFollowedByDefault(t *testing.T) {
	dir := t.TempDir()

	root := filepath.Join(dir, "tree")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "f", strings.Repeat("x", 2000))

	if err := os.Symlink("tree", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	out, _, code := runToolAt(t, dir, "-A", "-s", "link")
	if code != 0 {
		t.Fatalf("du -A -s link code = %d", code)
	}
	line := strings.TrimSuffix(out, "\n")
	if n := parseFirstValue(t, line); n != 1 {
		t.Errorf("du followed or mis-measured the symlink operand: %q (want the link's own single 512-byte unit)", out)
	}

	treeOut, _, _ := runToolAt(t, dir, "-A", "-s", "tree")
	if treeN := parseFirstValue(t, strings.TrimSuffix(treeOut, "\n")); treeN <= 1 {
		t.Fatalf("control tree total %d too small to discriminate; fixture is broken", treeN)
	}
}

func TestDuIssue7OperandErrorsDoNotAbortLaterOperands(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "good", strings.Repeat("x", 700))
	out, errb, code := runToolAt(t, dir, "-A", "missing", "good")
	if code != 1 || !strings.Contains(errb, "cannot access 'missing'") || out != "2 good\n" {
		t.Fatalf("du missing good = (%q, %q, %d), want later operand output and nonzero status", out, errb, code)
	}
}

func TestDuIssue7LastHLWinsForOperandSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on many Windows setups")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "outside"), "payload", "1234567")
	if err := os.Mkdir(filepath.Join(dir, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(dir, "target", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "arg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, paths := parseDu(t, dir, "-abLH", "arg")
	if got := strings.Join(paths, " "); got != "arg/link arg" {
		t.Fatalf("du -LH arg paths = %q, want final -H to follow only the operand symlink", got)
	}
	_, paths = parseDu(t, dir, "-abHL", "arg")
	if got := strings.Join(paths, " "); got != "arg/link/payload arg/link arg" {
		t.Fatalf("du -HL arg paths = %q, want final -L to follow nested symlinks", got)
	}
}

func parseFirstValue(t *testing.T, line string) int64 {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) == 0 {
		t.Fatalf("empty du line")
	}
	var n int64
	for _, c := range fields[0] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
