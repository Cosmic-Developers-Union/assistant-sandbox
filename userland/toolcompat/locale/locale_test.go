package locale

import (
	"errors"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	for _, test := range []struct {
		name string
		env  []string
		cat  Category
		want string
	}{
		{"default", nil, CType, Default},
		{"lang", []string{"LANG=de_DE"}, CType, "de_DE"},
		{"category-beats-lang", []string{"LANG=de_DE", "LC_CTYPE=fr_FR"}, CType, "fr_FR"},
		{"lc-all-beats-category", []string{"LC_ALL=es_ES", "LC_CTYPE=fr_FR", "LANG=de_DE"}, CType, "es_ES"},
		{"empty-lc-all-falls-through", []string{"LC_ALL=", "LC_CTYPE=fr_FR"}, CType, "fr_FR"},
		{"empty-category-falls-through", []string{"LC_CTYPE=", "LANG=de_DE"}, CType, "de_DE"},
		{"other-category", []string{"LC_COLLATE=pt_PT"}, Collate, "pt_PT"},
		{"last-duplicate-wins", []string{"LANG=a", "LANG=b"}, CType, "b"},
		{"zero-category-skips", []string{"LANG=de_DE"}, "", "de_DE"},
	} {
		if got := Resolve(test.env, test.cat); got != test.want {
			t.Fatalf("%s: 得到 %q，want %q", test.name, got, test.want)
		}
	}
	if all := ResolveAll([]string{"LC_ALL=de_DE.UTF-8"}); all.All != "de_DE.UTF-8" || all.Collate != "de_DE.UTF-8" || all.Time != "de_DE.UTF-8" {
		t.Fatalf("ResolveAll: %+v", all)
	}
	if all := ResolveAll(nil); all.All != Default {
		t.Fatalf("ResolveAll 默认: %+v", all)
	}
}

func TestResolveCarried(t *testing.T) {
	env := []string{"LANG=C.UTF-8"}
	if got := ResolveCarried(env, Collate); got != "C" {
		t.Fatalf("排序类应回落成 C: %q", got)
	}
	if got := ResolveCarried(env, CType); got != "C.UTF-8" {
		t.Fatalf("其他类别仍是 C.UTF-8: %q", got)
	}
	if got := ResolveCarried([]string{"LANG=de_DE"}, CType); got != "de_DE" {
		t.Fatalf("非 UTF-8 名字原样返回: %q", got)
	}
	cert := []string{"VSC_PROFILE=cert", "LANG=C.UTF-8"}
	if got := ResolveCarried(cert, CType); got != "C.UTF-8" {
		t.Fatalf("cert 画像不携带: %q", got)
	}
	if !IsCUTF8("C.utf8") || !IsCUTF("C.UTF-8@euro") || IsCUTF8("en_US.UTF-8") || IsCUTF8("C") {
		t.Fatal("IsCUTF8 判定错误")
	}
	if got := StoreEnvAt([]string{"LANG=C"}, func(string) string { return "/tmp" }); len(got) != 1 || got[0] != "LANG=C" {
		t.Fatalf("StoreEnvAt 应原样返回: %v", got)
	}
}

func TestProviderClassification(t *testing.T) {
	provider, err := Open("C")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	if provider.Name() != "C" {
		t.Fatalf("Name=%q", provider.Name())
	}
	if _, err := Open("de_DE.UTF-8"); !errors.Is(err, ErrUnsupportedLocale) {
		t.Fatalf("只有 C/POSIX 语义: %v", err)
	}
	for _, name := range []string{"", "C", "POSIX", "C.UTF-8", "c", "posix"} {
		if _, err := OpenEnv(nil, name); err != nil {
			t.Fatalf("%q 应被接受: %v", name, err)
		}
	}
	type predicate struct {
		name string
		fn   func(byte) (bool, error)
		yes  []byte
		no   []byte
	}
	predicates := []predicate{
		{"IsAlpha", provider.IsAlpha, []byte("aZ"), []byte("0_ ")},
		{"IsAlnum", provider.IsAlnum, []byte("aZ9"), []byte("_ ")},
		{"IsBlank", provider.IsBlank, []byte(" \t"), []byte("a\n")},
		{"IsCntrl", provider.IsCntrl, []byte{0x00, 0x1f, 0x7f}, []byte("a ")},
		{"IsDigit", provider.IsDigit, []byte("09"), []byte("a ")},
		{"IsGraph", provider.IsGraph, []byte("!A9~"), []byte(" \n")},
		{"IsLower", provider.IsLower, []byte("az"), []byte("AZ")},
		{"IsPrint", provider.IsPrint, []byte(" A~"), []byte{0x1f, 0x7f}},
		{"IsPunct", provider.IsPunct, []byte("!@~"), []byte("a9 ")},
		{"IsSpace", provider.IsSpace, []byte(" \t\n\v\f\r"), []byte("a")},
		{"IsUpper", provider.IsUpper, []byte("AZ"), []byte("az")},
		{"IsXDigit", provider.IsXDigit, []byte("09aAfF"), []byte("g ")},
	}
	for _, item := range predicates {
		for _, c := range item.yes {
			if ok, err := item.fn(c); err != nil || !ok {
				t.Fatalf("%s(%q)=%v err=%v，want true", item.name, c, ok, err)
			}
		}
		for _, c := range item.no {
			if ok, err := item.fn(c); err != nil || ok {
				t.Fatalf("%s(%q)=%v err=%v，want false", item.name, c, ok, err)
			}
		}
	}
	if got, _ := provider.ToLower([]byte("AbC1\xc3\x84")); string(got) != "abc1\xc3\x84" {
		t.Fatalf("ToLower=%q", got)
	}
	if got, _ := provider.ToUpper([]byte("AbC1\xc3\xa4")); string(got) != "ABC1\xc3\xa4" {
		t.Fatalf("ToUpper=%q", got)
	}
}

