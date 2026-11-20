package shell

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestQueuedStepCancellationDoesNotExecute 验证 P04:等待执行权的请求
// 在上下文中取消后不会在轮到它时执行。
func TestQueuedStepCancellationDoesNotExecute(t *testing.T) {
	dir := t.TempDir()
	slot := newTestSlot(t, dir)
	// 模拟一个正在执行的请求:先占住执行权。
	if err := slot.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := slot.Step(ctx, []byte("printf executed > queued.txt"))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("排队取消 err=%v want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("排队请求未随取消返回")
	}
	slot.release()
	if _, err := os.Stat(filepath.Join(dir, "queued.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("被取消的排队请求不应执行: %v", err)
	}
}

// TestConcurrentStepsStaySerialized 验证 P04:同一槽位的并发请求串行
// 执行,共享 Shell 变量与目录不被交错破坏。
func TestConcurrentStepsStaySerialized(t *testing.T) {
	slot := newTestSlot(t, t.TempDir())
	const requests = 8
	var wait sync.WaitGroup
	for index := 0; index < requests; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := slot.Step(context.Background(), []byte("n=$((n+1))")); err != nil {
				t.Errorf("并发步进: %v", err)
			}
		}()
	}
	wait.Wait()
	output, err := slot.Step(context.Background(), []byte("echo $n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output.Data)); got != "8" {
		t.Fatalf("并发计数=%q want 8(Shell 状态被交错破坏)", got)
	}
}

// TestSlotExitStateAndExplicitRestart 验证 P14:exit 后会话进入可查询
// 的退出状态,后续 Step 返回稳定 ErrExited 而不是伪装成功;只有显式
// Restart 才重建 Bash,且不继承旧目录/变量。
func TestSlotExitStateAndExplicitRestart(t *testing.T) {
	dir := t.TempDir()
	slot := newTestSlot(t, dir)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := slot.Step(context.Background(), []byte("cd sub; export KEEP=old; exit 7"))
	if err != nil || output.Status != 7 {
		t.Fatalf("exit 结果=%+v err=%v want status 7", output, err)
	}
	state, err := slot.State(context.Background())
	if err != nil || !state.Exited || state.ExitCode != 7 {
		t.Fatalf("退出状态=%+v err=%v", state, err)
	}
	if _, err := slot.Step(context.Background(), []byte("echo must-not-run")); !errors.Is(err, ErrExited) {
		t.Fatalf("退出后 Step err=%v want ErrExited", err)
	}
	if err := slot.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = slot.State(context.Background())
	if err != nil || state.Exited || state.ExitCode != 0 {
		t.Fatalf("重启后状态=%+v err=%v", state, err)
	}
	output, err = slot.Step(context.Background(), []byte("printf '%s:%s' \"${KEEP-unset}\" \"${PWD}\""))
	if err != nil {
		t.Fatal(err)
	}
	if want := "unset:" + dir; strings.TrimSpace(string(output.Data)) != want {
		t.Fatalf("重启后目录/变量=%q want %q", string(output.Data), want)
	}
}
