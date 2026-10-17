package man

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestDisplaysBuiltinPage(t *testing.T) {
	r := commandtest.Run(t, Run, t.TempDir(), []string{"man"}, "")
	if r.Code != 0 || !strings.Contains(r.Stdout, "man(1)") {
		t.Fatalf("man man = %+v", r)
	}
	if !strings.Contains(r.Stdout, "SYNOPSIS") || !strings.Contains(r.Stdout, "DESCRIPTION") {
		t.Fatalf("页应有标准段: %s", r.Stdout)
	}
}

func TestDisplaysAllDeclaredPages(t *testing.T) {
	for _, name := range []string{"nice", "renice", "at", "batch", "vi", "tabs", "lp", "talk"} {
		r := commandtest.Run(t, Run, t.TempDir(), []string{name}, "")
		if r.Code != 0 || !strings.Contains(r.Stdout, name+"(1)") {
			t.Fatalf("%s = %+v", name, r)
		}
	}
}

func TestMissingEntryExits1(t *testing.T) {
	r := commandtest.Run(t, Run, t.TempDir(), []string{"nosuchcmd"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有手册页条目") {
		t.Fatalf("未知名字 = %+v", r)
	}

	r = commandtest.Run(t, Run, t.TempDir(), []string{"man", "nosuchcmd"}, "")
	if r.Code != 1 || !strings.Contains(r.Stdout, "man(1)") {
		t.Fatalf("混合查询 = %+v", r)
	}
}

func TestWhatisAndKeyword(t *testing.T) {
	r := commandtest.Run(t, Run, t.TempDir(), []string{"-f", "at"}, "")
	if r.Code != 0 || !strings.Contains(r.Stdout, "at (1) - execute commands at a later time") {
		t.Fatalf("-f = %+v", r)
	}
	r = commandtest.Run(t, Run, t.TempDir(), []string{"-k", "priority"}, "")
	if r.Code != 0 || !strings.Contains(r.Stdout, "nice") || !strings.Contains(r.Stdout, "renice") {
		t.Fatalf("-k priority = %+v", r)
	}
	r = commandtest.Run(t, Run, t.TempDir(), []string{"-f", "nosuchcmd"}, "")
	if r.Code != 1 {
		t.Fatalf("-f 未知名字 = %+v", r)
	}
}

func TestLeadingSectionSkipped(t *testing.T) {
	r := commandtest.Run(t, Run, t.TempDir(), []string{"1", "man"}, "")
	if r.Code != 0 || !strings.Contains(r.Stdout, "man(1)") {
		t.Fatalf("前导小节 = %+v", r)
	}
}

func TestWrapRespectsWidth(t *testing.T) {
	page, ok := lookup("at")
	if !ok {
		t.Fatal("at 页应存在")
	}
	rendered := page.render(40)
	for _, line := range strings.Split(rendered, "\n") {

		display := 0
		for _, r := range line {
			if r > 0x7F {
				display += 2
			} else {
				display++
			}
		}
		if display > 40 && !strings.HasSuffix(line, "手册") && !strings.HasPrefix(strings.TrimSpace(line), "SYNOPSIS") && !strings.HasPrefix(strings.TrimSpace(line), "DESCRIPTION") {
			t.Fatalf("断行超宽（显示宽度 %d）: %q", display, line)
		}
	}
}
