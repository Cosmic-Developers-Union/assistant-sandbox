package signal

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestResetForExecKeepsIgnoresAndMasks(t *testing.T) {
	table := New()
	table.Spawn(4)
	if err := table.SetIgnored(4, HUP, true); err != nil {
		t.Fatal(err)
	}
	if err := table.SetMasked(4, TERM, true); err != nil {
		t.Fatal(err)
	}
	if err := table.Raise(4, USR1); err != nil {
		t.Fatal(err)
	}
	fired := false
	table.BindInterrupt(4, func(Number) { fired = true })
	if err := table.ResetForExec(4); err != nil {
		t.Fatal(err)
	}

	if pending, err := table.Pending(4); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}

	if table.Interrupt(4, USR1) || fired {
		t.Fatal("中断钩子必须随旧程序失效")
	}

	if err := table.Raise(4, HUP); err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Deliverable(4); ok {
		t.Fatal("忽略处置必须保留")
	}

	if err := table.Raise(4, TERM); err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Deliverable(4); ok {
		t.Fatal("屏蔽必须保留")
	}
	if err := table.SetMasked(4, TERM, false); err != nil {
		t.Fatal(err)
	}
	if n, ok := table.Deliverable(4); !ok || n != TERM {
		t.Fatalf("解除屏蔽后应可投递: n=%v ok=%v", n, ok)
	}
}

func TestResetForExecRejectsUnknownProcess(t *testing.T) {
	table := New()
	if err := table.ResetForExec(77); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}
}
