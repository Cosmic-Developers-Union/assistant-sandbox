package testcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

func runIn(t *testing.T, cmdt *toolcompat.Tool, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	bindDir(t, dir)
	rc := &toolcompat.RunContext{
		Ctx:   context.Background(),
		Dir:   dir,
		Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: &out, Err: &errb},
	}
	code = cmdt.Run(rc, args)
	return out.String(), errb.String(), code
}

func runTest(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runIn(t, cmd, t.TempDir(), args...)
}

func runBracket(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runIn(t, bracketCmd, t.TempDir(), args...)
}

func truth(t *testing.T, want int, args ...string) {
	t.Helper()
	out, errb, code := runTest(t, args...)
	if code != want {
		t.Errorf("test %q = %d, want %d (stderr %q)", args, code, want, errb)
	}
	if out != "" || errb != "" {
		t.Errorf("test %q wrote stdout=%q stderr=%q, want silence", args, out, errb)
	}
}

func TestRegistration(t *testing.T) {
	got, ok := toolcompat.Lookup("test")
	if !ok || got == nil {
		t.Fatal("test 未在 toolcompat 登记")
	}
	if got.Name != "test" {
		t.Errorf("Lookup(\"test\").Name = %q", got.Name)
	}
	if got.Synopsis == "" || got.Usage == "" || got.Run == nil {
		t.Error("test: 工具条目不完整")
	}

	if got == bracketCmd {
		t.Error("test 与 [ 必须是不同条目（选项处理不同）")
	}
}

func TestZeroOperands(t *testing.T) {

	truth(t, statusFalse)

	out, errb, code := runBracket(t, "]")
	if code != statusFalse || out != "" || errb != "" {
		t.Errorf("[ ] = (%q, %q, %d), want (\"\", \"\", 1)", out, errb, code)
	}
}

func TestOneOperand(t *testing.T) {

	truth(t, statusTrue, "x")
	truth(t, statusTrue, "0")
	truth(t, statusTrue, "false")
	truth(t, statusTrue, "!")
	truth(t, statusTrue, "(")
	truth(t, statusTrue, "-e")
	truth(t, statusTrue, "-a")
	truth(t, statusTrue, " ")
	truth(t, statusFalse, "")
}

func TestTwoOperands(t *testing.T) {
	truth(t, statusTrue, "-n", "x")
	truth(t, statusFalse, "-n", "")
	truth(t, statusTrue, "-z", "")
	truth(t, statusFalse, "-z", "x")

	truth(t, statusFalse, "!", "x")
	truth(t, statusTrue, "!", "")

	truth(t, statusFalse, "!", "-e")
}

func TestThreeOperands(t *testing.T) {

	truth(t, statusTrue, "-e", "=", "-e")
	truth(t, statusFalse, "-e", "=", "-f")

	truth(t, statusTrue, "!", "-n", "")
	truth(t, statusFalse, "!", "-z", "")

	truth(t, statusTrue, "(", "x", ")")
	truth(t, statusFalse, "(", "", ")")

	truth(t, statusTrue, "x", "-a", "y")
	truth(t, statusFalse, "x", "-a", "")
	truth(t, statusTrue, "", "-o", "y")
	truth(t, statusFalse, "", "-o", "")
}

func TestFourOperands(t *testing.T) {

	truth(t, statusFalse, "!", "a", "=", "a")
	truth(t, statusTrue, "!", "a", "=", "b")
	truth(t, statusFalse, "!", "(", "x", ")")

	truth(t, statusTrue, "(", "-n", "x", ")")
	truth(t, statusFalse, "(", "-z", "x", ")")
	truth(t, statusTrue, "(", "!", "", ")")

	truth(t, statusTrue, "-n", "x", "-a", "y")
	truth(t, statusFalse, "-z", "x", "-o", "")
}

