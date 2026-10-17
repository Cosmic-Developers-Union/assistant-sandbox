package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestAliasesReferenceVmExecutablePort(t *testing.T) {

	var _ Executor = New()
	if Quota != 3 || Fault != 2 || Signaled != 1 || Exited != 0 {
		t.Fatal("Reason 常量必须与端口定义一致")
	}
	instance, err := New().Start(t.Context(), executable.Func(func(context.Context, proc.Proc) (uint8, error) {
		return 2, nil
	}), proc.Proc{ID: 1, Program: "probe"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := instance.Wait(t.Context())
	if err != nil || result.Code != 2 || result.Reason != Exited {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := New().Start(t.Context(), nil, proc.Proc{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("err=%v", err)
	}
}
