package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLISingleExecutionUsesVMAndHostMount 验证 R02:单次执行经同一
// 原生 VM + Bash 槽位,并把授权工作区挂载为 /work。
func TestCLISingleExecutionUsesVMAndHostMount(t *testing.T) {
	dir := t.TempDir()
	output, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	code, err := run(t.Context(), []string{"--dir", dir, "-c", "printf cli-ok; printf data > /work/file"}, strings.NewReader(""), output, diagnostic)
	if err != nil || code != 0 || output.String() != "cli-ok" {
		t.Fatalf("code=%d err=%v out=%q stderr=%q", code, err, output.String(), diagnostic.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "file"))
	if err != nil || string(data) != "data" {
		t.Fatalf("宿主文件=%q err=%v", data, err)
	}
}

// TestCLISessionKeepsSameVM 验证 R02:持续会话逐行执行仍共享同一
// VM/Bash 状态,不另造内核。
func TestCLISessionKeepsSameVM(t *testing.T) {
	dir := t.TempDir()
	output, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	input := "X=kept\nprintf \"$X\"; printf session > /work/session\n"
	code, err := run(t.Context(), []string{"--dir", dir, "--session"}, strings.NewReader(input), output, diagnostic)
	if err != nil || code != 0 || output.String() != "kept" {
		t.Fatalf("code=%d err=%v out=%q stderr=%q", code, err, output.String(), diagnostic.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "session"))
	if err != nil || string(data) != "session" {
		t.Fatalf("会话文件=%q err=%v", data, err)
	}
}

// TestCLIDryRunIsSideEffectFree 验证 debug/verbose/dry-run 行为明确。
func TestCLIDryRunIsSideEffectFree(t *testing.T) {
	dir := t.TempDir()
	output, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	code, err := run(t.Context(), []string{"--dir", dir, "--dry-run", "--debug", "-c", "printf never > /work/file"}, strings.NewReader(""), output, diagnostic)
	if err != nil || code != 0 || output.Len() != 0 || !strings.Contains(diagnostic.String(), "工作区=") {
		t.Fatalf("code=%d err=%v out=%q stderr=%q", code, err, output.String(), diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "file")); !os.IsNotExist(err) {
		t.Fatalf("dry-run 产生副作用: %v", err)
	}
}

// TestCLIDefaultDoesNotMountWorkingDirectory 验证安全默认:未显式指定
// --dir 时,宿主当前目录不会成为客体 /work;写入只留在临时 VM 内。
func TestCLIDefaultDoesNotMountWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	output, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	code, err := run(t.Context(), []string{"-c", "printf data > /work/private"}, strings.NewReader(""), output, diagnostic)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v stdout=%q stderr=%q", code, err, output.String(), diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "private")); !os.IsNotExist(err) {
		t.Fatalf("默认行为把当前目录挂载到了 /work: %v", err)
	}
}