func TestOperatorPrecedence(t *testing.T) {

	truth(t, statusTrue, "", "-a", "x", "-o", "y")

	truth(t, statusTrue, "x", "-o", "", "-a", "y")

	truth(t, statusFalse, "", "-o", "x", "-a", "")

	truth(t, statusTrue, "a", "-a", "b", "-a", "c")
	truth(t, statusFalse, "a", "-a", "", "-a", "c")
	truth(t, statusFalse, "", "-o", "", "-o", "")
	truth(t, statusTrue, "", "-o", "", "-o", "c")
}

func TestNegationParity(t *testing.T) {
	truth(t, statusTrue, "!", "!", "x", "-a", "y")
	truth(t, statusFalse, "!", "!", "!", "x", "-a", "y")

	truth(t, statusFalse, "!", "x", "-a", "y")
	truth(t, statusTrue, "!", "", "-a", "y")

	truth(t, statusFalse, "!", "!")
	truth(t, statusTrue, "!", "!", "!")
	truth(t, statusFalse, "!", "!", "!", "!")
}

func TestParenGrouping(t *testing.T) {

	truth(t, statusFalse, "", "-a", "x", "-o", "")
	truth(t, statusTrue, "(", "", "-o", "x", ")", "-a", "y")
	truth(t, statusFalse, "(", "", "-o", "", ")", "-a", "y")

	truth(t, statusTrue, "(", "(", "x", ")", ")")
	truth(t, statusTrue, "!", "(", "", "-o", "", ")")
}

func TestNoShortCircuit(t *testing.T) {

	_, errb, code := runTest(t, "x", "-o", "-q", "y")
	if code != statusSyntax {
		t.Errorf("`x -o -q y` = %d, want 2", code)
	}
	if want := "test: '-q': unary operator expected\n"; errb != want {
		t.Errorf("stderr = %q, want %q", errb, want)
	}

	_, errb, code = runTest(t, "", "-a", "-q", "y")
	if code != statusSyntax || !strings.Contains(errb, "'-q'") {
		t.Errorf("`\"\" -a -q y` = %d, stderr %q", code, errb)
	}
}

func TestStringComparison(t *testing.T) {
	truth(t, statusTrue, "abc", "=", "abc")
	truth(t, statusFalse, "abc", "=", "abd")
	truth(t, statusTrue, "abc", "==", "abc")
	truth(t, statusFalse, "abc", "==", "abd")
	truth(t, statusTrue, "abc", "!=", "abd")
	truth(t, statusFalse, "abc", "!=", "abc")

	truth(t, statusTrue, "Z", "<", "a")
	truth(t, statusFalse, "a", "<", "Z")
	truth(t, statusTrue, "a", ">", "Z")
	truth(t, statusTrue, "ab", "<", "abc")
	truth(t, statusFalse, "abc", "<", "abc")
	truth(t, statusFalse, "abc", ">", "abc")

	truth(t, statusTrue, "", "=", "")
	truth(t, statusTrue, "", "<", "a")
}

func TestIntegerComparison(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"1", "-eq", "1"}, statusTrue},
		{[]string{"1", "-eq", "2"}, statusFalse},
		{[]string{"1", "-ne", "2"}, statusTrue},
		{[]string{"2", "-gt", "1"}, statusTrue},
		{[]string{"1", "-gt", "1"}, statusFalse},
		{[]string{"1", "-ge", "1"}, statusTrue},
		{[]string{"1", "-lt", "2"}, statusTrue},
		{[]string{"2", "-lt", "2"}, statusFalse},
		{[]string{"2", "-le", "2"}, statusTrue},

		{[]string{"10", "-gt", "9"}, statusTrue},

		{[]string{"-1", "-lt", "0"}, statusTrue},
		{[]string{"+1", "-eq", "1"}, statusTrue},
		{[]string{"007", "-eq", "7"}, statusTrue},
		{[]string{"-0", "-eq", "0"}, statusTrue},

		{[]string{" 3 ", "-eq", "3"}, statusTrue},
		{[]string{"\t3", "-eq", "3"}, statusTrue},

		{[]string{"99999999999999999999999", "-gt", "99999999999999999999998"}, statusTrue},
		{[]string{"-99999999999999999999999", "-lt", "0"}, statusTrue},
	}
	for _, c := range cases {
		truth(t, c.want, c.args...)
	}
}

