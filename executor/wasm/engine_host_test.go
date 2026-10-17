//go:build !wasip1

package wasm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func wasmModule(body []byte) []byte {
	module := append(header(), section(SectionType, []byte{0x01, 0x60, 0x00, 0x00})...)
	module = append(module, section(SectionFunction, []byte{0x01, 0x00})...)

	exportBody := append(uvarint(1), name("_start")...)
	exportBody = append(exportBody, 0x00)
	exportBody = append(exportBody, uvarint(0)...)
	module = append(module, section(SectionExport, exportBody)...)

	codeBody := append(uvarint(1), uvarint(uint64(len(body)+2))...)
	codeBody = append(codeBody, 0x00)
	codeBody = append(codeBody, body...)
	codeBody = append(codeBody, 0x0b)
	return append(module, section(SectionCode, codeBody)...)
}

func infiniteLoopWASM(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "infinite_loop.wasm"))
	if err != nil {
		t.Fatalf("缺少夹具: %v", err)
	}
	return raw
}

func exitingModule() []byte { return wasmModule(nil) }

func newTestEngine(t *testing.T) *HostEngine {
	t.Helper()
	engine, err := NewHostEngine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func incompleteFunctions() []byte {
	module := append(header(), section(SectionType, []byte{0x01, 0x60, 0x00, 0x00})...)
	module = append(module, section(SectionFunction, []byte{0x02, 0x00, 0x00})...)
	return append(module, section(SectionCode, []byte{0x01, 0x02, 0x00, 0x0b})...)
}

func analyzeModule(t *testing.T, raw []byte) *Module {
	t.Helper()
	parsed, err := New(Budgets{MaxBytes: 1 << 20, MaxImports: 64, MaxExports: 64, MaxSections: 32}).
		Analyze(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestInstantiateNormalExit(t *testing.T) {
	engine := newTestEngine(t)
	raw := exitingModule()
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{Name: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := instance.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != Exited || result.Code != 0 || result.Signal != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestInterruptStopsInfiniteLoop(t *testing.T) {
	engine := newTestEngine(t)
	raw := infiniteLoopWASM(t)
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{Name: "loop", Entry: "infinite_loop"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()

	early, cancelEarly := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelEarly()
	if _, err := instance.Wait(early); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("无限循环不应自行结束: %v", err)
	}
	if err := instance.Interrupt(signal.TERM); err != nil {
		t.Fatal(err)
	}

	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := instance.Wait(deadline)
	if err != nil {
		t.Fatalf("中断后应结束: %v", err)
	}
	if result.Reason != Interrupted || result.Signal != signal.TERM {
		t.Fatalf("result=%+v", result)
	}
	if result.Code != signal.ExitCode(signal.TERM) {
		t.Fatalf("中断退出码=%d want %d", result.Code, signal.ExitCode(signal.TERM))
	}
}

func TestCloseStopsRunningInstance(t *testing.T) {
	engine := newTestEngine(t)
	raw := infiniteLoopWASM(t)
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{Name: "closeloop", Entry: "infinite_loop"})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}

	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := instance.Wait(deadline)
	if err != nil {
		t.Fatalf("Close 后 Wait 应立即返回: %v", err)
	}
	if result.Reason != Interrupted {
		t.Fatalf("Close 应记为中断: %+v", result)
	}

	if err := instance.Close(); err != nil {
		t.Fatalf("重复 Close 应幂等: %v", err)
	}

	if err := instance.Interrupt(signal.TERM); !errors.Is(err, errno.Busy) {
		t.Fatalf("已结束实例的中断应报 EBUSY: %v", err)
	}
}

func TestInstantiateRejectsMalformedModule(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	structural := map[string][]byte{
		"空":   nil,
		"魔数错": {0x01, 0x02, 0x03, 0x04},
		"版本错": append(append([]byte{}, magic...), 0x02, 0x00, 0x00, 0x00),
	}
	for label, raw := range structural {
		if _, err := New(DefaultBudgets()).Analyze(ctx, raw); err == nil {
			t.Errorf("%s: 入账层应拒绝", label)
			continue
		}
		if _, err := engine.Instantiate(ctx, &Module{Digest: label}, raw, Config{Name: label}); err == nil {
			t.Errorf("%s: 引擎应拒绝编译", label)
		}
	}

	semantic := map[string][]byte{
		"段内容非法": append(header(), section(SectionType, []byte{0x7f})...),

		"函数体数量不符": incompleteFunctions(),
	}
	for label, raw := range semantic {
		if _, err := New(DefaultBudgets()).Analyze(ctx, raw); err != nil {
			t.Errorf("%s: 入账层只做结构校验，不应拒绝: %v", label, err)
		}
		if _, err := engine.Instantiate(ctx, &Module{Digest: label}, raw, Config{Name: label}); err == nil {
			t.Errorf("%s: 引擎应拒绝编译", label)
		}
	}

	empty := header()
	if _, err := New(DefaultBudgets()).Analyze(ctx, empty); err != nil {
		t.Fatalf("空模块结构合法: %v", err)
	}
	instance, err := engine.Instantiate(ctx, &Module{Digest: "empty"}, empty, Config{Name: "empty"})
	if err != nil {
		t.Fatalf("空模块应能实例化: %v", err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, waitErr := instance.Wait(ctx); waitErr == nil {
		t.Fatal("缺少入口必须报错")
	}

	if _, err := engine.Instantiate(ctx, nil, []byte{0x00}, Config{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 模块应报 EINVAL: %v", err)
	}
	if _, err := engine.Instantiate(ctx, &Module{}, nil, Config{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空字节应报 EINVAL: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := engine.Instantiate(cancelled, &Module{}, exitingModule(), Config{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestInstantiateAfterEngineClose(t *testing.T) {
	engine, err := NewHostEngine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	raw := exitingModule()
	if _, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{}); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后启动应报 ECANCELED: %v", err)
	}
}

func TestInstanceStdoutIsCaptured(t *testing.T) {
	engine := newTestEngine(t)

	raw := exitingModule()
	var output bytes.Buffer
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{Name: "out", Stdout: &output})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := instance.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("无输出模块不应写入: %q", output.String())
	}

	quiet, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{Name: "quiet"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := quiet.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := quiet.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReasonStrings(t *testing.T) {
	for reason, want := range map[Reason]string{Exited: "exited", Interrupted: "interrupted", Fault: "fault"} {
		if got := reason.String(); got != want {
			t.Errorf("Reason(%d).String()=%q want %q", reason, got, want)
		}
	}
	if got := Reason(99).String(); got != "unknown" {
		t.Errorf("未知原因=%q", got)
	}
	if !strings.Contains(ErrNoEngine.Error(), "ENOTSUP") {
		t.Errorf("ErrNoEngine 应带 ENOTSUP 身份: %v", ErrNoEngine)
	}
}

func procExitModule() []byte {

	typeBody := []byte{0x02, 0x60, 0x00, 0x00, 0x60, 0x01, 0x7f, 0x00}

	importBody := append(uvarint(1), name("wasi_snapshot_preview1")...)
	importBody = append(importBody, name("proc_exit")...)
	importBody = append(importBody, 0x00, 0x01)

	functionBody := []byte{0x01, 0x00}

	exportBody := append(uvarint(1), name("_start")...)
	exportBody = append(exportBody, 0x00, 0x01)

	code := []byte{0x41, 0x2a, 0x10, 0x00, 0x0b}
	codeBody := append(uvarint(1), uvarint(uint64(len(code)+1))...)
	codeBody = append(codeBody, 0x00)
	codeBody = append(codeBody, code...)

	module := append(header(), section(SectionType, typeBody)...)
	module = append(module, section(SectionImport, importBody)...)
	module = append(module, section(SectionFunction, functionBody)...)
	module = append(module, section(SectionExport, exportBody)...)
	return append(module, section(SectionCode, codeBody)...)
}

func TestInstantiateReportsExplicitExitCode(t *testing.T) {
	engine := newTestEngine(t)
	raw := procExitModule()
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{Name: "exit42"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := instance.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != Exited || result.Code != 42 || result.Signal != 0 {
		t.Fatalf("result=%+v want code=42 Exited", result)
	}
}

func TestCutEnv(t *testing.T) {
	if name, value, ok := cutEnv("KEY=value=with=equals"); !ok || name != "KEY" || value != "value=with=equals" {
		t.Fatalf("cutEnv=%q,%q,%v", name, value, ok)
	}
	if name, value, ok := cutEnv("EMPTY="); !ok || name != "EMPTY" || value != "" {
		t.Fatalf("空值应可解析: %q,%q,%v", name, value, ok)
	}
	if _, _, ok := cutEnv("NOEQUALS"); ok {
		t.Fatal("缺少 = 的项应报告失败")
	}
}

func TestInstantiateWithEnvAndDir(t *testing.T) {
	engine := newTestEngine(t)
	raw := exitingModule()
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{
		Name: "env",
		Args: []string{"prog", "arg"},
		Env:  []string{"KEY=value", "NOEQUALS"},
		Dir:  "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := instance.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWriterAdapterForwards(t *testing.T) {
	var buffer bytes.Buffer
	adapter := writerAdapter{&buffer}
	if n, err := adapter.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if buffer.String() != "hello" {
		t.Fatalf("buffer=%q", buffer.String())
	}

	if n, err := (discardWriter{}).Write([]byte("ignored")); err != nil || n != 7 {
		t.Fatalf("discardWriter n=%d err=%v", n, err)
	}
}

func TestHostEngineOnCancelledContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	engine, err := NewHostEngine(cancelled)
	if err != nil {
		t.Skipf("本运行时拒绝已取消上下文的装配: %v", err)
	}
	defer func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	}()
	raw := exitingModule()
	if _, err := engine.Instantiate(cancelled, &Module{Digest: "x"}, raw, Config{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消上下文启动应失败: %v", err)
	}
}

func memoryHeavyModule() []byte {

	module := append(header(), section(SectionType, []byte{0x01, 0x60, 0x00, 0x00})...)
	module = append(module, section(SectionFunction, []byte{0x01, 0x00})...)

	module = append(module, section(SectionMemory, []byte{0x01, 0x01, 0x02, 0x03})...)
	exportBody := append(uvarint(1), name("_start")...)
	exportBody = append(exportBody, 0x00, 0x00)
	module = append(module, section(SectionExport, exportBody)...)
	return append(module, section(SectionCode, []byte{0x01, 0x02, 0x00, 0x0b})...)
}

func TestMemoryBudgetRejectsOversizedModule(t *testing.T) {
	ctx := context.Background()
	raw := memoryHeavyModule()
	parsed := analyzeModule(t, raw)

	tight, err := NewHostEngineWithMemoryLimit(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tight.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := tight.Instantiate(ctx, parsed, raw, Config{Name: "tight"}); !errors.Is(err, errno.Limit) && err == nil {
		t.Fatalf("内存超预算应拒绝: %v", err)
	}

	roomy, err := NewHostEngineWithMemoryLimit(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := roomy.Close(); err != nil {
			t.Error(err)
		}
	}()
	instance, err := roomy.Instantiate(ctx, parsed, raw, Config{Name: "roomy"})
	if err != nil {
		t.Fatalf("预算足够应可启动: %v", err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	if result, err := instance.Wait(ctx); err != nil || result.Reason != Exited {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func writeModule(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "write_hello.wasm"))
	if err != nil {
		t.Fatalf("缺少夹具: %v", err)
	}
	return raw
}

func TestOutputBudgetTerminatesModule(t *testing.T) {
	engine := newTestEngine(t)
	raw := writeModule(t)
	var sink bytes.Buffer
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{
		Name: "quota", Stdout: &sink, MaxOutputBytes: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := instance.Wait(context.Background())

	if !errors.Is(err, errno.Limit) {
		t.Fatalf("预算 trap 应带 EAGAIN 身份: %v", err)
	}
	if result.Reason != Quota {
		t.Fatalf("result=%+v want Quota", result)
	}
	if sink.Len() != 0 {
		t.Fatalf("超限写入不得落盘: %q", sink.String())
	}
}

func TestOutputWithinBudgetIsCaptured(t *testing.T) {
	engine := newTestEngine(t)
	raw := writeModule(t)
	var sink bytes.Buffer
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{
		Name: "within", Stdout: &sink, MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := instance.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != Exited {
		t.Fatalf("result=%+v want Exited", result)
	}
	if sink.String() != "hello" {
		t.Fatalf("输出=%q want %q", sink.String(), "hello")
	}
}

type chargingWriter struct {
	target Writer
	budget *outputBudget
}

func (w *chargingWriter) Write(p []byte) (int, error) {
	if err := w.budget.charge(int64(len(p))); err != nil {
		return 0, err
	}
	return w.target.Write(p)
}

func TestOutputBudgetChargesSharedBudget(t *testing.T) {
	budget := newOutputBudget(10)
	var first, second bytes.Buffer

	stdout := &chargingWriter{target: &first, budget: budget}
	stderr := &chargingWriter{target: &second, budget: budget}

	if n, err := stdout.Write([]byte("123456")); err != nil || n != 6 {
		t.Fatalf("stdout n=%d err=%v", n, err)
	}
	if n, err := stderr.Write([]byte("7890")); err != nil || n != 4 {
		t.Fatalf("stderr n=%d err=%v", n, err)
	}

	if _, err := stderr.Write([]byte("x")); !errors.Is(err, errno.Limit) {
		t.Fatalf("超出共享预算应报 EAGAIN: %v", err)
	}
	if first.String() != "123456" || second.String() != "7890" {
		t.Fatalf("超限写入不得落盘: %q %q", first.String(), second.String())
	}
}

func TestOutputBudgetUnlimitedAndExact(t *testing.T) {
	unlimited := &chargingWriter{target: &bytes.Buffer{}, budget: newOutputBudget(0)}
	if _, err := unlimited.Write(make([]byte, 4096)); err != nil {
		t.Fatalf("预算为 0 应不限制: %v", err)
	}
	exact := &chargingWriter{target: &bytes.Buffer{}, budget: newOutputBudget(4)}
	if _, err := exact.Write([]byte("abcd")); err != nil {
		t.Fatalf("恰好用完预算应允许: %v", err)
	}
	if _, err := exact.Write([]byte("e")); !errors.Is(err, errno.Limit) {
		t.Fatalf("用尽后再写应报 EAGAIN: %v", err)
	}
}

func TestCancelChannelTerminatesModule(t *testing.T) {
	engine := newTestEngine(t)
	raw := infiniteLoopWASM(t)
	cancel := make(chan struct{})
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{
		Name: "cancel", Entry: "infinite_loop", Cancel: cancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	close(cancel)
	deadline, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	result, err := instance.Wait(deadline)
	if err != nil {
		t.Fatalf("外层取消后应结束: %v", err)
	}
	if result.Reason != Interrupted {
		t.Fatalf("result=%+v want Interrupted", result)
	}
}

func TestQuotaReasonString(t *testing.T) {
	for reason, want := range map[Reason]string{Exited: "exited", Interrupted: "interrupted", Fault: "fault", Quota: "quota"} {
		if got := reason.String(); got != want {
			t.Errorf("Reason(%d).String()=%q want %q", reason, got, want)
		}
	}
}
