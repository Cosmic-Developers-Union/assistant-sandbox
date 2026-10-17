package grep

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args        []string
		input, want string
		code        int
	}{
		{[]string{"-n", "alpha"}, "beta\nalpha\n", "2:alpha\n", 0},
		{[]string{"-i", "ALPHA"}, "alpha\n", "alpha\n", 0},
		{[]string{"-v", "alpha"}, "beta\nalpha\n", "beta\n", 0},
		{[]string{"-q", "alpha"}, "alpha\n", "", 0},
		{[]string{"none"}, "alpha\n", "", 1},
		{[]string{"-F", "["}, "[\n", "[\n", 0},
		{[]string{"-En", "^a"}, "alpha\n", "1:alpha\n", 0},
		{[]string{"["}, "", "", 2},
		{[]string{}, "", "", 2},
		{[]string{"--bad"}, "", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v result=%+v want=%q code=%d", test.args, result, test.want, test.code)
		}
	}
}

func TestDownstreamOutputFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"a"}, strings.NewReader("a:1\nb:2\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}

func TestRecursiveFilesAndEarlyQuietExit(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "value"), []byte("needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := commandtest.Run(t, Run, dir, []string{"-r", "needle", "sub"}, "")
	if r.Code != 0 || !strings.Contains(r.Stdout, "value:needle") {
		t.Fatal(r)
	}
	r = commandtest.Run(t, Run, dir, []string{"-q", "needle", "sub/value", "missing"}, "")
	if r.Code != 0 {
		t.Fatal("找到匹配后继续访问缺失文件", r)
	}
	r = commandtest.Run(t, Run, dir, []string{"needle", "sub/value", "sub/value"}, "")
	if r.Code != 0 || strings.Count(r.Stdout, "sub/value:needle") != 2 {
		t.Fatal(r)
	}
	r = commandtest.Run(t, Run, dir, []string{"-r", "needle", "-"}, "needle")
	if r.Code != 0 || r.Stdout != "-:needle\n" {
		t.Fatal(r)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	r = commandtest.Run(t, Run, dir, []string{"-r", "needle", "empty"}, "needle")
	if r.Code != 1 {
		t.Fatal("空目录误读 stdin", r)
	}
	r = commandtest.Run(t, Run, dir, []string{"-r", "needle", "missing"}, "")
	if r.Code != 2 {
		t.Fatal(r)
	}
}
func TestRecursiveBudgetAndCancellation(t *testing.T) {
	dir := t.TempDir()
	cancelled := func(ctx context.Context, args []string) error {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return Run(ctx, args)
	}
	r := commandtest.Run(t, cancelled, dir, []string{"-r", "needle", "."}, "")
	if r.Code != 2 {
		t.Fatal(r)
	}
	for i := range 4097 {
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	r = commandtest.Run(t, Run, dir, []string{"-r", "needle", "."}, "")
	if r.Code != 2 || !strings.Contains(r.Stderr, "4096") {
		t.Fatal(r)
	}
}

func TestPatternSources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("alpha\nbeta\ngamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
		code int
	}{
		{"-e 单模式", []string{"-e", "alpha", "data"}, "alpha\n", 0},
		{"-e 多模式", []string{"-e", "alpha", "-e", "gamma", "data"}, "alpha\ngamma\n", 0},
		{"-e 空模式匹配所有行", []string{"-e", "", "data"}, "alpha\nbeta\ngamma\n", 0},
		{"-e 与 -i", []string{"-i", "-e", "ALPHA", "data"}, "alpha\n", 0},
		{"-e 与 -F", []string{"-F", "-e", "a.p", "data"}, "", 1},
		{"-e 与 -x", []string{"-x", "-e", "beta", "data"}, "beta\n", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, dir, test.args, "")
			if result.Code != test.code || result.Stdout != test.want {
				t.Fatalf("%v 得到 code=%d stdout=%q stderr=%q", test.args, result.Code, result.Stdout, result.Stderr)
			}
		})
	}

	if err := os.WriteFile(filepath.Join(dir, "pats"), []byte("alpha\ngamma\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-f", "pats", "data"}, "")
	if result.Code != 0 || result.Stdout != "alpha\ngamma\n" {
		t.Fatalf("-f 得到 %+v", result)
	}

	if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-f", "empty", "data"}, ""); result.Code != 1 || result.Stdout != "" {
		t.Fatalf("空模式文件得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-f", "absent", "data"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("缺失模式文件得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s", "-f", "absent", "data"}, "")
	if result.Code != 2 || result.Stderr != "" {
		t.Fatalf("-s 缺失模式文件得到 %+v", result)
	}

	if err := os.WriteFile(filepath.Join(dir, "blank"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-f", "blank", "data"}, ""); result.Code != 0 {
		t.Fatalf("空行模式得到 %+v", result)
	}
}

func TestFilesWithMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hit"), []byte("alpha\nalpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "miss"), []byte("beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-l", "alpha", "hit", "miss"}, "")
	if result.Code != 0 || result.Stdout != "hit\n" {
		t.Fatalf("-l 得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-l", "gamma", "hit", "miss"}, ""); result.Code != 1 {
		t.Fatalf("-l 无命中得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-l", "-c", "alpha", "hit"}, ""); result.Stdout != "hit\n" {
		t.Fatalf("-l -c 得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-q", "-l", "alpha", "hit", "absent"}, ""); result.Code != 0 || result.Stdout != "" || result.Stderr != "" {
		t.Fatalf("-q -l 得到 %+v", result)
	}
}

func TestFileErrorsDoNotAbortRemainingOperands(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"alpha", "absent", "good"}, "")
	if result.Code != 2 || !strings.Contains(result.Stdout, "alpha") || result.Stderr == "" {
		t.Fatalf("缺文件时得到 %+v", result)
	}
	if !strings.Contains(result.Stderr, "absent") {
		t.Fatalf("诊断里应点名缺失文件: %q", result.Stderr)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s", "alpha", "absent", "good"}, "")
	if result.Code != 2 || result.Stderr != "" || !strings.Contains(result.Stdout, "alpha") {
		t.Fatalf("-s 时得到 %+v", result)
	}
}

func TestWholeLineMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("beta\nbetamax\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-x", "beta", "data"}, "")
	if result.Code != 0 || result.Stdout != "beta\n" {
		t.Fatalf("-x 得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"beta", "data"}, ""); result.Stdout != "beta\nbetamax\n" {
		t.Fatalf("不加 -x 应做子串匹配: %+v", result)
	}
}
