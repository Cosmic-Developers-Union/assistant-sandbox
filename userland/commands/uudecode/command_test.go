package uudecodecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	clearUmask(t)
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, nil, catFixture)
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("标准输入解码得到 %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cat.txt"))
	if err != nil || string(data) != "Cat" {
		t.Fatalf("解码结果 = (%q, %v)", data, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "in.uue"), []byte(catFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"-o", "/dev/stdout", "in.uue"}, "")
	if result.Code != 0 || result.Stdout != "Cat" || result.Stderr != "" {
		t.Fatalf("操作数 + -o 得到 %+v", result)
	}

	if err := os.WriteFile(filepath.Join(dir, "cat.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, nil, catFixture)
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("覆盖解码得到 %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "cat.txt")); err != nil || string(data) != "Cat" {
		t.Fatalf("覆盖结果 = (%q, %v)", data, err)
	}

	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, nil, "begin 600 adir\n#0V%T\n \nend\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "not a regular file") {
		t.Fatalf("目录目标得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"absent.uue"}, ""); result.Code != 1 || result.Stderr == "" {
		t.Fatalf("缺失输入得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, nil, "no header here\n"); result.Code != 1 || !strings.Contains(result.Stderr, "no 'begin' line") {
		t.Fatalf("无 begin 行得到 %+v", result)
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

func TestModeGrammarEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		spec string
		want os.FileMode
	}{
		{"u=rwx,go=r", 0o744},
		{"a+r", 0o444},
		{"u+w", 0o200},
		{"g+x", 0o010},
		{"o-rwx", 0},
	} {
		got, err := parseHeaderMode(tc.spec)
		if err != nil || got != tc.want {
			t.Errorf("parseHeaderMode(%q) = (%v, %v), want %v", tc.spec, got, err, tc.want)
		}
	}
	for _, spec := range []string{"", "u+z", "u+rw,", "z+r"} {
		if _, err := parseHeaderMode(spec); err == nil {
			t.Errorf("parseHeaderMode(%q) 应失败", spec)
		}
	}
}
