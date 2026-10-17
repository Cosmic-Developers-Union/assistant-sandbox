package cut

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractFields(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"rows": "a:b:c\nplain\n",
		"tabs": "a\tb\tc\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-d", ":", "-f", "1"}, "a:b:c\n", "a\n"},

		{[]string{"-d", ":", "-f", "3,1"}, "a:b:c\n", "a:c\n"},
		{[]string{"-d", ":", "-f", "1-2"}, "a:b:c\n", "a:b\n"},
		{[]string{"-d", ":", "-f", "2-"}, "a:b:c\n", "b:c\n"},
		{[]string{"-d", ":", "-f", "9"}, "a:b:c\n", "\n"},
		{[]string{"-d", ":", "-f", "1"}, "plain\n", "plain\n"},
		{[]string{"-f", "1"}, "a\tb\tc\n", "a\n"},
		{[]string{"-d", ":", "-f", "1", "rows"}, "", "a\nplain\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
}

func TestContractParseErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"-d", "::", "-f", "1"},
		{"-d", ":"},
		{"-d", ":", "-f", ","},
		{"-d", ":", "-f", "-3"},
		{"-d", ":", "-f", "0"},
		{"-d", ":", "-f", "abc"},
		{"-d", ":", "-f", "3-1"},
		{"-d", ":", "-f", "1-x"},
		{"-d", ":", "-f", "1-1048576,1-2"},
		{"-d", ":", "-f", "1", "absent"},
		{"--bad"},
	} {
		result := commandtest.Run(t, Run, dir, args, "a:b\n")
		if result.Code != 2 || result.Stderr == "" || result.Stdout != "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestDownstreamFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-d", ":", "-f", "1"}, strings.NewReader("a:b\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
