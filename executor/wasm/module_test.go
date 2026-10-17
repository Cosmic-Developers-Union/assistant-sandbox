package wasm

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func uvarint(value uint64) []byte {
	var buffer [binary.MaxVarintLen64]byte
	return buffer[:binary.PutUvarint(buffer[:], value)]
}

func name(value string) []byte {
	return append(uvarint(uint64(len(value))), value...)
}

func section(id SectionID, body []byte) []byte {
	out := []byte{byte(id)}
	out = append(out, uvarint(uint64(len(body)))...)
	return append(out, body...)
}

func header() []byte { return append(append([]byte{}, magic...), version...) }

func TestAnalyzeRejectsMalformedModules(t *testing.T) {
	analyzer := New(DefaultBudgets())
	ctx := context.Background()
	cases := map[string][]byte{
		"空":           nil,
		"过短":          {0x00, 0x61},
		"魔数错":         append([]byte{0x01, 0x61, 0x73, 0x6d}, version...),
		"版本错":         append(append([]byte{}, magic...), 0x02, 0x00, 0x00, 0x00),
		"段长度越界":       append(header(), 0x01, 0x7f),
		"段长度损坏":       append(header(), 0x01, 0xff, 0xff, 0xff, 0xff, 0xff),
		"import 段损坏":  append(header(), section(SectionImport, []byte{0x01})...),
		"export 段损坏":  append(header(), section(SectionExport, []byte{0x01})...),
		"export 名字越界": append(header(), section(SectionExport, append(uvarint(1), append(uvarint(9), []byte("ab")...)...))...),
	}
	for label, module := range cases {
		if _, err := analyzer.Analyze(ctx, module); !errors.Is(err, errno.NoExec) {
			t.Errorf("%s: err=%v want ENOEXEC", label, err)
		}
	}
}

func TestAnalyzeRejectsBadSectionOrder(t *testing.T) {
	analyzer := New(DefaultBudgets())
	empty := section(SectionType, []byte{0x00})

	repeated := append(header(), empty...)
	repeated = append(repeated, empty...)
	if _, err := analyzer.Analyze(context.Background(), repeated); !errors.Is(err, errno.NoExec) {
		t.Fatalf("重复段应报 ENOEXEC: %v", err)
	}

	outOfOrder := append(header(), section(SectionExport, []byte{0x00})...)
	outOfOrder = append(outOfOrder, section(SectionImport, []byte{0x00})...)
	if _, err := analyzer.Analyze(context.Background(), outOfOrder); !errors.Is(err, errno.NoExec) {
		t.Fatalf("乱序段应报 ENOEXEC: %v", err)
	}

	custom := section(SectionCustom, append(name("x"), 0x01))
	valid := append(header(), custom...)
	valid = append(valid, section(SectionType, []byte{0x00})...)
	valid = append(valid, custom...)
	if _, err := analyzer.Analyze(context.Background(), valid); err != nil {
		t.Fatalf("custom 段重复应合法: %v", err)
	}
}

func TestAnalyzeParsesImportAndExportSurface(t *testing.T) {

	importBody := append(uvarint(1), name("env")...)
	importBody = append(importBody, name("abort")...)
	importBody = append(importBody, 0x00)
	importBody = append(importBody, uvarint(0)...)

	exportBody := append(uvarint(1), name("_start")...)
	exportBody = append(exportBody, 0x00)
	exportBody = append(exportBody, uvarint(0)...)

	module := append(header(), section(SectionImport, importBody)...)
	module = append(module, section(SectionExport, exportBody)...)
	analyzer := New(DefaultBudgets())
	parsed, err := analyzer.Analyze(context.Background(), module)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Imports) != 1 || parsed.Imports[0].Module != "env" || parsed.Imports[0].Name != "abort" || parsed.Imports[0].Kind != 0 {
		t.Fatalf("imports=%+v", parsed.Imports)
	}
	if len(parsed.Exports) != 1 || parsed.Exports[0].Name != "_start" || parsed.Exports[0].Kind != 0 {
		t.Fatalf("exports=%+v", parsed.Exports)
	}
	if parsed.Size != len(module) || parsed.Digest == "" {
		t.Fatalf("size=%d digest=%q", parsed.Size, parsed.Digest)
	}
}

