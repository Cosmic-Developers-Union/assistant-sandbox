package signal

import (
	"errors"
	"sync"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestDefaultActions(t *testing.T) {
	terminate := []Number{HUP, INT, USR1, USR2, ALRM, TERM, PIPE, XCPU, PWR, IO}
	for _, n := range terminate {
		if a, err := Default(n); err != nil || a != Terminate {
			t.Fatalf("%v: %v %v", n, a, err)
		}
	}
	core := []Number{QUIT, ILL, TRAP, ABRT, BUS, FPE, SEGV, SYS}
	for _, n := range core {
		if a, _ := Default(n); a != Core {
			t.Fatalf("%v: %v", n, a)
		}
	}
	for _, n := range []Number{CHLD, URG, WINCH, CONT} {
		if a, _ := Default(n); a != Ignore {
			t.Fatalf("%v: %v", n, a)
		}
	}
	for _, n := range []Number{STOP, TSTP, TTIN, TTOU} {
		if a, _ := Default(n); a != Stop {
			t.Fatalf("%v: %v", n, a)
		}
	}
	if _, err := Default(0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if _, err := Default(Max + 1); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestPendingMergeAndDeliverableOrder(t *testing.T) {
	table := New()
	table.Spawn(1)
	if err := table.Raise(1, TERM); err != nil {
		t.Fatal(err)
	}

	if err := table.Raise(1, TERM); err != nil {
		t.Fatal(err)
	}
	pending, _ := table.Pending(1)
	if len(pending) != 1 || pending[0] != TERM {
		t.Fatalf("pending=%v", pending)
	}
	if err := table.Raise(1, HUP); err != nil {
		t.Fatal(err)
	}

	if n, ok := table.Deliverable(1); !ok || n != HUP {
		t.Fatalf("n=%v ok=%v", n, ok)
	}
	table.Consume(1, HUP)
	if n, ok := table.Deliverable(1); !ok || n != TERM {
		t.Fatalf("n=%v ok=%v", n, ok)
	}
	table.Consume(1, TERM)
	if _, ok := table.Deliverable(1); ok {
		t.Fatal("不应再有可投递信号")
	}
}

func TestMaskAndIgnore(t *testing.T) {
	table := New()
	table.Spawn(2)

	for _, fn := range []func() error{
		func() error { return table.SetMasked(2, KILL, true) },
		func() error { return table.SetIgnored(2, STOP, true) },
	} {
		if err := fn(); !errors.Is(err, errno.NotSupported) {
			t.Fatalf("err=%v", err)
		}
	}
	if err := table.SetMasked(2, TERM, true); err != nil {
		t.Fatal(err)
	}
	if err := table.Raise(2, TERM); err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Deliverable(2); ok {
		t.Fatal("被屏蔽信号不应投递")
	}

	if err := table.SetMasked(2, TERM, false); err != nil {
		t.Fatal(err)
	}
	if n, ok := table.Deliverable(2); !ok || n != TERM {
		t.Fatalf("n=%v ok=%v", n, ok)
	}
	table.Consume(2, TERM)

	if err := table.SetMasked(2, INT, true); err != nil {
		t.Fatal(err)
	}
	if err := table.Raise(2, INT); err != nil {
		t.Fatal(err)
	}
	if err := table.SetMasked(2, INT, false); err != nil {
		t.Fatal(err)
	}
	if err := table.SetIgnored(2, INT, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Deliverable(2); ok {
		t.Fatal("忽略处置不应投递")
	}
	if pending, _ := table.Pending(2); len(pending) != 0 {
		t.Fatalf("pending=%v", pending)
	}
}

func TestUnknownProcessAndInvalidNumber(t *testing.T) {
	table := New()
	if err := table.Raise(9, TERM); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if _, err := table.Pending(9); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	table.Spawn(9)
	if err := table.Raise(9, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := table.Raise(9, 32); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	table.Discard(9)
	if err := table.Raise(9, TERM); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
}

func TestInterruptHooks(t *testing.T) {
	table := New()
	table.Spawn(3)
	called := make(chan Number, 2)
	table.BindInterrupt(3, func(n Number) { called <- n })
	if err := table.Raise(3, TERM); err != nil {
		t.Fatal(err)
	}
	if !table.Interrupt(3, TERM) {
		t.Fatal("钩子未触发")
	}
	if n := <-called; n != TERM {
		t.Fatalf("n=%v", n)
	}
	table.BindInterrupt(3, nil)
	if table.Interrupt(3, TERM) {
		t.Fatal("钩子应已撤销")
	}

	table.Spawn(4)
	if table.Interrupt(4, TERM) {
		t.Fatal("不应有钩子")
	}
}

func TestExitCodeAndNames(t *testing.T) {
	if ExitCode(TERM) != 143 || ExitCode(INT) != 130 {
		t.Fatal("退出码换算错误")
	}
	if TERM.Name() != "SIGTERM" || TERM.String() != "SIGTERM" {
		t.Fatal(TERM.Name())
	}
	if Number(40).Name() != "" || Number(40).String() != "signal(40)" {
		t.Fatal("未知编号应可诊断")
	}
}

func TestSignalStateConcurrencyAndUnknownNumber(t *testing.T) {
	table := New()
	table.Spawn(1)
	numbers := [...]Number{1, 2, 3, 4, 5, 6, 7, 8}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 50 {
				_ = table.Raise(1, numbers[i])
				_, _ = table.Pending(1)
			}
		})
	}
	wg.Wait()

	if n, ok := table.Deliverable(1); !ok || !n.Valid() {
		t.Fatalf("n=%v ok=%v", n, ok)
	}
	if err := table.SetMasked(1, 99, true); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestActionText(t *testing.T) {
	for action, want := range map[Action]string{
		Terminate: "terminate",
		Core:      "core",
		Ignore:    "ignore",
		Stop:      "stop",
		Continue:  "continue",
	} {
		if got := action.String(); got != want {
			t.Fatalf("Action(%d).String()=%q want %q", action, got, want)
		}
	}
	if Action(200).String() != "unknown" {
		t.Fatal("未知动作必须有稳定文本")
	}
}

func TestSignalNameLookup(t *testing.T) {
	for name, want := range map[string]Number{
		"SIGHUP": HUP, "SIGINT": INT, "SIGKILL": KILL, "SIGTERM": TERM, "SIGWINCH": WINCH,
	} {
		got, err := ByName(name)
		if err != nil || got != want {
			t.Fatalf("ByName(%q)=%v err=%v", name, got, err)
		}
		if got.Name() != name {
			t.Fatalf("Name()=%q want %q", got.Name(), name)
		}
	}
	if _, err := ByName("SIGNOPE"); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if Number(0).Valid() || (Max + 1).Valid() || !TERM.Valid() {
		t.Fatal("Valid 范围错误")
	}
}

func TestStateRejectsInvalidNumbers(t *testing.T) {
	var state State
	if state.Raise(0) || state.Raise(Max+1) {
		t.Fatal("越界编号不得进入待决集合")
	}
	if err := state.SetMasked(0, true); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := state.SetIgnored(Max+1, true); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := state.SetIgnored(KILL, true); !errors.Is(err, errno.NotSupported) {
		t.Fatal(err)
	}
}

func TestTableReportsUnknownProcesses(t *testing.T) {
	table := New()
	if err := table.SetMasked(77, TERM, true); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if err := table.SetIgnored(77, TERM, true); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if n, ok := table.Deliverable(77); ok || n != 0 {
		t.Fatalf("n=%v ok=%v", n, ok)
	}

	table.Consume(77, TERM)
	if table.Interrupt(77, TERM) {
		t.Fatal("未登记进程不应有中断钩子")
	}
}

func TestIgnoredSignalCanBeRestored(t *testing.T) {
	table := New()
	table.Spawn(5)
	if err := table.SetIgnored(5, INT, true); err != nil {
		t.Fatal(err)
	}
	if err := table.Raise(5, INT); err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Deliverable(5); ok {
		t.Fatal("忽略处置的信号不得投递")
	}
	if err := table.SetIgnored(5, INT, false); err != nil {
		t.Fatal(err)
	}
	if err := table.Raise(5, INT); err != nil {
		t.Fatal(err)
	}
	if n, ok := table.Deliverable(5); !ok || n != INT {
		t.Fatalf("恢复后应可投递: n=%v ok=%v", n, ok)
	}
}

func TestActionRegistrationLifecycle(t *testing.T) {
	table := New()
	table.Spawn(9)

	// 未登记即 SIG_DFL。
	if config, err := table.Action(9, USR1); err != nil || config.Handler != SigDefault {
		t.Fatalf("缺省处置=%+v err=%v", config, err)
	}
	// 登记 SIG_IGN 与自定义处理器,互相覆盖。
	if err := table.SetAction(9, USR1, ActionConfig{Handler: SigIgnore}); err != nil {
		t.Fatal(err)
	}
	if config, _ := table.Action(9, USR1); config.Handler != SigIgnore {
		t.Fatalf("IGN 未登记: %+v", config)
	}
	custom := ActionConfig{Handler: 0xCAFE, Flags: 4, Restorer: 0x9000, Mask: 1<<10 | 1<<12}
	if err := table.SetAction(9, USR1, custom); err != nil {
		t.Fatal(err)
	}
	if config, _ := table.Action(9, USR1); config != custom {
		t.Fatalf("自定义处置=%+v want %+v", config, custom)
	}
	// 未登记的信号不受影响。
	if config, _ := table.Action(9, TERM); config.Handler != SigDefault {
		t.Fatalf("TERM 被波及: %+v", config)
	}

	// KILL/STOP 与非法号 EINVAL。
	for _, n := range []Number{KILL, STOP, 0, Max + 1} {
		if err := table.SetAction(9, n, ActionConfig{Handler: 1}); !errors.Is(err, errno.Invalid) {
			t.Fatalf("%d: err=%v want EINVAL", n, err)
		}
	}

	// exec 复位处置:全部回归 SIG_DFL。
	if err := table.ResetForExec(9); err != nil {
		t.Fatal(err)
	}
	if config, _ := table.Action(9, USR1); config.Handler != SigDefault {
		t.Fatalf("exec 后处置未复位: %+v", config)
	}
	// 未知进程 ENOPROC。
	if err := table.SetAction(99, USR1, ActionConfig{}); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程: err=%v", err)
	}
}

func TestApplyMaskBulkSemantics(t *testing.T) {
	table := New()
	table.Spawn(3)

	// SIG_BLOCK 叠加,返回旧值。
	old, err := table.ApplyMask(3, MaskBlock, 1<<(USR1-1)|1<<(USR2-1))
	if err != nil || old != 0 {
		t.Fatalf("block: old=%d err=%v", old, err)
	}
	old, _ = table.ApplyMask(3, MaskBlock, 1<<(TERM-1))
	if old != 1<<(USR1-1)|1<<(USR2-1) {
		t.Fatalf("第二次 block 返回旧值=%d", old)
	}
	// SIG_UNBLOCK 清位。
	old, _ = table.ApplyMask(3, MaskUnblock, 1<<(USR1-1))
	if old&(1<<(USR1-1)) == 0 {
		t.Fatalf("unblock 返回的旧值应含 USR1: %d", old)
	}
	// SIG_SETMASK 整体替换。
	old, _ = table.ApplyMask(3, MaskSetMask, 1<<(TERM-1))
	if old&(1<<(USR2-1)) == 0 {
		t.Fatalf("setmask 返回的旧值应含 USR2: %d", old)
	}
	if current, _ := table.Mask(3); current != 1<<(TERM-1) {
		t.Fatalf("当前屏蔽=%d", current)
	}
	// 越界位与未知 how EINVAL;屏蔽 KILL/STOP 不被 ApplyMask 拒绝之外,
	// 范围内的合法位正常工作。
	if _, err := table.ApplyMask(3, MaskBlock, 1<<31|1<<40); !errors.Is(err, errno.Invalid) {
		t.Fatalf("越界位: err=%v want EINVAL", err)
	}
	if _, err := table.ApplyMask(3, MaskHow(9), 1); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知 how: err=%v want EINVAL", err)
	}
	if _, err := table.ApplyMask(99, MaskBlock, 1); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程: err=%v", err)
	}
	// 批量屏蔽与单信号屏蔽一致:被屏蔽信号不可投递。
	if _, err := table.ApplyMask(3, MaskBlock, 1<<(HUP-1)); err != nil {
		t.Fatal(err)
	}
	if err := table.Raise(3, HUP); err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Deliverable(3); ok {
		t.Fatal("被屏蔽信号不应可投递")
	}
}

func TestAltStackLifecycle(t *testing.T) {
	table := New()
	table.Spawn(5)

	if stack := table.AltStack(5); stack.Present {
		t.Fatalf("缺省备用栈=%+v", stack)
	}
	table.SetAltStack(5, AltStack{Sp: 0x60000, Size: 0x8000, Present: true})
	if stack := table.AltStack(5); stack.Sp != 0x60000 || stack.Size != 0x8000 || !stack.Present {
		t.Fatalf("登记=%+v", stack)
	}
	// SS_DISABLE:清除登记。
	table.SetAltStack(5, AltStack{})
	if stack := table.AltStack(5); stack.Present {
		t.Fatalf("禁用后=%+v", stack)
	}
	// 未知进程查询返回空栈,不 panic。
	if stack := table.AltStack(99); stack.Present {
		t.Fatal("未知进程不应有备用栈")
	}
}
