package wasm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func hostCallModule(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "host_call.wasm"))
	if err != nil {
		t.Fatalf("缺少夹具: %v", err)
	}
	return raw
}

type waitBinding struct {
	mu      sync.Mutex
	args    []uint64
	entered chan struct{}

	release chan struct{}
	ctxErr  error
}

func (b *waitBinding) handler(ctx context.Context, memory GuestMemory, args []uint64) error {
	b.mu.Lock()
	b.args = append([]uint64(nil), args...)
	b.mu.Unlock()
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	if b.release != nil {
		select {
		case <-b.release:
			return nil
		case <-ctx.Done():
		}
	}
	<-ctx.Done()
	b.mu.Lock()
	b.ctxErr = ctx.Err()
	b.mu.Unlock()
	return errors.Join(ErrHostCallCancelled, ctx.Err())
}

func (b *waitBinding) binding() HostBinding {
	return HostBinding{
		Module:  "testhost",
		Entry:   "wait",
		Params:  []ValueType{TypeI32},
		Handler: b.handler,
	}
}

func TestHostCallObservesInterrupt(t *testing.T) {
	engine := newTestEngine(t)
	raw := hostCallModule(t)
	host := &waitBinding{entered: make(chan struct{})}
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{
		Name:     "hostcall",
		Hosts:    registryWithHost(t, host.binding()),
		Bindings: []HostBinding{host.binding()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()

	select {
	case <-host.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("宿主函数未被调用")
	}
	host.mu.Lock()
	if len(host.args) != 1 || host.args[0] != 7 {
		host.mu.Unlock()
		t.Fatalf("宿主函数参数=%v want [7]", host.args)
	}
	host.mu.Unlock()

	if err := instance.Interrupt(signal.TERM); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := instance.Wait(deadline); err != nil {
		t.Fatalf("中断后应结束: %v", err)
	}
	host.mu.Lock()
	ctxErr := host.ctxErr
	host.mu.Unlock()
	if !errors.Is(ctxErr, context.Canceled) {
		t.Fatalf("宿主函数应观察到取消，实际 %v", ctxErr)
	}
}

func TestHostCallErrorBecomesTrap(t *testing.T) {
	engine := newTestEngine(t)
	raw := hostCallModule(t)
	host := &waitBinding{entered: make(chan struct{}), release: make(chan struct{})}

	failing := HostBinding{
		Module: "testhost", Entry: "wait", Params: []ValueType{TypeI32},
		Handler: func(context.Context, GuestMemory, []uint64) error {
			return errno.Wrap(errno.Permission, "宿主函数拒绝")
		},
	}
	instance, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{
		Name:     "hostfail",
		Hosts:    registryWithHost(t, failing),
		Bindings: []HostBinding{failing},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, waitErr := instance.Wait(context.Background())
	if waitErr == nil {
		t.Fatal("宿主函数失败应导致 trap")
	}
	if result.Reason != Fault {
		t.Fatalf("result=%+v want Fault", result)
	}
	_ = host
}

func TestBindingsRejectInvalidShape(t *testing.T) {
	engine := newTestEngine(t)
	raw := hostCallModule(t)
	module := analyzeModule(t, raw)
	ctx := context.Background()
	for _, tc := range []struct {
		label   string
		binding HostBinding
	}{
		{"空模块名", HostBinding{Entry: "wait", Handler: func(context.Context, GuestMemory, []uint64) error { return nil }}},
		{"空条目名", HostBinding{Module: "testhost", Handler: func(context.Context, GuestMemory, []uint64) error { return nil }}},
		{"缺实现", HostBinding{Module: "testhost", Entry: "wait"}},
	} {
		if _, err := engine.Instantiate(ctx, module, raw, Config{
			Name: tc.label, Bindings: []HostBinding{tc.binding},
		}); !errors.Is(err, errno.Invalid) {
			t.Errorf("%s: 应报 EINVAL，实际 %v", tc.label, err)
		}
	}

	other := HostBinding{
		Module: "testhost", Entry: "other", Params: []ValueType{TypeI32},
		Handler: func(context.Context, GuestMemory, []uint64) error { return nil },
	}
	if _, err := engine.Instantiate(ctx, module, raw, Config{
		Name: "mismatch", Hosts: registryWithHost(t, other), Bindings: []HostBinding{other},
	}); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("未登记 import 应报 ENOTSUP: %v", err)
	}
}

func registryWithHost(t *testing.T, bindings ...HostBinding) *Registry {
	t.Helper()
	registry := NewRegistry()
	for _, binding := range bindings {
		if err := registry.Register(binding.Module, binding.Entry, binding.Handler); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func TestValueTypeNamesAreStable(t *testing.T) {
	for value, want := range map[ValueType]string{
		TypeI32: "i32", TypeI64: "i64", TypeF32: "f32", TypeF64: "f64",
	} {
		if got := value.String(); got != want {
			t.Errorf("ValueType(%d).String()=%q want %q", value, got, want)
		}
	}
	if got := ValueType(9).String(); got != "type(9)" {
		t.Fatalf("未知类型=%q", got)
	}
}

func TestCheckCancelled(t *testing.T) {
	if err := checkCancelled(context.Background()); err != nil {
		t.Fatalf("未取消应通过: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err := checkCancelled(cancelled)
	if !errors.Is(err, errno.Cancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消应同时带 ECANCELED 与取消身份: %v", err)
	}
	if err := checkCancelled(nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 上下文应报 EINVAL: %v", err)
	}
}
