package ls

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractSelection(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, age := range map[string]time.Time{"b": base, "a": base.Add(time.Minute)} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, age, age); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("h"), 0600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Chtimes(sub, base.Add(2*time.Minute), base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"sorted", nil, "a\nb\nsub\n"},

		{"all", []string{"-a"}, ".\n.hidden\na\nb\nsub\n"},
		{"almost-all", []string{"-A"}, ".hidden\na\nb\nsub\n"},
		{"reverse", []string{"-r"}, "sub\nb\na\n"},

		{"time", []string{"-t"}, "sub\na\nb\n"},
		{"file-operand", []string{"a"}, "a\n"},

		{"two-names", []string{"sub", "a"}, "a\n\nsub:\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("%s: %+v，want %q", test.name, result, test.want)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"-l"}, "")
	if result.Code != 0 || !strings.HasPrefix(result.Stdout, "-") || !strings.Contains(result.Stdout, " a\n") {
		t.Fatalf("-l: %+v", result)
	}
}

func TestContractErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"absent"}, {"-l", "absent"}, {"--bad"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestDownstreamFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	err := commandtest.Invoke(t.Context(), Run, dir, []string{"a"}, strings.NewReader(""), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
