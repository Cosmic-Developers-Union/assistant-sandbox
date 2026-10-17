package shell

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/language"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func newTestSlot(t *testing.T, dir string) *Slot {
	t.Helper()
	kernel, parent, err := newKernel(dir, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := NewSlot(kernel, parent, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slot.Close(context.Background()) })
	return slot
}

func TestSlotKeepsStateAcrossSteps(t *testing.T) {
	ctx := context.Background()
	slot := newTestSlot(t, t.TempDir())

	if _, err := slot.Step(ctx, []byte("GREETING=hello; mkdir -p sub; cd sub")); err != nil {
		t.Fatal(err)
	}

	output, err := slot.Step(ctx, []byte("echo $GREETING; pwd"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(output.Data)
	if !strings.Contains(text, "hello") {
		t.Fatalf("变量未延续: %q", text)
	}
	if !strings.Contains(text, "sub") {
		t.Fatalf("当前目录未延续: %q", text)
	}
	if output.Status != 0 {
		t.Fatalf("status=%d", output.Status)
	}
}

func TestSlotReportsOutputPerStep(t *testing.T) {
	ctx := context.Background()
	slot := newTestSlot(t, t.TempDir())
	first, err := slot.Step(ctx, []byte("echo first"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Data) != "first\n" {
		t.Fatalf("第一次输出=%q", first.Data)
	}
	second, err := slot.Step(ctx, []byte("echo second"))
	if err != nil {
		t.Fatal(err)
	}
	if string(second.Data) != "second\n" {
		t.Fatalf("第二次输出=%q（不应包含上一次）", second.Data)
	}
}

func TestSlotReportsSyntaxErrorWithoutBreakingSlot(t *testing.T) {
	ctx := context.Background()
	slot := newTestSlot(t, t.TempDir())
	broken, err := slot.Step(ctx, []byte("if then"))
	if err != nil {
		t.Fatalf("语法错误不应是槽位故障: %v", err)
	}
	if broken.Status == 0 {
		t.Fatalf("语法错误应报非零状态: %+v", broken)
	}

	after, err := slot.Step(ctx, []byte("echo ok"))
	if err != nil || string(after.Data) != "ok\n" {
		t.Fatalf("槽位应仍可用: %q err=%v", after.Data, err)
	}
}

func TestSlotStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	slot := newTestSlot(t, dir)
	if _, err := slot.Step(ctx, []byte("mkdir -p deep/inner; cd deep/inner")); err != nil {
		t.Fatal(err)
	}
	state, err := slot.Encode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Format != language.StateFormat || state.Language != LanguageID {
		t.Fatalf("state=%+v", state)
	}

	if _, err := language.Decode(mustEncode(t, state)); err != nil {
		t.Fatal(err)
	}

	fresh := newTestSlot(t, dir)
	if err := fresh.Restore(ctx, state); err != nil {
		t.Fatal(err)
	}
	output, err := fresh.Step(ctx, []byte("pwd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output.Data), "inner") {
		t.Fatalf("恢复后目录不符: %q", output.Data)
	}

	if err := fresh.Restore(ctx, language.State{Language: "other"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("跨语言恢复应报 EINVAL: %v", err)
	}
}

func mustEncode(t *testing.T, state language.State) []byte {
	t.Helper()
	encoded, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestSlotClosedAndCancelled(t *testing.T) {
	ctx := context.Background()
	slot := newTestSlot(t, t.TempDir())
	if slot.Language() != LanguageID {
		t.Fatalf("language=%q", slot.Language())
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := slot.Step(cancelled, []byte("echo x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := slot.Encode(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := slot.Restore(cancelled, language.State{Language: LanguageID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := slot.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := slot.Close(ctx); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	if _, err := slot.Step(ctx, []byte("echo x")); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Step 应报 ECANCELED: %v", err)
	}
	if _, err := slot.Encode(ctx); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Encode 应报 ECANCELED: %v", err)
	}
	if err := slot.Restore(ctx, language.State{Language: LanguageID}); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Restore 应报 ECANCELED: %v", err)
	}
}

func TestSlotRejectsMissingKernel(t *testing.T) {
	if _, err := NewSlot(nil, proc.Proc{}, t.TempDir(), nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺少内核应报 EINVAL: %v", err)
	}
}
