package cat

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractOptions(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"data":  "alpha\n",
		"blank": "a\n\n\nb\n",
		"tabs":  "a\tb\n",
		"bare":  "no-newline",
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
	}{
		{"stdin", nil, "alpha\n", "alpha\n"},
		{"file-and-stdin", []string{"data", "-"}, "beta", "alpha\nbeta"},
		{"number", []string{"-n", "blank"}, "", "     1\ta\n     2\t\n     3\t\n     4\tb\n"},
		{"number-nonblank", []string{"-b", "blank"}, "", "     1\ta\n\n\n     2\tb\n"},
		{"squeeze", []string{"-s", "blank"}, "", "a\n\nb\n"},
		{"show-ends", []string{"-E", "tabs"}, "", "a\tb$\n"},
		{"show-tabs", []string{"-T", "tabs"}, "", "a^Ib\n"},
		{"show-all", []string{"-A", "tabs"}, "", "a^Ib$\n"},
		{"bare-number", []string{"-n", "bare"}, "", "     1\tno-newline"},
		{"combined", []string{"-nsE", "blank"}, "", "     1\ta$\n     2\t$\n     3\tb$\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("%s: %+v，want %q", test.name, result, test.want)
		}
	}
}

func TestContractErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"absent"}, {"-n", "absent"}, {"-E", "folder"}, {"-c"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestNumberedDownstreamFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-n"}, strings.NewReader("a\nb\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
