package sed

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestWriteCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-n", "9w out.txt", "in.txt"}, ""); result.Code != 0 {
		t.Fatalf("未选中的 w 得到 %+v", result)
	}
	if info, err := os.Stat(filepath.Join(dir, "out.txt")); err != nil || info.Size() != 0 {
		t.Fatalf("w 的目标应被创建并截断: %v %v", info, err)
	}

	result := commandtest.Run(t, Run, dir, []string{"-n", "-e", "1w out.txt", "-e", "2w out.txt", "in.txt"}, "")
	if result.Code != 0 {
		t.Fatalf("w 得到 %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil || string(data) != "a\nb\n" {
		t.Fatalf("写出内容 = %q, %v", data, err)
	}

	result = commandtest.Run(t, Run, dir, []string{"w absent-dir/x.txt", "in.txt"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("不可写目标得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"w", "in.txt"}, ""); result.Code != 2 {
		t.Fatalf("缺少文件名应报错: %+v", result)
	}
}

func TestReadCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("R1\nR2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"r extra.txt", "in.txt"}, "")
	if result.Code != 0 || result.Stdout != "a\nR1\nR2\nb\nR1\nR2\n" {
		t.Fatalf("r 得到 %+v", result)
	}

	if err := os.WriteFile(filepath.Join(dir, "raw.txt"), []byte("RAW"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"r raw.txt", "in.txt"}, "")
	if result.Code != 0 || result.Stdout != "a\nRAWb\nRAW" {
		t.Fatalf("无结尾换行的 r 得到 %q", result.Stdout)
	}

	result = commandtest.Run(t, Run, dir, []string{"r missing.txt", "in.txt"}, "")
	if result.Code != 0 || result.Stderr != "" || result.Stdout != "a\nb\n" {
		t.Fatalf("缺失文件的 r 得到 %+v", result)
	}
}

func TestReadWriteScriptOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("R\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-e", "r extra.txt", "-e", "a A", "in.txt"}, "")
	if result.Code != 0 || result.Stdout != "a\nR\nA\n" {
		t.Fatalf("r 在 a 之前得到 %q", result.Stdout)
	}
	result = commandtest.Run(t, Run, dir, []string{"-e", "a A", "-e", "r extra.txt", "in.txt"}, "")
	if result.Code != 0 || result.Stdout != "a\nA\nR\n" {
		t.Fatalf("a 在 r 之前得到 %q", result.Stdout)
	}
}

func TestReadWriteWithInPlace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(target, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-i", "-n", "w saved.txt", "in.txt"}, "")
	if result.Code != 0 {
		t.Fatalf("-i 与 w 得到 %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "saved.txt")); err != nil || string(data) != "a\nb\n" {
		t.Fatalf("saved.txt = %q, %v", data, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "" {
		t.Fatalf("in.txt 应被 -n 清空: %q, %v", data, err)
	}
}
