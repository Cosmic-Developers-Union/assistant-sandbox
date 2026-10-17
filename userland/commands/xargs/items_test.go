package xargs

import (
	"strings"
	"testing"
)

func values(items []inputItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.value
	}
	return out
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestReadItemsSplitting(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		o     options
		want  []string
	}{
		{"缺省按空白与换行", "a b\nc\n", options{maxArgs: -1}, []string{"a", "b", "c"}},
		{"引号成组且反斜杠转义", `'a b' c\ d`, options{maxArgs: -1}, []string{"a b", "c d"}},
		{"空引号也算一项", `a '' b`, options{maxArgs: -1}, []string{"a", "", "b"}},
		{"NUL 只分隔不切空白", "a\x00b c\x00", options{null: true, maxArgs: -1}, []string{"a", "b c"}},
		{"自定义分隔符", "a,b,c", options{delim: ",", maxArgs: -1}, []string{"a", "b", "c"}},
		{"分隔符转义写法", "a\tb", options{delim: `\t`, maxArgs: -1}, []string{"a", "b"}},
		{"-E 截断且不含该串", "a\nb\n_END_\nc\n", options{eof: "_END_", maxArgs: -1}, []string{"a", "b"}},
		{"换行也是分隔符", "a\n\nb", options{maxArgs: -1}, []string{"a", "b"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			items, err := readItems(strings.NewReader(test.input), test.o)
			if err != nil {
				t.Fatal(err)
			}
			if got := values(items); !equal(got, test.want) {
				t.Fatalf("得到 %q，期望 %q", got, test.want)
			}
		})
	}
}

func TestReadItemsRejectsMalformedQuoting(t *testing.T) {
	for _, input := range []string{"'a b", `a "b`, `a\`} {
		if _, err := readItems(strings.NewReader(input), options{maxArgs: -1}); err == nil {
			t.Fatalf("%q 应当报错", input)
		}
	}
}

func TestReadItemsReplacementMode(t *testing.T) {
	items, err := readItems(strings.NewReader("a b\n  'c d'  \n"), options{replaceSet: true, replace: "{}", maxArgs: -1, maxLines: 1})
	if err != nil {
		t.Fatal(err)
	}

	if got := values(items); !equal(got, []string{"a b", "c d  "}) {
		t.Fatalf("得到 %q", got)
	}
	if _, err := readItems(strings.NewReader("a 'b\n"), options{replaceSet: true, maxArgs: -1, maxLines: 1}); err == nil {
		t.Fatal("未配对的引号应当报错")
	}
}

func TestReadItemsLogicalLines(t *testing.T) {

	items, err := readItems(strings.NewReader("one \n\n two\nthree\n"), options{maxLines: 2, maxArgs: -1})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		value string
		line  int
	}{{"one", 1}, {"two", 1}, {"three", 2}}
	if len(items) != len(want) {
		t.Fatalf("得到 %+v", items)
	}
	for i, w := range want {
		if items[i].value != w.value || items[i].line != w.line {
			t.Fatalf("第 %d 项得到 %+v，期望 %+v", i, items[i], w)
		}
	}

	items, err = readItems(strings.NewReader("one\\ \ntwo\n"), options{maxLines: 2, maxArgs: -1})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].line == items[1].line {
		t.Fatalf("转义空白不应续行: %+v", items)
	}
}

func TestReadItemsInputBudget(t *testing.T) {
	big := strings.Repeat("a ", maxInputBytes/2+1)
	if _, err := readItems(strings.NewReader(big), options{maxArgs: -1}); err == nil {
		t.Fatal("超过预算应当报错")
	}
}
