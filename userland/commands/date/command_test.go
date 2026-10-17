package datecmd

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"mvdan.cc/sh/v3/interp"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"epoch-default", []string{"-u", "-d", "@0"}, "Thu Jan  1 00:00:00 UTC 1970\n"},
		{"epoch-format", []string{"-u", "-d", "@0", "+%F %T %z"}, "1970-01-01 00:00:00 +0000\n"},
		{"virtual-clock-seconds", []string{"-u", "+%s"}, "0\n"},
		{"virtual-clock-default-shape", []string{"-u"}, "Thu Jan  1 00:00:00 UTC 1970\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, dir, tc.args, "")
			if result.Code != 0 || result.Stderr != "" || result.Stdout != tc.want {
				t.Fatalf("date %q = (%q, %q, %d), want %q", tc.args, result.Stdout, result.Stderr, result.Code, tc.want)
			}
		})
	}

	result := commandtest.Run(t, Run, dir, []string{"-u", "-d", "not-a-date"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "invalid date") {
		t.Fatalf("非法日期得到 %+v", result)
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
