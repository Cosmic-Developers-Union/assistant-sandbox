package wasm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type fakeMemory struct {
	size uint32
	data []byte
}

func (m fakeMemory) Size() uint32 { return m.size }

func (m fakeMemory) Read(offset, length uint32) ([]byte, error) {
	if length == 0 {
		if offset > m.size {
			return nil, errno.Wrap(errno.Range, "越界")
		}
		return nil, nil
	}
	if offset >= m.size || length > m.size-offset {
		return nil, errno.Wrap(errno.Range, "越界")
	}

	window := make([]byte, length)
	if start := int(offset); start < len(m.data) {
		copy(window, m.data[start:])
	}
	return window, nil
}

func TestReadBytesRejectsOutOfRangePointers(t *testing.T) {
	memory := fakeMemory{size: 16, data: []byte("0123456789abcdef")}
	if _, err := ReadBytes(memory, 8, 8); err != nil {
		t.Fatalf("恰好到边界应允许: %v", err)
	}
	for _, tc := range []struct {
		label        string
		offset, size uint32
	}{
		{"起点等于大小", 16, 1},
		{"起点超出", 17, 1},
		{"跨越末端", 8, 9},
		{"长度巨大", 0, 1 << 31},
		{"加法溢出", 1 << 31, 1 << 31},
		{"最大溢出", ^uint32(0), 8},
	} {
		if _, err := ReadBytes(memory, tc.offset, tc.size); !errors.Is(err, errno.Range) {
			t.Errorf("%s: 应报 ERANGE，实际 %v", tc.label, err)
		}
	}

	if view, err := ReadBytes(memory, 16, 0); err != nil || len(view) != 0 {
		t.Fatalf("空读应允许: %v %v", view, err)
	}

	if _, err := ReadBytes(nil, 0, 1); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 内存应报 EINVAL: %v", err)
	}
}

func TestReadBytesCopiesDoNotAliasGuestMemory(t *testing.T) {
	backing := []byte("0123456789abcdef")
	memory := fakeMemory{size: 16, data: backing}
	view, err := ReadBytes(memory, 0, 4)
	if err != nil {
		t.Fatal(err)
	}

	copy(backing[0:4], []byte("XXXX"))
	if string(view) != "0123" {
		t.Fatalf("副本被别名修改: %q", view)
	}
}

func TestReadStringHonoursBudgetAndTerminator(t *testing.T) {
	memory := fakeMemory{size: 32, data: append([]byte("hello\x00trailing"), make([]byte, 16)...)}
	value, err := ReadString(memory, 0, 32)
	if err != nil || value != "hello" {
		t.Fatalf("value=%q err=%v", value, err)
	}

	if _, err := ReadString(memory, 0, 3); !errors.Is(err, errno.Range) {
		t.Fatalf("超预算应报 ERANGE: %v", err)
	}

	if value, err := ReadString(memory, 0, 6); err != nil || value != "hello" {
		t.Fatalf("恰好覆盖终止符应成功: %q %v", value, err)
	}

	if _, err := ReadString(memory, 64, 8); !errors.Is(err, errno.Range) {
		t.Fatalf("越界起点应报 ERANGE: %v", err)
	}

	unterminated := fakeMemory{size: 8, data: []byte("abcdefgh")}
	if _, err := ReadString(unterminated, 0, 8); !errors.Is(err, errno.Range) {
		t.Fatalf("缺终止符应报 ERANGE: %v", err)
	}

	if _, err := ReadString(nil, 0, 8); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 内存应报 EINVAL: %v", err)
	}
	if _, err := ReadString(memory, 0, 0); !errors.Is(err, errno.Invalid) {
		t.Fatalf("零预算应报 EINVAL: %v", err)
	}
}

