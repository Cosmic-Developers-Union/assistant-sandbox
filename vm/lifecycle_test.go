package vm

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestLanguageProcessLifecycleBindsCanonicalStateAndStreams(t *testing.T) {
	descriptors := fd.New()
	reg, _ := executable.New(map[string]executable.Program{"writer": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		if p.Args[0] != "canonical" || p.Env["VALUE"] != "original" {
			t.Fatal(p)
		}
		current, err := proc.FromContext(ctx)
		if err != nil || current.ID != p.ID {
			t.Fatal(current, err)
		}
		stream, err := p.Resources.Descriptors()
		if err != nil {
			return 0, err
		}
		out, err := stream.Get(ctx, uint64(p.ID), 1)
		if err != nil {
			return 0, err
		}
		_, err = out.Write([]byte(p.Args[0]))
		return 3, err
	})})
	kernel, _ := New(Services{Set: capability.Set{FD: descriptors}, Programs: reg}, 3)
	var out bytes.Buffer
	for range 300 {
		child, err := kernel.Begin(0, "writer", []string{"canonical"}, "/", map[string]string{"VALUE": "original"}, fd.Streams{Output: &out})
		if err != nil {
			t.Fatal(err)
		}
		forged := child
		forged.Args[0] = "changed"
		forged.Env["VALUE"] = "changed"
		forged.Resources = nil
		status, err := kernel.RunRegistered(t.Context(), forged)
		if status.Code != 3 || err != nil {
			t.Fatal(status, err)
		}
		if err := kernel.Finish(99, child.ID, status); !errors.Is(err, errno.NoChild) {
			t.Fatal(err)
		}
		if err := kernel.Finish(0, child.ID, status); err != nil {
			t.Fatal(err)
		}
		// H10/K10:僵尸由 wait 回收,额度与描述符随退出清算。
		if _, _, waitErr := kernel.WaitChild(context.Background(), 0, child.ID, true); waitErr != nil {
			t.Fatal(waitErr)
		}

		if _, err := descriptors.Get(t.Context(), uint64(child.ID), 1); !errors.Is(err, errno.NoProcess) {
			t.Fatal(err)
		}
	}
	if len(kernel.Processes()) != 1 || out.Len() != 300*len("canonical") {
		t.Fatal(kernel.Processes(), out.Len())
	}
	if _, err := kernel.RunRegistered(t.Context(), proc.Proc{ID: 999}); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if err := kernel.Finish(0, 999, ExitStatus{}); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := kernel.RunRegistered(ctx, proc.Proc{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	root, _ := kernel.Begin(0, "writer", nil, "/", nil, fd.Streams{})
	child, _ := kernel.Begin(root.ID, "writer", nil, "/", nil, fd.Streams{})
	if err := kernel.Finish(0, root.ID, ExitStatus{}); !errors.Is(err, errno.Busy) {
		t.Fatal(err)
	}
	if kernel.Processes()[1].State != proc.Running {
		t.Fatal("未回收子进程时提前退出父进程")
	}
	if _, err := kernel.RunRegistered(t.Context(), proc.Proc{ID: root.ID, Program: "forged"}); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := kernel.Finish(root.ID, child.ID, ExitStatus{Code: 3}); err != nil {
		t.Error(err)
	}
	if _, _, waitErr := kernel.WaitChild(context.Background(), root.ID, child.ID, true); waitErr != nil {
		t.Error(waitErr)
	}
	if err := kernel.Finish(0, root.ID, ExitStatus{}); err != nil {
		t.Error(err)
	}
	invalid, _ := New(Services{Programs: nilRegistry{}}, 3)
	if _, err := invalid.Begin(0, "nil", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestRegisteredPanicFailsAllFutureProcessCreation(t *testing.T) {
	reg, _ := executable.New(map[string]executable.Program{"panic": executable.Func(func(context.Context, proc.Proc) (uint8, error) { panic("fault") })})
	kernel, _ := New(Services{Programs: reg}, 3)
	p, _ := kernel.Begin(0, "panic", nil, "/", nil, fd.Streams{})

	if _, err := kernel.RunRegistered(t.Context(), p); err == nil {
		t.Fatal("故障应返回错误")
	}
	if _, err := kernel.Begin(0, "panic", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Busy) {
		t.Fatal(err)
	}
}
