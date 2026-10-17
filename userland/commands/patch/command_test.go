package patchcmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "apple\nbanana\ncherry\n")
	diff := "--- f.txt\n+++ f.txt\n@@ -1,3 +1,3 @@\n apple\n-banana\n+berry\n cherry\n"
	result := commandtest.Run(t, Run, dir, []string{"f.txt"}, diff)
	if result.Code != 0 || result.Stderr == "" {
		t.Fatalf("就地打补丁得到 %+v", result)
	}
	if got := readFile(t, dir, "f.txt"); got != "apple\nberry\ncherry\n" {
		t.Fatalf("补丁结果 %q", got)
	}

	result = commandtest.Run(t, Run, dir, nil, "--- /dev/null\n+++ created.txt\n@@ -0,0 +1,2 @@\n+hello\n+world\n")
	if result.Code != 0 {
		t.Fatalf("创建文件得到 %+v", result)
	}
	if got := readFile(t, dir, "created.txt"); got != "hello\nworld\n" {
		t.Fatalf("创建结果 %q", got)
	}

	result = commandtest.Run(t, Run, dir, nil, "1c1\n< one\n---\n> two\n")
	if result.Code == 0 || !strings.Contains(result.Stderr, "cannot determine file to patch") {
		t.Fatalf("无终端提问得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"--not-a-flag"}, ""); result.Code != 2 {
		t.Fatalf("未知参数得到 %+v", result)
	}

	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}

func TestApplyFromGuestVFSOperands(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "one\n")
	writeFile(t, dir, "p.diff", "--- f.txt\n+++ f.txt\n@@ -1 +1 @@\n-one\n+two\n")
	result := commandtest.Run(t, Run, dir, []string{"f.txt", "p.diff"}, "")
	if result.Code != 0 {
		t.Fatalf("操作数形式得到 %+v", result)
	}
	if got := readFile(t, dir, "f.txt"); got != "two\n" {
		t.Fatalf("补丁结果 %q", got)
	}

	result = commandtest.Run(t, Run, dir, []string{filepath.Join(dir, "f.txt"), filepath.Join(dir, "p.diff")}, "")
	if result.Code == 0 {
		t.Fatalf("已应用的补丁应报告冲突: %+v", result)
	}
}

func TestDefaultReaderPromptHasNoTerminal(t *testing.T) {
	if _, err := readPromptLine(); err == nil {
		t.Fatal("客体没有 /dev/tty，readPromptLine 必须报错")
	}
	var errb strings.Builder
	rc := &toolcompat.RunContext{
		Ctx: t.Context(), Dir: t.TempDir(),
		Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: io.Discard, Err: &errb},
	}
	if _, err := askPrompt(rc, "Assume -R? [n] "); err == nil {
		t.Fatal("客体里 askPrompt 必须报错")
	}
	if !strings.Contains(errb.String(), "Assume -R?") {
		t.Fatalf("提问应写到 stderr，得到 %q", errb.String())
	}
}

func TestOperationalErrorPaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stdin   string
		args    []string
		prepare func(t *testing.T, dir string)
		want    int
		contain string
	}{
		{name: "missing-patchfile", args: []string{"-i", "absent.diff"}, want: 2, contain: "absent.diff"},
		{name: "malformed-patch", stdin: "this is not a patch\n", want: 2, contain: "patch"},
		{

			name: "missing-operand-target", stdin: "--- f.txt\n+++ f.txt\n@@ -1 +1 @@\n-old\n+new\n",
			args: []string{"f.txt"}, want: 2, contain: "can't find file to patch",
		},
		{

			name: "directory-target", stdin: "--- /dev/null\n+++ sub\n@@ -0,0 +1 @@\n+x\n",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: 2, contain: "can't patch directory",
		},
		{
			name: "force-missing-target", stdin: "1c1\n< one\n---\n> two\n", args: []string{"-f"},
			want: 2, contain: "can't find file to patch",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.prepare != nil {
				tc.prepare(t, dir)
			}
			result := commandtest.Run(t, Run, dir, tc.args, tc.stdin)
			if result.Code != tc.want || !strings.Contains(result.Stderr, tc.contain) {
				t.Fatalf("得到 (%q, %q, %d), want code %d 含 %q", result.Stdout, result.Stderr, result.Code, tc.want, tc.contain)
			}
		})
	}
}
