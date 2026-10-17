package nlcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	content := "a\n\nb\n"
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args  []string
		input string
		want  string
		code  int
	}{
		{[]string{"data"}, "", "     1\ta\n       \n     2\tb\n", 0},
		{[]string{"-ba", "data"}, "", "     1\ta\n     2\t\n     3\tb\n", 0},
		{[]string{"-bn", "data"}, "", "       a\n       \n       b\n", 0},
		{[]string{"-w", "3", "-s", ":", "data"}, "", "  1:a\n    \n  2:b\n", 0},
		{[]string{"-n", "rz", "data"}, "", "000001\ta\n       \n000002\tb\n", 0},
		{[]string{"-n", "ln", "-w", "2", "data"}, "", "1 \ta\n   \n2 \tb\n", 0},
		{[]string{"-v", "5", "-i", "2", "data"}, "", "     5\ta\n       \n     7\tb\n", 0},
		{nil, "x\ny\n", "     1\tx\n     2\ty\n", 0},
		{[]string{"absent"}, "", "", 1},
		{[]string{"-b", "q"}, "", "", 2},
		{[]string{"-n", "zz"}, "", "", 2},
		{[]string{"--bad"}, "", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code {
			t.Fatalf("args=%v 得到 %+v，want code=%d", test.args, result, test.code)
		}
		if test.code == 0 && result.Stdout != test.want {
			t.Fatalf("args=%v 得到 stdout=%q，want %q", test.args, result.Stdout, test.want)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 {
		t.Fatalf("--help 得到 %+v", result)
	}
}

func TestRegexpStyleAndJoin(t *testing.T) {
	dir := t.TempDir()
	content := "A\n\nB\n"
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-b", "p[A-Z]", "data"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "1\tA") {
		t.Fatalf("p<RE> 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-b", "p[", "data"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("非法正则得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-l", "2", "data"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "1\tA") {
		t.Fatalf("-l 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-h", "n", "-f", "n", "-b", "n", "data"}, "")
	if result.Code != 0 {
		t.Fatalf("全 n 样式得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-d", "::", "data"}, "")
	if result.Code != 0 {
		t.Fatalf("-d 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-p", "data"}, "")
	if result.Code != 0 {
		t.Fatalf("-p 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s", "", "data"}, "")
	if result.Code != 0 {
		t.Fatalf("空分隔符得到 %+v", result)
	}
}