func TestIntegerErrors(t *testing.T) {
	for _, args := range [][]string{
		{"x", "-eq", "1"},
		{"1", "-eq", "x"},
		{"", "-eq", "1"},
		{"1.5", "-eq", "1"},
		{"0x10", "-eq", "16"},
		{"1", "-eq", "1 2"},
		{"-", "-eq", "1"},
	} {
		_, errb, code := runTest(t, args...)
		if code != statusSyntax {
			t.Errorf("test %q = %d, want 2", args, code)
		}
		if !strings.HasPrefix(errb, "test: invalid integer ") {
			t.Errorf("test %q stderr = %q, want an invalid-integer diagnostic", args, errb)
		}
	}

	_, errb, _ := runTest(t, "1", "-eq", "x y")
	if want := "test: invalid integer 'x y'\n"; errb != want {
		t.Errorf("stderr = %q, want %q", errb, want)
	}
}

func TestFilePrimaries(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "file"), "hello")
	mustWrite(t, filepath.Join(dir, "empty"), "")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		args []string
		want int
	}{
		{[]string{"-e", "file"}, statusTrue},
		{[]string{"-e", "sub"}, statusTrue},
		{[]string{"-e", "missing"}, statusFalse},

		{[]string{"-a", "file"}, statusTrue},
		{[]string{"-a", "missing"}, statusFalse},
		{[]string{"-f", "file"}, statusTrue},
		{[]string{"-f", "sub"}, statusFalse},
		{[]string{"-f", "missing"}, statusFalse},
		{[]string{"-d", "sub"}, statusTrue},
		{[]string{"-d", "file"}, statusFalse},
		{[]string{"-d", "missing"}, statusFalse},
		{[]string{"-s", "file"}, statusTrue},
		{[]string{"-s", "empty"}, statusFalse},
		{[]string{"-s", "missing"}, statusFalse},

		{[]string{"-b", "file"}, statusFalse},
		{[]string{"-c", "file"}, statusFalse},
		{[]string{"-p", "file"}, statusFalse},
		{[]string{"-S", "file"}, statusFalse},
		{[]string{"-u", "file"}, statusFalse},
		{[]string{"-g", "file"}, statusFalse},
		{[]string{"-k", "file"}, statusFalse},
		{[]string{"-h", "file"}, statusFalse},
		{[]string{"-L", "file"}, statusFalse},
		{[]string{"-h", "missing"}, statusFalse},
	}
	for _, c := range cases {
		out, errb, code := runIn(t, cmd, dir, c.args...)
		if code != c.want || out != "" || errb != "" {
			t.Errorf("test %q = (%q, %q, %d), want code %d", c.args, out, errb, code, c.want)
		}
	}
}

func TestFileComparisonPrimaries(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "older")
	newer := filepath.Join(dir, "newer")
	mustWrite(t, older, "a")
	mustWrite(t, newer, "b")

	base := time.Now().Add(-time.Hour)
	chtimes(t, older, base, base)
	chtimes(t, newer, base.Add(time.Minute), base.Add(time.Minute))

	cases := []struct {
		args []string
		want int
	}{

		{[]string{"older", "-ef", "older"}, statusTrue},
		{[]string{"older", "-ef", "newer"}, statusFalse},
		{[]string{"older", "-ef", "missing"}, statusFalse},
		{[]string{"missing", "-ef", "missing"}, statusFalse},

		{[]string{"newer", "-nt", "older"}, statusTrue},
		{[]string{"older", "-nt", "newer"}, statusFalse},
		{[]string{"older", "-nt", "older"}, statusFalse},

		{[]string{"older", "-nt", "missing"}, statusTrue},
		{[]string{"missing", "-nt", "older"}, statusFalse},
		{[]string{"missing", "-nt", "missing"}, statusFalse},

		{[]string{"older", "-ot", "newer"}, statusTrue},
		{[]string{"newer", "-ot", "older"}, statusFalse},
		{[]string{"older", "-ot", "older"}, statusFalse},
		{[]string{"missing", "-ot", "older"}, statusTrue},
		{[]string{"older", "-ot", "missing"}, statusFalse},
		{[]string{"missing", "-ot", "missing"}, statusFalse},
	}
	for _, c := range cases {
		out, errb, code := runIn(t, cmd, dir, c.args...)
		if code != c.want || out != "" || errb != "" {
			t.Errorf("test %q = (%q, %q, %d), want code %d", c.args, out, errb, code, c.want)
		}
	}
}

