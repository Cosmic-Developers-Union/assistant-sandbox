package shell

import (
	"strings"
	"testing"
)

func TestHashRemembersRegisteredCommands(t *testing.T) {
	dir := t.TempDir()
	if out, diag, code := runScript(t, dir, "hash echo; hash"); code != 0 || out != "0\t/bin/echo\n" || diag != "" {
		t.Fatalf("记住后列出 = (%q, %q, %d)", out, diag, code)
	}

	if out, _, code := runScript(t, dir, "hash echo; echo; echo; hash"); code != 0 || out != "\n\n2\t/bin/echo\n" {
		t.Fatalf("执行两次后 = (%q, %d)", out, code)
	}

	_, diag, code := runScript(t, dir, "hash no-such-command")
	if code != 1 || !strings.Contains(diag, "no-such-command") || strings.Contains(diag, "exit status") {
		t.Fatalf("未找到 = (%q, %d)", diag, code)
	}

	if out, _, code := runScript(t, dir, "hash echo; hash -r; hash"); code != 0 || out != "" {
		t.Fatalf("-r 清空 = (%q, %d)", out, code)
	}

	if _, diag, code := runScript(t, dir, "hash ./missing/script"); code != 1 || !strings.Contains(diag, "./missing/script") {
		t.Fatalf("带路径 = (%q, %d)", diag, code)
	}
}

func TestFCHistoryList(t *testing.T) {
	dir := t.TempDir()
	if out, _, code := runScript(t, dir, "true a; true b; fc -l"); code != 0 || out != "1\ttrue a\n2\ttrue b\n" {
		t.Fatalf("列表 = (%q, %d)", out, code)
	}
	if out, _, code := runScript(t, dir, "true a; true b; fc -l -n"); code != 0 || out != "\ttrue a\n\ttrue b\n" {
		t.Fatalf("-n = (%q, %d)", out, code)
	}
	if out, _, code := runScript(t, dir, "true a; true b; fc -l -r"); code != 0 || out != "2\ttrue b\n1\ttrue a\n" {
		t.Fatalf("-r = (%q, %d)", out, code)
	}

	if out, _, code := runScript(t, dir, "true a; true b; true c; fc -l -2"); code != 0 || out != "2\ttrue b\n3\ttrue c\n" {
		t.Fatalf("-2 = (%q, %d)", out, code)
	}

	if out, _, code := runScript(t, dir, "true a; echo b; fc -l ec"); code != 0 || out != "b\n2\techo b\n" {
		t.Fatalf("前缀 = (%q, %d)", out, code)
	}

	if out, _, code := runScript(t, dir, "fc -l"); code != 0 || out != "" {
		t.Fatalf("空历史 = (%q, %d)", out, code)
	}

	if _, diag, code := runScript(t, dir, "true a; fc -l 9"); code != 1 || !strings.Contains(diag, "越界") {
		t.Fatalf("越界 = (%q, %d)", diag, code)
	}

	if _, diag, code := runScript(t, dir, "true a; fc -l -s"); code != 1 || !strings.Contains(diag, "不能同用") {
		t.Fatalf("-l -s = (%q, %d)", diag, code)
	}
	if _, diag, code := runScript(t, dir, "true a; fc -e vi"); code != 1 || !strings.Contains(diag, "编辑器") {
		t.Fatalf("-e = (%q, %d)", diag, code)
	}
}

func TestFCReplay(t *testing.T) {
	dir := t.TempDir()

	if out, _, code := runScript(t, dir, "echo a; fc -s"); code != 0 || out != "a\na\n" {
		t.Fatalf("重放最近一条 = (%q, %d)", out, code)
	}

	if out, _, code := runScript(t, dir, "echo a; fc -s a=b"); code != 0 || out != "a\nb\n" {
		t.Fatalf("替换 = (%q, %d)", out, code)
	}

	if out, _, code := runScript(t, dir, "true a; echo b; fc -s ec"); code != 0 || out != "b\nb\n" {
		t.Fatalf("前缀选择 = (%q, %d)", out, code)
	}

	if _, diag, code := runScript(t, dir, "fc -s"); code != 1 || !strings.Contains(diag, "历史为空") {
		t.Fatalf("空历史 = (%q, %d)", diag, code)
	}

}

func TestJobControlRewritesReachRegisteredCommands(t *testing.T) {
	dir := t.TempDir()
	if out, diag, code := runScript(t, dir, "jobs"); code != 0 || out != "" || diag != "" {
		t.Fatalf("jobs = (%q, %q, %d)", out, diag, code)
	}
	if _, diag, code := runScript(t, dir, "bg"); code != 1 || !strings.Contains(diag, "作业控制") {
		t.Fatalf("bg = (%q, %d)", diag, code)
	}
	if _, diag, code := runScript(t, dir, "fg %1"); code != 1 || !strings.Contains(diag, "作业控制") {
		t.Fatalf("fg = (%q, %d)", diag, code)
	}

	if out, _, code := runScript(t, dir, "bg; echo after"); code != 0 || out != "after\n" {
		t.Fatalf("bg 后继续 = (%q, %d)", out, code)
	}
}
