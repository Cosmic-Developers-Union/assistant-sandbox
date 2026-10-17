package readlink

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{"link"}, "target\n", 0},
		{[]string{"-n", "link"}, "target", 0},
		{[]string{"link", "link"}, "target\ntarget\n", 0},
		{[]string{"plain"}, "", 2},
		{[]string{}, "", 2},
		{[]string{"--bad", "link"}, "", 2},
		{[]string{"--help"}, "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want code=%d stdout=%q", test.args, result, test.code, test.want)
		}
	}

	resolved := commandtest.Run(t, Run, dir, []string{"-f", "link"}, "")
	if resolved.Code != 0 || resolved.Stdout != filepath.Join(dir, "target")+"\n" {
		t.Fatalf("-f link 得到 %+v", resolved)
	}
}

func TestCanonicalizeFollowsLinks(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b", "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../b", filepath.Join(dir, "a", "link")); err != nil {
		t.Skip("该平台不可创建符号链接")
	}
	if err := os.Symlink("nowhere", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("a/link/file", filepath.Join(dir, "chain")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want string
		code int
	}{
		{"跟随目录链接", []string{"-f", "a/link/file"}, filepath.Join(dir, "b", "file") + "\n", 0},
		{"多级链接", []string{"-f", "chain"}, filepath.Join(dir, "b", "file") + "\n", 0},
		{"-f 允许最后一个组件不存在", []string{"-f", "b/missing"}, filepath.Join(dir, "b", "missing") + "\n", 0},
		{"-e 要求全部存在", []string{"-e", "b/missing"}, "", 1},
		{"-e 存在时可解析", []string{"-e", "a/link/file"}, filepath.Join(dir, "b", "file") + "\n", 0},
		{"-m 不要求存在", []string{"-m", "b/missing/deeper"}, filepath.Join(dir, "b", "missing", "deeper") + "\n", 0},
		{"悬空链接 -f 失败（中间组件）", []string{"-f", "dangling/x"}, "", 1},
		{"普通文件也算规范路径", []string{"-f", "b/file"}, filepath.Join(dir, "b", "file") + "\n", 0},
		{"-n 与规范化共存", []string{"-n", "-f", "b/file"}, filepath.Join(dir, "b", "file"), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, dir, test.args, "")
			if result.Code != test.code || result.Stdout != test.want {
				t.Fatalf("%v 得到 code=%d stdout=%q stderr=%q", test.args, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestCanonicalizeRejectsLinkLoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("loop", filepath.Join(dir, "loop")); err != nil {
		t.Skip("该平台不可创建符号链接")
	}
	if result := commandtest.Run(t, Run, dir, []string{"-e", "loop"}, ""); result.Code != 1 {
		t.Fatalf("自指链接应失败: %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-m", "loop"}, ""); result.Code != 1 {
		t.Fatalf("-m 自指链接应失败: %+v", result)
	}
}
