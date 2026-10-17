package diffcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("1\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("1\n3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-q", "a", "b"}, "")
	if result.Code != 1 || !strings.Contains(result.Stdout, "differ") {
		t.Fatalf("-q 有差异得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-q", "a", "a"}, "")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("-q 相同得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-u", "a", "b"}, "")
	if result.Code != 1 || !strings.Contains(result.Stdout, "@@") || !strings.Contains(result.Stdout, "-2\n+3\n") {
		t.Fatalf("-u 统一格式得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"absent"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("缺少操作数得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"--not-a-flag", "a", "b"}, ""); result.Code != 2 {
		t.Fatalf("未知参数得到 %+v", result)
	}

	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}

func TestOversizeFilesNotReportedIdentical(t *testing.T) {
	dir := t.TempDir()
	prefix := bytes.Repeat([]byte("x"), 4<<20+1)
	for name, tail := range map[string]string{"a": "left", "b": "right"} {
		body := append(append([]byte{}, prefix...), tail...)
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"-q", "a", "b"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("超预算文件应报错: %+v", result)
	}
}
