package id

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
)

func run(t *testing.T, args ...string) commandtest.Result {
	t.Helper()
	return commandtest.RunWithIdentity(t, Run, t.TempDir(), args, "", nil, identity.SingleUser{})
}

func TestIDPrintsDeclaredIdentity(t *testing.T) {
	if r := run(t); r.Code != 0 || r.Stdout != "uid=0(sandbox) gid=0\n" {
		t.Fatalf("默认 = %+v", r)
	}
	if r := run(t, "-u"); r.Code != 0 || r.Stdout != "0\n" {
		t.Fatalf("-u = %+v", r)
	}
	if r := run(t, "-u", "-n"); r.Code != 0 || r.Stdout != "sandbox\n" {
		t.Fatalf("-u -n = %+v", r)
	}
	if r := run(t, "-g"); r.Code != 0 || r.Stdout != "0\n" {
		t.Fatalf("-g = %+v", r)
	}
	if r := run(t, "-G"); r.Code != 0 || r.Stdout != "0\n" {
		t.Fatalf("-G = %+v", r)
	}

	if r := run(t, "-u", "-r"); r.Code != 0 || r.Stdout != "0\n" {
		t.Fatalf("-u -r = %+v", r)
	}

	if r := run(t, "sandbox"); r.Code != 0 || r.Stdout != "uid=0(sandbox) gid=0\n" {
		t.Fatalf("按名查询 = %+v", r)
	}
	if r := run(t, "0"); r.Code != 0 || r.Stdout != "uid=0(sandbox) gid=0\n" {
		t.Fatalf("按 UID 查询 = %+v", r)
	}
}

func TestIDRejectsUnknownUsersAndGroupNames(t *testing.T) {
	if r := run(t, "nobody"); r.Code != 1 || r.Stdout != "" || r.Stderr == "" {
		t.Fatalf("未知用户 = %+v", r)
	}
	if r := run(t, "-g", "-n"); r.Code != 1 || r.Stderr == "" {
		t.Fatalf("-g -n = %+v", r)
	}
	if r := run(t, "-G", "-n"); r.Code != 1 {
		t.Fatalf("-G -n = %+v", r)
	}

	if r := run(t, "-g", "-u"); r.Code != 2 {
		t.Fatalf("互斥 = %+v", r)
	}
	if r := run(t, "sandbox", "sandbox"); r.Code != 2 {
		t.Fatalf("多操作数 = %+v", r)
	}
	if r := run(t, "--bogus"); r.Code != 2 || !strings.Contains(r.Stderr, "参数错误") {
		t.Fatalf("未知选项 = %+v", r)
	}
}
