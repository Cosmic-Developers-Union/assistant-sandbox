package xargs

import (
	"strings"
	"testing"
)

func itemsOf(text string) []inputItem {
	items, err := readItems(strings.NewReader(text), options{maxArgs: -1})
	if err != nil {
		panic(err)
	}
	return items
}

func format(batches [][]string) string {
	var out strings.Builder
	for _, argv := range batches {
		out.WriteString(strings.Join(argv, " "))
		out.WriteString("\n")
	}
	return out.String()
}

func TestPlanBatches(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		command []string
		o       options
		want    string
	}{
		{"缺省一项一批", "a b c\n", []string{"echo"}, options{maxArgs: -1, maxChars: lineMax}, "echo a b c\n"},
		{"-n2 每批两项", "1 2 3 4 5\n", []string{"echo"}, options{maxArgs: 2, maxChars: lineMax}, "echo 1 2\necho 3 4\necho 5\n"},
		{"-L2 每批两逻辑行", "a b\nc\nd e\n", []string{"echo"}, options{maxLines: 2, maxArgs: -1, maxChars: lineMax}, "echo a b c\necho d e\n"},
		{"空输入仍跑一次", "   \n", []string{"echo", "ran"}, options{maxArgs: -1, maxChars: lineMax}, "echo ran\n"},
		{"-r 空输入不跑", "   \n", []string{"echo", "ran"}, options{maxArgs: -1, maxChars: lineMax, noRunEmpty: true}, ""},
		{"-I 每项一次且只替参数", "x\ny\n", []string{"echo", "[{}]"}, options{replaceSet: true, replace: "{}", maxArgs: -1, maxLines: 1, maxChars: lineMax}, "echo [x]\necho [y]\n"},
		{"-n1 与引号", "'a b' c\n", []string{"echo"}, options{maxArgs: 1, maxChars: lineMax}, "echo a b\necho c\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			batches, err := plan(test.command, itemsOf(test.input), test.o)
			if err != nil {
				t.Fatal(err)
			}
			if got := format(batches); got != test.want {
				t.Fatalf("得到 %q，期望 %q", got, test.want)
			}
		})
	}
}

func TestPlanSizeLimits(t *testing.T) {
	command := []string{"echo"}
	base := argvSize(command)

	items := []inputItem{{value: "abcd", line: 1}}
	if _, err := plan(command, items, options{maxArgs: -1, maxChars: base + 5}); err != nil {
		t.Fatalf("恰好等于上限不应报错: %v", err)
	}
	if _, err := plan(command, items, options{maxArgs: -1, maxChars: base + 4}); err == nil {
		t.Fatal("超出上限应当报错")
	}

	if _, err := plan(command, items, options{maxArgs: -1, maxChars: base + 5, strictSize: true}); err == nil {
		t.Fatal("严格比较下等于上限应当报错")
	}

	four := []inputItem{{value: "a", line: 1}, {value: "b", line: 1}, {value: "c", line: 1}}
	if _, err := plan(command, four, options{maxArgs: -1, maxChars: base + 3, exactSize: true}); err == nil {
		t.Fatal("-x 装不下应当报错")
	}

	if _, err := plan([]string{"echo", strings.Repeat("x", 32)}, nil, options{maxArgs: -1, maxChars: 8}); err == nil {
		t.Fatal("命令自身超限应当报错")
	}
}

func TestPlanReplacementLimits(t *testing.T) {
	o := options{replaceSet: true, replace: "{}", maxArgs: -1, maxLines: 1, maxChars: 4096}
	five := []string{"echo", "{}", "{}", "{}", "{}", "{}"}
	if _, err := plan(five, itemsOf("x\n"), o); err != nil {
		t.Fatalf("五个替换参数应当接受: %v", err)
	}
	six := append(append([]string(nil), five...), "{}")
	if _, err := plan(six, itemsOf("x\n"), o); err == nil {
		t.Fatal("六个替换参数应当报错")
	}
	long := []string{"echo", strings.Repeat("a", 250) + "{}"}
	if _, err := plan(long, itemsOf(strings.Repeat("y", 10)+"\n"), o); err == nil {
		t.Fatal("替换后超过 255 字节应当报错")
	}
}

func TestPlanNoItemsNoReplacement(t *testing.T) {
	batches, err := plan([]string{"echo", "{}"}, itemsOf(""), options{replaceSet: true, replace: "{}", maxLines: 1, maxChars: 4096})
	if err != nil || len(batches) != 0 {
		t.Fatalf("得到 %+v %v", batches, err)
	}
}