func TestAnalyzeParsesRealGuestModule(t *testing.T) {
	path := filepath.Join("..", "..", "assistant-bash.wasm")
	module, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("真实客体模块不可用: %v", err)
	}
	analyzer := New(Budgets{MaxBytes: 64 << 20, MaxImports: 4096, MaxExports: 4096, MaxSections: 64})
	parsed, err := analyzer.Analyze(context.Background(), module)
	if err != nil {
		t.Fatalf("真实模块应通过入账校验: %v", err)
	}
	if parsed.Size != len(module) {
		t.Fatalf("size=%d want %d", parsed.Size, len(module))
	}

	var foundStart bool
	for _, exported := range parsed.Exports {
		if exported.Name == "_start" {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatalf("真实模块应导出 _start: %d 个导出", len(parsed.Exports))
	}
	if len(parsed.Imports) == 0 {
		t.Fatal("真实 WASI 模块应有 import")
	}

	var sawMemory bool
	for _, id := range parsed.Sections {
		if id == SectionMemory {
			sawMemory = true
		}
	}
	if !sawMemory {
		t.Fatalf("真实模块应含内存段: %v", parsed.Sections)
	}
}

func TestAnalyzeEnforcesBudgets(t *testing.T) {
	module := append(header(), section(SectionCustom, name("x"))...)

	if _, err := New(Budgets{MaxBytes: 4}).Analyze(context.Background(), module); !errors.Is(err, errno.Limit) {
		t.Fatalf("体积超限应报 EAGAIN: %v", err)
	}

	body := append(header(), section(SectionCustom, name("a"))...)
	body = append(body, section(SectionCustom, name("b"))...)
	body = append(body, section(SectionCustom, name("c"))...)
	if _, err := New(Budgets{MaxSections: 2}).Analyze(context.Background(), body); !errors.Is(err, errno.Limit) {
		t.Fatalf("段数超限应报 EAGAIN: %v", err)
	}

	importBody := append(uvarint(2), name("env")...)
	importBody = append(importBody, name("a")...)
	importBody = append(importBody, 0x00)
	importBody = append(importBody, uvarint(0)...)
	importBody = append(importBody, name("env")...)
	importBody = append(importBody, name("b")...)
	importBody = append(importBody, 0x00)
	importBody = append(importBody, uvarint(0)...)
	withImports := append(header(), section(SectionImport, importBody)...)
	if _, err := New(Budgets{MaxImports: 1}).Analyze(context.Background(), withImports); !errors.Is(err, errno.Limit) {
		t.Fatalf("import 超限应报 EAGAIN: %v", err)
	}

	exportBody := append(uvarint(2), name("a")...)
	exportBody = append(exportBody, 0x00)
	exportBody = append(exportBody, uvarint(0)...)
	exportBody = append(exportBody, name("b")...)
	exportBody = append(exportBody, 0x00)
	exportBody = append(exportBody, uvarint(0)...)
	withExports := append(header(), section(SectionExport, exportBody)...)
	if _, err := New(Budgets{MaxExports: 1}).Analyze(context.Background(), withExports); !errors.Is(err, errno.Limit) {
		t.Fatalf("export 超限应报 EAGAIN: %v", err)
	}
}

func TestAnalyzeCachesByContentIdentity(t *testing.T) {
	analyzer := New(DefaultBudgets())
	module := append(header(), section(SectionType, []byte{0x00})...)
	first, err := analyzer.Analyze(context.Background(), module)
	if err != nil {
		t.Fatal(err)
	}
	second, err := analyzer.Analyze(context.Background(), module)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("相同内容应返回同一解析结果")
	}

	other := append(header(), section(SectionType, []byte{0x01})...)
	third, err := analyzer.Analyze(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if third == first || third.Digest == first.Digest {
		t.Fatal("不同内容应有不同身份")
	}
}

func TestAnalyzeHonoursCancellation(t *testing.T) {
	analyzer := New(DefaultBudgets())
	module := append(header(), section(SectionType, []byte{0x00})...)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := analyzer.Analyze(cancelled, module); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}

	if _, err := analyzer.Analyze(context.Background(), module); err != nil {
		t.Fatal(err)
	}
	if _, err := analyzer.Analyze(cancelled, module); !errors.Is(err, context.Canceled) {
		t.Fatalf("命中缓存也要检查取消: %v", err)
	}
}

func TestSectionIDNames(t *testing.T) {
	want := map[SectionID]string{
		SectionCustom: "custom", SectionType: "type", SectionImport: "import",
		SectionFunction: "function", SectionTable: "table", SectionMemory: "memory",
		SectionGlobal: "global", SectionExport: "export", SectionStart: "start",
		SectionElement: "element", SectionCode: "code", SectionData: "data",
	}
	for id, name := range want {
		if got := id.String(); got != name {
			t.Errorf("SectionID(%d).String()=%q want %q", id, got, name)
		}
	}
	if got := SectionID(42).String(); got != "section(42)" {
		t.Errorf("未知段名=%q", got)
	}
}

