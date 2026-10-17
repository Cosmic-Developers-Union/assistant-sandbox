package ex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func TestEditingPipeline(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-s"}, "a\nhello\n.\nw out\n1s/hello/hi/\nw out\nq\n")
	if result.Code != 0 {
		t.Fatalf("会话: %+v", result)
	}

	if result.Stdout != "" {
		t.Fatalf("stdout: %q", result.Stdout)
	}
	if body, _ := read(t, dir, "out"); body != "hi\n" {
		t.Fatalf("out: %q", body)
	}
}

func TestPrompt(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, nil, "a\nx\n.\nq!\n")
	if result.Code != 0 || result.Stdout != "::" {
		t.Fatalf("提示符: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s"}, "a\nx\n.\nq!\n")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("-s 无提示: %+v", result)
	}
}

func TestSetOptions(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-s"},
		"set number\na\nhi\n.\n1p\nset nonu\n1p\nset nu?\nq!\n")
	if result.Code != 0 {
		t.Fatalf("set: %+v", result)
	}

	if result.Stdout != "number\n1\thi\nnonumber\nhi\nnonumber\n" {
		t.Fatalf("set 输出: %q", result.Stdout)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s"}, "set list\na\nx\n.\n1p\nset nolist\nset\nq!\n")
	if result.Code != 0 {
		t.Fatalf("list: %+v", result)
	}

	if result.Stdout != "list\nx$\nnolist\n" {
		t.Fatalf("list 输出: %q", result.Stdout)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s"}, "set autoindent\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "不支持的 set 选项: autoindent") {
		t.Fatalf("set 拒绝: %+v", result)
	}
}

func TestExCommands(t *testing.T) {
	dir := t.TempDir()

	result := commandtest.Run(t, Run, dir, []string{"-s"}, "a\nhello\n.\nwq out\n")
	if result.Code != 0 {
		t.Fatalf("wq: %+v", result)
	}
	if body, _ := read(t, dir, "out"); body != "hello\n" {
		t.Fatalf("wq out: %q", body)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s"}, "a\nx\n.\nq!\n")
	if result.Code != 0 {
		t.Fatalf("q!: %+v", result)
	}

	write(t, dir, "g", "one\ntwo\n")
	result = commandtest.Run(t, Run, dir, []string{"-s", "g"}, "$=\nfile\nargs\nq\n")
	if result.Code != 0 {
		t.Fatalf("状态命令: %+v", result)
	}
	if result.Stdout != "2\n\"g\" line 2 of 2 --50%--\n[g]\n" {
		t.Fatalf("状态输出: %q", result.Stdout)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s", "g"}, "next\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "没有更多要编辑的文件") {
		t.Fatalf("next: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s", "g"}, "rewind\n1p\nq\n")
	if result.Code != 0 || result.Stdout != "one\n" {
		t.Fatalf("rewind: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s"}, "visual\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "屏幕") {
		t.Fatalf("visual: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s"}, "vi\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "屏幕") {
		t.Fatalf("vi: %+v", result)
	}
}

func TestStartup(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "g", "one\n")

	result := commandtest.Run(t, Run, dir, []string{"-s", "-c", "1p", "g"}, "q\n")
	if result.Code != 0 || result.Stdout != "one\n" {
		t.Fatalf("-c: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s", "nope"}, "a\nhi\n.\nw\nq\n")
	if result.Code != 0 || result.Stderr == "" {
		t.Fatalf("缺失文件: %+v", result)
	}
	if body, _ := read(t, dir, "nope"); body != "hi\n" {
		t.Fatalf("nope: %q", body)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s"}, "a\nx\n.\n")
	if result.Code != 0 || result.Stderr == "" {
		t.Fatalf("EOF: %+v", result)
	}
}

func TestRejectedOptions(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"-v"}, {"-r"}, {"-R"}, {"-w", "10"}, {"-v", "g"},
	} {
		result := commandtest.Run(t, Run, dir, args, "q\n")
		if result.Code != 1 || result.Stderr == "" {
			t.Fatalf("选项 %v: %+v", args, result)
		}
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-s"}, "Y\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "未知命令") {
		t.Fatalf("未知命令: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"a", "b"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("操作数: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"--bogus"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("选项: %+v", result)
	}
}

func TestFileStatusVariants(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "g", "one\n")

	result := commandtest.Run(t, Run, dir, []string{"-s", "g"}, "a\nx\n.\nfile\nq!\n")
	if result.Code != 0 || result.Stdout != "\"g\" [modified] line 2 of 2 --50%--\n" {
		t.Fatalf("modified: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s", "g"}, "file other\nq\n")
	if result.Code != 0 || result.Stdout != "other\n" {
		t.Fatalf("改名: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s", "nope"}, "file\nq\n")
	if result.Code != 0 || result.Stdout != "\"nope\" line 0 of 0 --0%--\n" {
		t.Fatalf("空缓冲: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-s"}, "file\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "没有当前文件名") {
		t.Fatalf("无名: %+v", result)
	}
}

func TestArgsRewindEdge(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-s"}, "args\nq\n")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("args 无文件: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s"}, "rewind\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "没有当前文件名") {
		t.Fatalf("rewind 无文件: %+v", result)
	}
}

func TestBareSet(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-s"}, "set\nset number\nset\nq!\n")
	if result.Code != 0 || result.Stdout != "number\nnumber\n" {
		t.Fatalf("裸 set: %+v", result)
	}
}

func TestFileErrors(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-s"}, "a\nx\n.\nw nodir/out\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "nodir/out") {
		t.Fatalf("w 失败: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s"}, "a\nx\n.\nW nodir/out\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "nodir/out") {
		t.Fatalf("W 失败: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-s"}, "r .\nq\n")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("r 目录: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--bogus"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("选项: %+v", result)
	}
}