func TestSymlinkPrimaries(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	mustWrite(t, target, "x")

	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("nothing", filepath.Join(dir, "dangling")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cases := []struct {
		args []string
		want int
	}{

		{[]string{"-h", "link"}, statusTrue},
		{[]string{"-L", "link"}, statusTrue},
		{[]string{"-h", "target"}, statusFalse},
		{[]string{"-h", "dangling"}, statusTrue},
		{[]string{"-L", "dangling"}, statusTrue},
		{[]string{"-e", "link"}, statusTrue},
		{[]string{"-f", "link"}, statusTrue},

		{[]string{"-e", "dangling"}, statusFalse},
		{[]string{"-f", "dangling"}, statusFalse},

		{[]string{"link", "-ef", "target"}, statusTrue},
	}
	for _, c := range cases {
		out, errb, code := runIn(t, cmd, dir, c.args...)
		if code != c.want || out != "" || errb != "" {
			t.Errorf("test %q = (%q, %q, %d), want code %d", c.args, out, errb, code, c.want)
		}
	}
}

func TestOperandsResolveAgainstRunContext(t *testing.T) {

	root := t.TempDir()
	here := filepath.Join(root, "here")
	elsewhere := filepath.Join(root, "elsewhere")
	for _, dir := range []string{here, elsewhere} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(here, "marker"), "x")

	run := func(dir string, args ...string) int {
		t.Helper()
		release := vfs.Bind(filesystem.NewFilesystem(root), dir, nil)
		defer release()
		var out, errb bytes.Buffer
		rc := &toolcompat.RunContext{
			Ctx:   context.Background(),
			Dir:   dir,
			Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: &out, Err: &errb},
		}
		return cmd.Run(rc, args)
	}

	if code := run(here, "-f", "marker"); code != statusTrue {
		t.Errorf("-f marker in its own dir = %d, want 0", code)
	}
	if code := run(elsewhere, "-f", "marker"); code != statusFalse {
		t.Errorf("-f marker in another dir = %d, want 1", code)
	}
	if code := run(elsewhere, "-f", filepath.Join(here, "marker")); code != statusTrue {
		t.Errorf("-f 绝对操作数（同一授予树内）= %d, want 0", code)
	}

	if code := run(elsewhere, "-f", filepath.Join(os.TempDir(), "outside-marker")); code != statusFalse {
		t.Error("-f 授予树之外的绝对路径必须不可达")
	}

	if code := run(elsewhere, "-f", "test.go"); code != statusFalse {
		t.Error("-f test.go resolved against the process cwd, not the command dir")
	}
}

func TestTerminalPrimary(t *testing.T) {

	truth(t, statusFalse, "-t", "-1")
	truth(t, statusFalse, "-t", "99999999999999999999999")
	truth(t, statusFalse, "-t", "0")
	truth(t, statusFalse, "-t", "1")
	truth(t, statusFalse, "-t", "2")
	truth(t, statusFalse, "-t", "3")

	_, errb2, code := runTest(t, "-t", "tty")
	if code != statusSyntax || errb2 != "test: invalid integer 'tty'\n" {
		t.Errorf("-t tty = (%q, %d), want an invalid-integer error", errb2, code)
	}
}

func TestSyntaxErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{

		{[]string{"-q", "x"}, "test: '-q': unary operator expected\n"},
		{[]string{"x", "y"}, "test: 'x': unary operator expected\n"},
		{[]string{"x", "-a"}, "test: 'x': unary operator expected\n"},
		{[]string{"--help", "x"}, "test: '--help': unary operator expected\n"},

		{[]string{"x", "y", "z"}, "test: 'y': binary operator expected\n"},
		{[]string{"-n", "x", "-a"}, "test: 'x': binary operator expected\n"},

		{[]string{"x", "y", "z", "w", "v"}, "test: extra argument 'y'\n"},

		{[]string{"(", "-n", "x", "-a", "y"}, "test: ')' expected\n"},
		{[]string{"(", "x", "-o", "y"}, "test: ')' expected\n"},

		{[]string{"a", "-a", "b", "-a", "c", "-a"}, "test: missing argument after '-a'\n"},
		{[]string{"a", "-o", "b", "-o", "c", "-o"}, "test: missing argument after '-o'\n"},

		{[]string{"x", "-a", "y", "-a", "-n"}, "test: missing argument after '-n'\n"},

		{[]string{"-o", "name"}, "test: '-o': unary operator expected\n"},

		{[]string{"-l", "abc", "-eq", "3"}, "test: '-l': unary operator expected\n"},
	}
	for _, c := range cases {
		out, errb, code := runTest(t, c.args...)
		if code != statusSyntax {
			t.Errorf("test %q = %d, want 2", c.args, code)
		}
		if errb != c.want {
			t.Errorf("test %q stderr = %q, want %q", c.args, errb, c.want)
		}
		if out != "" {
			t.Errorf("test %q wrote %q to stdout", c.args, out)
		}
	}
}

func TestDiagnosticsAreDeterministic(t *testing.T) {
	args := []string{"x", "y", "z"}
	_, first, _ := runTest(t, args...)
	for range 3 {
		if _, again, _ := runTest(t, args...); again != first {
			t.Fatalf("diagnostic varied between runs: %q vs %q", first, again)
		}
	}

	_, errb, code := runBracket(t, "x", "y", "z", "]")
	if code != statusSyntax || errb != "[: 'y': binary operator expected\n" {
		t.Errorf("[ x y z ] = (%q, %d)", errb, code)
	}
}

func TestBracketRequiresClosingBracket(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"x"},
		{"-n", "x"},
		{"x", "]", "y"},
		{"]", "x"},
	} {
		out, errb, code := runBracket(t, args...)
		if code != statusSyntax {
			t.Errorf("[ %q = %d, want 2", args, code)
		}
		if want := "[: missing ']'\n"; errb != want {
			t.Errorf("[ %q stderr = %q, want %q", args, errb, want)
		}
		if out != "" {
			t.Errorf("[ %q wrote %q to stdout", args, out)
		}
	}
}

func TestBracketEvaluatesLikeTest(t *testing.T) {

	cases := []struct {
		args []string
		want int
	}{
		{[]string{"]"}, statusFalse},
		{[]string{"x", "]"}, statusTrue},
		{[]string{"", "]"}, statusFalse},
		{[]string{"-n", "x", "]"}, statusTrue},
		{[]string{"a", "=", "a", "]"}, statusTrue},
		{[]string{"!", "a", "=", "a", "]"}, statusFalse},
		{[]string{"1", "-lt", "2", "-a", "3", "-lt", "4", "]"}, statusTrue},

		{[]string{"]", "=", "]", "]"}, statusTrue},
	}
	for _, c := range cases {
		out, errb, code := runBracket(t, c.args...)
		if code != c.want || out != "" || errb != "" {
			t.Errorf("[ %q = (%q, %q, %d), want code %d", c.args, out, errb, code, c.want)
		}
	}
}

