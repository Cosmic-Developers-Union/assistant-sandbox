package locale

import (
	"strings"
	"testing"
)

func TestCUTF8Classes(t *testing.T) {
	for _, test := range []struct {
		class string
		r     rune
		want  bool
	}{
		{"alpha", 'a', true},
		{"alpha", 'ä', true},
		{"alpha", '1', false},
		{"digit", '7', true},
		{"digit", '٣', false},
		{"alnum", '7', true},
		{"upper", 'A', true},
		{"upper", 'a', false},
		{"lower", 'a', true},
		{"lower", 'A', false},
		{"space", ' ', true},
		{"space", '\u00a0', false},
		{"blank", '\t', true},
		{"print", '!', true},
		{"print", 0x07, false},
		{"graph", '!', true},
		{"graph", ' ', false},
		{"cntrl", 0x07, true},
		{"cntrl", 'a', false},
		{"punct", '!', true},
		{"punct", 'a', false},
		{"xdigit", 'f', true},
		{"xdigit", 'g', false},
		{"unknown-class", 'a', false},
	} {
		if got := CUTF8Class(test.class, test.r); got != test.want {
			t.Fatalf("CUTF8Class(%q, %q)=%v，want %v", test.class, test.r, got, test.want)
		}
	}
}

func TestCUTF8RE2ClassContent(t *testing.T) {
	content, ok := CUTF8RE2ClassContent("digit")
	if !ok || !strings.Contains(content, `\x{30}-\x{39}`) {
		t.Fatalf("digit 类内容 %q ok=%v", content, ok)
	}
	if again, ok := CUTF8RE2ClassContent("digit"); !ok || again != content {
		t.Fatalf("缓存后的内容 %q ok=%v", again, ok)
	}
	alpha, ok := CUTF8RE2ClassContent("alpha")
	if !ok || alpha == "" {
		t.Fatalf("alpha 类内容 %q ok=%v", alpha, ok)
	}
	if content, ok := CUTF8RE2ClassContent("nosuchclass"); ok || content != "" {
		t.Fatalf("未知类应返回 false: %q %v", content, ok)
	}
	for _, name := range []string{"space", "blank", "print", "graph", "cntrl", "punct", "xdigit", "upper", "lower", "alnum"} {
		if _, ok := CUTF8RE2ClassContent(name); !ok {
			t.Fatalf("%s 应生成 RE2 类内容", name)
		}
	}
}

func TestWriteRE2Rune(t *testing.T) {
	var builder strings.Builder
	for _, r := range []rune{'a', '0', '\n', 'ä', '中'} {
		writeRE2Rune(&builder, r)
	}
	if got := builder.String(); got == "" {
		t.Fatal("转义输出不应为空")
	}
	var single strings.Builder
	writeRE2Rune(&single, 'a')
	if single.String() != `\x{61}` {
		t.Fatalf("码点按 \\x{..} 形式转义: %q", single.String())
	}
	if strings.Contains(builder.String(), "a") {
		t.Fatalf("不应原样输出裸字符: %q", builder.String())
	}
}

func TestCollatingTypes(t *testing.T) {
	element := CollatingElement{Text: "ch", Order: 1, Weights: [][]int{{1}}}
	collation := Collation{Backward: []bool{false}, Elements: []CollatingElement{element}}
	if collation.Elements[0].Text != "ch" || collation.Position != nil {
		t.Fatalf("零值语义改变: %+v", collation)
	}
}
