package logname

import (
	"context"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
)

func TestLognamePrintsDeclaredName(t *testing.T) {
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), nil, "", nil, identity.SingleUser{})
	if r.Code != 0 || r.Stdout != "sandbox\n" {
		t.Fatalf("logname = %+v", r)
	}

	r = commandtest.Run(t, Run, t.TempDir(), nil, "")
	if r.Code != 2 || r.Stdout != "" {
		t.Fatalf("无身份能力 = %+v", r)
	}

	r = commandtest.RunWithIdentity(t, Run, t.TempDir(), []string{"sandbox"}, "", nil, identity.SingleUser{})
	if r.Code != 2 {
		t.Fatalf("操作数 = %+v", r)
	}
	r = commandtest.RunWithIdentity(t, Run, t.TempDir(), []string{"--bogus"}, "", nil, identity.SingleUser{})
	if r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

type missingIdentity struct{}

func (missingIdentity) Lookup(ctx context.Context, pid uint64) (identity.Identity, error) {
	return identity.Identity{}, errno.NotImplemented
}

func TestLognameReportsLookupFailure(t *testing.T) {
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), nil, "", nil, missingIdentity{})
	if r.Code != 2 || r.Stdout != "" {
		t.Fatalf("Lookup 失败 = %+v", r)
	}
}