func TestProviderSorting(t *testing.T) {
	provider, _ := Open("POSIX")
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, test := range []struct {
		a, b string
		want int
	}{
		{"a", "b", -1},
		{"b", "a", 1},
		{"a", "a", 0},
		{"A", "a", -1},
	} {
		got, err := provider.Compare(test.a, test.b)
		if err != nil {
			t.Fatal(err)
		}
		if (got < 0) != (test.want < 0) || (got > 0) != (test.want > 0) {
			t.Fatalf("Compare(%q,%q)=%d，want 符号 %d", test.a, test.b, got, test.want)
		}
	}
	if got, _ := provider.Equivalents('x'); string(got) != "x" {
		t.Fatalf("Equivalents=%q", got)
	}
	if classes, _ := provider.EquivalenceClasses(); len(classes) != 256 {
		t.Fatalf("EquivalenceClasses 长度 %d", len(classes))
	}
	if weights, _ := provider.CollationWeights(); len(weights) != 256 || weights['A'] != 'A' {
		t.Fatalf("CollationWeights=%v", weights)
	}
	if elements, _ := provider.CollatingElements(); len(elements) != 256 {
		t.Fatalf("CollatingElements 长度 %d", len(elements))
	}
	var nilProvider *Provider
	if nilProvider.Name() != "" {
		t.Fatal("nil Provider 的名字应为空")
	}
	if err := nilProvider.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBREToERE(t *testing.T) {
	for _, test := range []struct {
		in, want string
	}{
		{`abc`, `abc`},
		{`a+b`, `a\+b`},
		{`a?b`, `a\?b`},
		{`a|b`, `a\|b`},
		{`(a)`, `\(a\)`},
		{`a{2}`, `a\{2\}`},
		{`a\+b`, `a+b`},
		{`a\?b`, `a?b`},
		{`a\|b`, `a|b`},
		{`\(a\)`, `(a)`},
		{`a\{2\}`, `a{2}`},
		{`[a+]`, `[a+]`},
		{`[^]]+`, `[^]]\+`},
		{`a\.b`, `a\.b`},
		{`a\`, `a\`},
		{`[abc`, `[abc`},
	} {
		if got := BREToERE(test.in); got != test.want {
			t.Fatalf("BREToERE(%q)=%q，want %q", test.in, got, test.want)
		}
	}
}

func TestMatchAffirmative(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      []string
		response string
		want     bool
	}{
		{"default-yes", nil, "y", true},
		{"default-yes-upper", nil, "Y", true},
		{"default-no", nil, "n", false},
		{"default-empty", nil, "", false},
		{"trailing-newline", []string{"LC_ALL=C"}, "y\n", true},
		{"trailing-crlf", []string{"LC_ALL=POSIX"}, "Y\r\n", true},
		{"prefix-word-matches", nil, "yes", true},
		{"leading-space", nil, " y", false},
		{"utf8-spelling-served-as-c", []string{"LANG=C.UTF-8"}, "y", true},
		{"foreign-utf8-spelling", []string{"LC_ALL=en_US.UTF-8"}, "y", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchAffirmative(tc.env, tc.response)
			if err != nil {
				t.Fatalf("MatchAffirmative(%v, %q) = %v", tc.env, tc.response, err)
			}
			if got != tc.want {
				t.Fatalf("MatchAffirmative(%v, %q) = %v, want %v", tc.env, tc.response, got, tc.want)
			}
		})
	}
	if _, err := MatchAffirmative([]string{"LC_ALL=de_DE.ISO-8859-1"}, "j"); !errors.Is(err, ErrUnsupportedMessagesLocale) {
		t.Fatalf("非 C/POSIX 语言环境应报不支持，得到 %v", err)
	}
	if _, err := MatchAffirmative([]string{"LC_MESSAGES=fr_FR.iso8859-1"}, "o"); !errors.Is(err, ErrUnsupportedMessagesLocale) {
		t.Fatalf("非 UTF-8 外来语言环境应报不支持，得到 %v", err)
	}
}
