package newgrp

import (
	"context"
	"errors"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type runStubControl struct {
	spec proc.RunSpec
	code uint8
	err  error
}

func (c *runStubControl) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (c *runStubControl) Finish(proc.PID, proc.PID, uint8) error { return nil }
func (c *runStubControl) Run(_ context.Context, spec proc.RunSpec) (proc.Proc, error) {
	c.spec = spec
	if c.err != nil {
		return proc.Proc{}, c.err
	}
	return proc.Proc{ExitCode: c.code}, nil
}

func TestNewgrpSubshellWithCurrentGroup(t *testing.T) {

	for _, args := range [][]string{nil, {"0"}, {"-l", "0"}} {
		control := &runStubControl{code: 3}
		r := commandtest.RunWithIdentity(t, Run, t.TempDir(), args, "", control, identity.SingleUser{})
		if r.Code != 3 {
			t.Fatalf("args=%v 退出码 = %d（应为子 shell 的 3）", args, r.Code)
		}
		if control.spec.Name != "bash" || control.spec.Dir == "" {
			t.Fatalf("args=%v RunSpec = %+v", args, control.spec)
		}
	}
}

func TestNewgrpRejectsOtherGroups(t *testing.T) {
	run := func(args ...string) commandtest.Result {
		return commandtest.RunWithIdentity(t, Run, t.TempDir(), args, "", nil, identity.SingleUser{})
	}

	if r := run("sandbox"); r.Code != 1 || !strings.Contains(r.Stderr, "没有这个组") {
		t.Fatalf("组名 = %+v", r)
	}

	if r := run("7"); r.Code != 1 || !strings.Contains(r.Stderr, "7") {
		t.Fatalf("GID 7 = %+v", r)
	}

	if r := run("0", "0"); r.Code != 2 {
		t.Fatalf("多操作数 = %+v", r)
	}
}

func TestNewgrpPropagatesRunErrors(t *testing.T) {
	control := &runStubControl{err: errno.NotFound}
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), nil, "", control, identity.SingleUser{})
	if r.Code != 127 || !strings.Contains(r.Stderr, "bash") {
		t.Fatalf("bash 缺失 = %+v", r)
	}
	control = &runStubControl{err: errno.NoExec}
	r = commandtest.RunWithIdentity(t, Run, t.TempDir(), nil, "", control, identity.SingleUser{})
	if r.Code != 126 {
		t.Fatalf("不可执行 = %+v", r)
	}
}

func TestNewgrpRejectsNegativeGID(t *testing.T) {
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), []string{"--", "-5"}, "", nil, identity.SingleUser{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有这个组") {
		t.Fatalf("负 GID = %+v", r)
	}
}

func TestNewgrpRejectsBadFlag(t *testing.T) {
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), []string{"--bogus"}, "", nil, identity.SingleUser{})
	if r.Code != 2 || !strings.Contains(r.Stderr, "参数错误") {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func TestNewgrpPropagatesPermission(t *testing.T) {
	control := &runStubControl{err: errno.Permission}
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), nil, "", control, identity.SingleUser{})
	if r.Code != 126 {
		t.Fatalf("权限拒绝 = %+v", r)
	}
}

type missingIdentity struct{}

func (missingIdentity) Lookup(ctx context.Context, pid uint64) (identity.Identity, error) {
	return identity.Identity{}, errno.NotImplemented
}

func TestNewgrpReportsMissingCapabilities(t *testing.T) {

	if err := Run(context.Background(), nil); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("脱离内核 = %v", err)
	}

	r := commandtest.Run(t, Run, t.TempDir(), nil, "")
	if r.Code != 2 || !strings.Contains(r.Stderr, "ENOSYS") {
		t.Fatalf("无身份能力 = %+v", r)
	}
}

func TestNewgrpPropagatesLookupFailure(t *testing.T) {
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), nil, "", nil, missingIdentity{})
	if r.Code != 2 {
		t.Fatalf("Lookup 失败 = %+v", r)
	}
}

func TestNewgrpZeroExitSubshell(t *testing.T) {
	control := &runStubControl{code: 0}
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), []string{"0"}, "", control, identity.SingleUser{})
	if r.Code != 0 {
		t.Fatalf("子 shell 成功 = %+v", r)
	}
}

func TestNewgrpWrapsUnexpectedRunError(t *testing.T) {
	control := &runStubControl{err: errno.Busy}
	r := commandtest.RunWithIdentity(t, Run, t.TempDir(), nil, "", control, identity.SingleUser{})
	if r.Code != 2 || !strings.Contains(r.Stderr, "newgrp") {
		t.Fatalf("意外错误包装 = %+v", r)
	}
}
