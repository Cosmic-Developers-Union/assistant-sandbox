package grep

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractMatching(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"one":           "alpha\nbeta\n",
		"two":           "gamma\n",
		"sub/child":     "alpha\n",
		"literal":       "a+b\n",
		"digits":        "a1b2\n",
		"class-subject": "+\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
		code  int
	}{
		{"number", []string{"-n", "beta"}, "alpha\nbeta\n", "2:beta\n", 0},
		{"invert", []string{"-v", "alpha"}, "alpha\nbeta\n", "beta\n", 0},
		{"quiet", []string{"-q", "alpha"}, "alpha\n", "", 0},
		{"fixed", []string{"-F", "a+b"}, "a+b\n", "a+b\n", 0},
		{"bre-literal", []string{"a+b"}, "a+b\naab\n", "a+b\n", 0},
		{"bre-class", []string{"[a+]"}, "+\nq\n", "+\n", 0},
		{"extended", []string{"-E", "a+"}, "aaa\nb\n", "aaa\n", 0},
		{"ignore-case", []string{"-i", "ALPHA"}, "alpha\n", "alpha\n", 0},
		{"count", []string{"-c", "alpha"}, "alpha\nbeta\nalpha\n", "2\n", 0},
		{"only", []string{"-o", "[0-9]"}, "a1b2\n", "1\n2\n", 0},
		{"multi-file", []string{"alpha", "one", "two"}, "", "one:alpha\n", 0},
		{"no-match", []string{"absent-pattern"}, "alpha\n", "", 1},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("%s: %+v，want code=%d stdout=%q", test.name, result, test.code, test.want)
		}
	}
}

func TestRecursivePathPrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "child"), []byte("alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-r", "alpha", "sub"}, "")
	if result.Code != 0 || !strings.HasSuffix(result.Stdout, "sub/child:alpha\n") {
		t.Fatalf("得到 %+v", result)
	}
}

func TestContractErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{}, {"["}, {"alpha", "absent"}, {"-r", "alpha", "absent"}, {"--bad"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestRecursiveEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-r", "alpha", "empty"}, ""); result.Code != 1 {
		t.Fatalf("得到 %+v", result)
	}
	result := commandtest.Run(t, Run, dir, []string{"-r", "alpha", "empty", "empty"}, "")
	if result.Code != 1 {
		t.Fatalf("多目录得到 %+v", result)
	}
}

func TestRecursiveFileLimit(t *testing.T) {
	dir := t.TempDir()
	for index := 0; index < 20; index++ {
		name := filepath.Join(dir, "f"+string(rune('a'+index)))
		if err := os.WriteFile(name, []byte("x\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if result := commandtest.Run(t, Run, dir, []string{"-r", "x", "."}, ""); result.Code != 0 {
		t.Fatalf("得到 %+v", result)
	}
}

func TestDownstreamFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"alpha"}, strings.NewReader("alpha\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
	err = commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-c", "alpha"}, strings.NewReader("alpha\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("计数模式输出失败被忽略")
	}
}