func TestOptionHandling(t *testing.T) {

	for _, arg := range []string{"--help", "--version", "--he"} {
		out, errb, code := runTest(t, arg)
		if code != statusTrue || out != "" || errb != "" {
			t.Errorf("test %s = (%q, %q, %d), want a true string test", arg, out, errb, code)
		}
	}

	out, errb, code := runBracket(t, "--help")
	if code != statusTrue || errb != "" {
		t.Errorf("[ --help = (%q, %d), want help on stdout", errb, code)
	}
	for _, want := range []string{"Usage: [ EXPRESSION ]", "-z STRING", "INTEGER1 -eq INTEGER2", "-ef FILE2"} {
		if !strings.Contains(out, want) {
			t.Errorf("[ --help output is missing %q", want)
		}
	}

	out, errb, code = runBracket(t, "--version")
	if code != statusTrue || errb != "" {
		t.Errorf("[ --version = (%q, %d)", errb, code)
	}
	if !strings.HasPrefix(out, "[ (assistant-sandbox) ") {
		t.Errorf("[ --version = %q", out)
	}

	for _, arg := range []string{"--help", "--version"} {
		out, errb, code := runBracket(t, arg, "]")
		if code != statusTrue || out != "" || errb != "" {
			t.Errorf("[ %s ] = (%q, %q, %d), want a true string test", arg, out, errb, code)
		}
	}

	out, errb, code = runBracket(t, "--he")
	if code != statusSyntax || out != "" || errb != "[: missing ']'\n" {
		t.Errorf("[ --he = (%q, %q, %d), want a missing-']' error", out, errb, code)
	}

	_, errb, code = runBracket(t, "--help", "x")
	if code != statusSyntax || errb != "[: missing ']'\n" {
		t.Errorf("[ --help x = (%q, %d), want a missing-']' error", errb, code)
	}
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func chtimes(t *testing.T, path string, atime, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, atime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionPrimaries(t *testing.T) {
	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{
		"rw":   0o600,
		"ro":   0o400,
		"wo":   0o200,
		"x":    0o100,
		"none": 0o000,
	} {
		mustWrite(t, filepath.Join(dir, name), "x")
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"-r", "rw"}, statusTrue},
		{[]string{"-w", "rw"}, statusTrue},
		{[]string{"-x", "rw"}, statusFalse},
		{[]string{"-r", "ro"}, statusTrue},
		{[]string{"-w", "ro"}, statusFalse},
		{[]string{"-x", "ro"}, statusFalse},
		{[]string{"-r", "wo"}, statusFalse},
		{[]string{"-w", "wo"}, statusTrue},
		{[]string{"-x", "x"}, statusTrue},
		{[]string{"-r", "none"}, statusFalse},
		{[]string{"-w", "none"}, statusFalse},
		{[]string{"-x", "none"}, statusFalse},

		{[]string{"-r", "missing"}, statusFalse},
		{[]string{"-w", "missing"}, statusFalse},
		{[]string{"-x", "missing"}, statusFalse},
	}
	for _, c := range cases {
		out, errb, code := runIn(t, cmd, dir, c.args...)
		if code != c.want || out != "" || errb != "" {
			t.Errorf("test %q = (%q, %q, %d), want code %d", c.args, out, errb, code, c.want)
		}
	}
}

func TestUnsupportedPrimariesFailLoudly(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f"), "x")
	for _, args := range [][]string{
		{"-O", "f"}, {"-G", "f"}, {"-N", "f"},
	} {
		out, errb, code := runIn(t, cmd, dir, args...)
		if code != statusSyntax || errb == "" || out != "" {
			t.Errorf("test %q = (%q, %q, %d), want 退出码 2 与诊断", args, out, errb, code)
		}
	}

	if _, errb, code := runIn(t, cmd, dir, "-N", "missing"); code != statusSyntax || errb == "" {
		t.Errorf("test -N missing = (%q, %d), want 明确失败", errb, code)
	}
}
