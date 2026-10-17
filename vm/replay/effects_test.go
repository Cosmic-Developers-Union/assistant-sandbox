package replay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const pureOp = "test.calc"

func init() {

	_ = Register(pureOp, Pure)
}

func TestClassifyDeclaresSideEffects(t *testing.T) {
	for _, name := range []string{OpEntropy, OpBridgeCall, OpStdin} {
		effect, note := Classify(name)
		if effect != SideEffect {
			t.Fatalf("%s 应登记为 SideEffect，实际 %v", name, effect)
		}
		if note == "" {
			t.Fatalf("%s 的分类应带理由", name)
		}
	}

	if effect, _ := Classify("never.registered"); effect != Unclassified {
		t.Fatalf("未登记操作应为 Unclassified，实际 %v", effect)
	}
}

func TestRegisterRejectsUnclearDeclarations(t *testing.T) {
	if err := Register("", Pure); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空名称应拒绝: %v", err)
	}
	if err := Register("test.bad", Unclassified); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未定义分类应拒绝: %v", err)
	}
}

func TestPureOperationIsRecomputedAndVerified(t *testing.T) {
	ctx := context.Background()
	journal := Record()
	calc := func(value string) func(context.Context) ([]byte, string, error) {
		return func(context.Context) ([]byte, string, error) {
			return []byte(strings.ToUpper(value)), "", nil
		}
	}
	first, err := RunTyped(ctx, journal, 1, pureOp, Pure, []byte("in"), calc("abc"))
	if err != nil || string(first) != "ABC" {
		t.Fatalf("记录 first=%q err=%v", first, err)
	}
	if len(journal.Facts()) != 1 {
		t.Fatalf("纯计算同样要登记事实作为比对基准: %+v", journal.Facts())
	}

	replayJournal := Replay(journal.Facts())
	if got, err := RunTyped(ctx, replayJournal, 1, pureOp, Pure, []byte("in"), calc("abc")); err != nil || string(got) != "ABC" {
		t.Fatalf("一致重算应成功: got=%q err=%v", got, err)
	}

	drifted := Replay(journal.Facts())
	_, err = RunTyped(ctx, drifted, 1, pureOp, Pure, []byte("in"), calc("xyz"))
	if !errors.Is(err, errno.Invalid) {
		t.Fatalf("结果漂移应报分歧: %v", err)
	}
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("应报告 MismatchError: %v", err)
	}
	if mismatch.Reason == "" || !strings.Contains(mismatch.Error(), "第") {
		t.Fatalf("分歧应带原因与位置: %v", mismatch)
	}
}

func TestUnclassifiedOperationRefusesReplay(t *testing.T) {
	ctx := context.Background()
	ran := false
	run := func(context.Context) ([]byte, string, error) {
		ran = true
		return []byte("x"), "", nil
	}

	journal := Record()
	if _, err := Run(ctx, journal, 1, "unregistered.op", nil, run); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("记录模式应执行真实操作")
	}

	ran = false
	replayJournal := Replay(journal.Facts())
	if _, err := Run(ctx, replayJournal, 1, "unregistered.op", nil, run); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未分类操作回放应拒绝: %v", err)
	}
	if ran {
		t.Fatal("拒绝时绝不能执行真实操作")
	}
}

func TestRunUsesRegisteredClassification(t *testing.T) {
	ctx := context.Background()
	journal := Record()
	calls := 0
	run := func(context.Context) ([]byte, string, error) {
		calls++
		return []byte("recorded"), "", nil
	}
	if _, err := Run(ctx, journal, 7, OpEntropy, []byte("req"), run); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("记录模式应执行一次: %d", calls)
	}
	replayJournal := Replay(journal.Facts())
	output, err := Run(ctx, replayJournal, 7, OpEntropy, []byte("req"), run)
	if err != nil || string(output) != "recorded" {
		t.Fatalf("回放应复用记录结果: %q err=%v", output, err)
	}
	if calls != 1 {
		t.Fatalf("回放不得重新执行副作用操作: %d", calls)
	}
}

func TestRunTypedRejectsNilJournal(t *testing.T) {
	if _, err := RunTyped(context.Background(), nil, 1, pureOp, Pure, nil, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil Journal 应报 EINVAL: %v", err)
	}
}
