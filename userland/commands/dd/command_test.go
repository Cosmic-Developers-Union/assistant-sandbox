package ddcmd

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
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := commandtest.Run(t, Run, dir, []string{"ibs=2", "obs=2", "count=1"}, "ab")
	if result.Code != 0 || result.Stdout != "ab" || !strings.Contains(result.Stderr, "1+0 records in") {
		t.Fatalf("stdin→stdout 得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"if=in", "of=out", "bs=3", "count=1"}, "")
	if result.Code != 0 {
		t.Fatalf("if/of 得到 %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "out")); err != nil || string(data) != "abc" {
		t.Fatalf("输出文件 = (%q, %v), want abc", data, err)
	}

	result = commandtest.Run(t, Run, dir, []string{"if=in", "bs=2", "skip=1", "count=1"}, "")
	if result.Code != 0 || result.Stdout != "cd" {
		t.Fatalf("skip= 得到 %+v", result)
	}

	if err := os.WriteFile(filepath.Join(dir, "seek.txt"), []byte("XXXXYYYY"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"if=in", "of=seek.txt", "bs=2", "skip=1", "seek=2", "count=1"}, "")
	if result.Code != 0 {
		t.Fatalf("seek= 得到 %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "seek.txt")); err != nil || string(data) != "XXXXcd" {
		t.Fatalf("seek= 结果 = (%q, %v), want XXXXcd", data, err)
	}

	result = commandtest.Run(t, Run, dir, []string{"conv=ucase"}, "ab")
	if result.Code != 0 || result.Stdout != "AB" {
		t.Fatalf("conv=ucase 得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"conv=swab"}, "ab")
	if result.Code != 0 || result.Stdout != "ba" {
		t.Fatalf("conv=swab 得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"if=absent"}, ""); result.Code != 1 || result.Stderr == "" {
		t.Fatalf("缺失输入得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"bogus=1"}, ""); result.Code != 2 {
		t.Fatalf("未知操作数得到 %+v", result)
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
