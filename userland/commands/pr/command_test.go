package prcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("l1\nl2\nl3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "in"), guestStamp, guestStamp); err != nil {
		t.Fatal(err)
	}

	result := commandtest.Run(t, Run, dir, []string{"in"}, "")
	header := "Jan  2 03:04 2020 in Page 1"
	want := "\n\n" + header + "\n\n\n" + "l1\nl2\nl3\n" + strings.Repeat("\n", 58)
	if result.Code != 0 || result.Stderr != "" || result.Stdout != want {
		t.Fatalf("默认页面 = (%q, %q, %d), want %q", result.Stdout, result.Stderr, result.Code, want)
	}

	result = commandtest.Run(t, Run, dir, []string{"-t"}, "a\nb\n")
	if result.Code != 0 || result.Stdout != "a\nb\n" {
		t.Fatalf("pr -t = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}

	result = commandtest.Run(t, Run, dir, []string{"-t", "-n", "in"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "1\tl1") || !strings.Contains(result.Stdout, "3\tl3") {
		t.Fatalf("pr -t -n = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}

	if err := os.WriteFile(filepath.Join(dir, "two"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"-t", "-2", "in"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "l2") {
		t.Fatalf("pr -2 = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}
	result = commandtest.Run(t, Run, dir, []string{"-m", "-t", "-w", "10", "-s|", "in", "two"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "l1") || !strings.Contains(result.Stdout, "|x") {
		t.Fatalf("pr -m = (%q, %q, %d)", result.Stdout, result.Stderr, result.Code)
	}

	result = commandtest.Run(t, Run, dir, []string{"absent"}, "")
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("缺失输入得到 %+v", result)
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
