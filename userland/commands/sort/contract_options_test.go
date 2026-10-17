package sort

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestContractOrdering(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rows"), []byte("10\n9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
	}{
		{[]string{"-r"}, "a\nb\n", "b\na\n"},
		{[]string{"-u"}, "a\na\nb\n", "a\nb\n"},
		{[]string{"-n"}, "10\n9\n", "9\n10\n"},
		{[]string{"-n", "rows"}, "", "9\n10\n"},
		{[]string{"-k", "2"}, "b 1\na 2\n", "b 1\na 2\n"},
		{[]string{"-k", "1,3"}, "a b\nc\n", "a b\nc\n"},
		{[]string{"-k", "3"}, "a b\nc\n", "a b\nc\n"},
		{[]string{"-t", ":", "-k", "2"}, "x:2\ny:1\n", "y:1\nx:2\n"},
		{[]string{"-t", ":", "-k", "1,5"}, "a:b\nc\n", "a:b\nc\n"},
		{[]string{"-r", "-u", "-n"}, "2\n1\n1\n", "2\n1\n"},
		{nil, "", ""},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
}

func TestContractKeyErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"-k", "0"}, {"-k", "x"}, {"-k", "2,1"}, {"-k", "1,x"},
		{"--bad"}, {"absent"},
	} {
		result := commandtest.Run(t, Run, dir, args, "a\n")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
}

func TestDownstreamFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"-n"}, strings.NewReader("2\n1\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
