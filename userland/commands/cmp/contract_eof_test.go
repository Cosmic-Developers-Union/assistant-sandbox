package cmp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestEOFMismatch(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"short": "abc",
		"long":  "abcd",
		"left":  "aaa\nbbb\n",
		"right": "aaa\nccc\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"short", "long"}, "")
	if result.Code != 1 || !strings.Contains(result.Stdout, "EOF on short after byte 3") {
		t.Fatalf("得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-s", "short", "long"}, ""); result.Code != 1 || result.Stdout != "" {
		t.Fatalf("-s 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"left", "right"}, "")
	if result.Code != 1 || !strings.Contains(result.Stdout, "byte 5, line 2") {
		t.Fatalf("行号报告得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"long", "short"}, ""); result.Code != 1 || !strings.Contains(result.Stdout, "EOF on short after byte 3") {
		t.Fatalf("反向得到 %+v", result)
	}
}