func TestRegistryRejectsUnknownAndInvalidEntries(t *testing.T) {
	registry := NewRegistry()
	host := func(context.Context, api.Module, uint32) uint32 { return 0 }
	if err := registry.Register("env", "read", host); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("env", "read", host); !errors.Is(err, errno.Exist) {
		t.Fatalf("重复登记应报 EEXIST: %v", err)
	}
	if err := registry.Register("", "read", host); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空模块名应报 EINVAL: %v", err)
	}
	if err := registry.Register("env", "", host); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空条目名应报 EINVAL: %v", err)
	}
	if err := registry.Register("env", "other", nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 实现应报 EINVAL: %v", err)
	}
	if err := registry.Register("env", "other", 42); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非函数应报 EINVAL: %v", err)
	}
	if _, ok := registry.Lookup("env", "missing"); ok {
		t.Fatal("未登记条目不应可解析")
	}
	if names := registry.Entries("env"); len(names) != 1 || names[0] != "read" {
		t.Fatalf("entries=%v", names)
	}
	if modules := registry.Modules(); len(modules) != 1 || modules[0] != "env" {
		t.Fatalf("modules=%v", modules)
	}
}

func TestValidateImportsListsMissingEntries(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("env", "known", func() {}); err != nil {
		t.Fatal(err)
	}
	module := &Module{Imports: []Import{
		{Module: "env", Name: "known"},
		{Module: "env", Name: "missing"},
		{Module: "wasi_snapshot_preview1", Name: "fd_write"},
	}}
	missing := registry.ValidateImports(module)
	if len(missing) != 2 {
		t.Fatalf("missing=%+v", missing)
	}
	if missing[0].Name != "missing" || missing[1].Module != "wasi_snapshot_preview1" {
		t.Fatalf("missing=%+v", missing)
	}

	if got := registry.ValidateImports(nil); got != nil {
		t.Fatalf("nil 模块=%+v", got)
	}
}

func TestWazeroMemoryAdapterAgainstRealModule(t *testing.T) {

	raw, err := os.ReadFile(filepath.Join("testdata", "memory_export.wasm"))
	if err != nil {
		t.Fatalf("缺少夹具: %v", err)
	}
	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	defer func() {
		if err := runtime.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	module, err := runtime.InstantiateWithConfig(ctx, raw, wazero.NewModuleConfig().WithName("mem"))
	if err != nil {
		t.Fatalf("实例化夹具失败: %v", err)
	}
	memory := module.Memory()
	if memory == nil {
		t.Fatal("夹具必须导出内存")
	}
	adapter := wazeroMemory{memory: memory}
	size := adapter.Size()
	if size != 65536 {
		t.Fatalf("内存大小=%d want 65536", size)
	}

	if view, err := adapter.Read(0, 5); err != nil || string(view) != "hello" {
		t.Fatalf("读取真实数据失败: %q %v", view, err)
	}

	if copied, err := ReadBytes(adapter, 0, 5); err != nil || string(copied) != "hello" {
		t.Fatalf("ReadBytes=%q %v", copied, err)
	}

	if _, err := adapter.Read(0, size); err != nil {
		t.Fatalf("整块读取应允许: %v", err)
	}
	if _, err := adapter.Read(size, 1); !errors.Is(err, errno.Range) {
		t.Fatalf("越过末端应报 ERANGE: %v", err)
	}
	if _, err := adapter.Read(size-1, 2); !errors.Is(err, errno.Range) {
		t.Fatalf("跨末端应报 ERANGE: %v", err)
	}
	if _, err := adapter.Read(^uint32(0), 1); !errors.Is(err, errno.Range) {
		t.Fatalf("溢出指针应报 ERANGE: %v", err)
	}

	if _, err := adapter.Read(size, 0); err != nil {
		t.Fatalf("末端空读应允许: %v", err)
	}
	if _, err := adapter.Read(size+1, 0); !errors.Is(err, errno.Range) {
		t.Fatalf("末端外空读应报 ERANGE: %v", err)
	}

	if _, err := ReadString(adapter, size, 4); !errors.Is(err, errno.Range) {
		t.Fatalf("越界字符串应报 ERANGE: %v", err)
	}
}

func TestInstantiateRejectsUnregisteredImports(t *testing.T) {
	engine := newTestEngine(t)
	raw := writeModule(t)
	registry := NewRegistry()

	if _, err := engine.Instantiate(context.Background(), analyzeModule(t, raw), raw, Config{
		Name: "unregistered", Hosts: registry,
	}); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("未登记 import 应报 ENOTSUP: %v", err)
	}
}