func TestAnalyzeParsesEveryImportKind(t *testing.T) {
	build := func(kind byte, descriptor []byte) []byte {
		body := append(uvarint(1), name("env")...)
		body = append(body, name("x")...)
		body = append(body, kind)
		body = append(body, descriptor...)
		return append(header(), section(SectionImport, body)...)
	}

	withMax := build(2, append(uvarint(0x01), append(uvarint(1), uvarint(2)...)...))
	parsed, err := New(DefaultBudgets()).Analyze(context.Background(), withMax)
	if err != nil || len(parsed.Imports) != 1 || parsed.Imports[0].Kind != 2 {
		t.Fatalf("内存 import 应解析: %+v err=%v", parsed, err)
	}

	for _, tc := range []struct {
		kind       byte
		descriptor []byte
	}{
		{0, uvarint(0)},

		{1, append([]byte{0x70}, append(uvarint(0x00), uvarint(1)...)...)},
		{3, []byte{0x7f, 0x00}},
	} {
		module := build(tc.kind, tc.descriptor)
		parsed, err := New(DefaultBudgets()).Analyze(context.Background(), module)
		if err != nil {
			t.Fatalf("kind=%d 应解析: %v", tc.kind, err)
		}
		if len(parsed.Imports) != 1 || parsed.Imports[0].Kind != tc.kind {
			t.Fatalf("kind=%d imports=%+v", tc.kind, parsed.Imports)
		}
	}

	unknown := build(9, nil)
	if _, err := New(DefaultBudgets()).Analyze(context.Background(), unknown); !errors.Is(err, errno.NoExec) {
		t.Fatalf("未知 import 类型应报 ENOEXEC: %v", err)
	}

	truncated := build(2, []byte{0x00})
	if _, err := New(DefaultBudgets()).Analyze(context.Background(), truncated); !errors.Is(err, errno.NoExec) {
		t.Fatalf("截断的内存 import 应报 ENOEXEC: %v", err)
	}

	if _, err := New(DefaultBudgets()).Analyze(context.Background(), build(1, nil)); !errors.Is(err, errno.NoExec) {
		t.Fatalf("缺失的表 import 描述应报 ENOEXEC: %v", err)
	}

	if _, err := New(DefaultBudgets()).Analyze(context.Background(), build(3, nil)); !errors.Is(err, errno.NoExec) {
		t.Fatalf("缺失的全局 import 描述应报 ENOEXEC: %v", err)
	}

	if _, err := New(DefaultBudgets()).Analyze(context.Background(), build(0, []byte{0xff, 0xff, 0xff, 0xff, 0xff})); !errors.Is(err, errno.NoExec) {
		t.Fatalf("损坏的函数 import 描述应报 ENOEXEC: %v", err)
	}
}

func TestAnalyzeRejectsTruncatedImportAndExportSections(t *testing.T) {
	analyzer := New(DefaultBudgets())
	ctx := context.Background()

	if _, err := analyzer.Analyze(ctx, append(header(), section(SectionImport, uvarint(1))...)); !errors.Is(err, errno.NoExec) {
		t.Fatalf("截断的 import 段应报 ENOEXEC: %v", err)
	}

	body := append(uvarint(1), name("env")...)
	body = append(body, uvarint(9)...)
	if _, err := analyzer.Analyze(ctx, append(header(), section(SectionImport, body)...)); !errors.Is(err, errno.NoExec) {
		t.Fatalf("越界的 import 名字应报 ENOEXEC: %v", err)
	}

	body = append(uvarint(1), name("env")...)
	body = append(body, name("x")...)
	if _, err := analyzer.Analyze(ctx, append(header(), section(SectionImport, body)...)); !errors.Is(err, errno.NoExec) {
		t.Fatalf("缺失的 import 类型应报 ENOEXEC: %v", err)
	}

	exportBody := append(uvarint(1), name("x")...)
	exportBody = append(exportBody, 0x00)
	if _, err := analyzer.Analyze(ctx, append(header(), section(SectionExport, exportBody)...)); !errors.Is(err, errno.NoExec) {
		t.Fatalf("缺失的 export 索引应报 ENOEXEC: %v", err)
	}

	badKind := append(uvarint(1), name("x")...)
	badKind = append(badKind, 0x09)
	if _, err := analyzer.Analyze(ctx, append(header(), section(SectionExport, badKind)...)); !errors.Is(err, errno.NoExec) {
		t.Fatalf("未知 export 类型应报 ENOEXEC: %v", err)
	}
}
