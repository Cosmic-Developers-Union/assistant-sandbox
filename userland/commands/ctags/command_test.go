package ctags

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

const sample = `#include <stdio.h>
/* 注释里的 fake_fn(int) 不算 */
// 行注释 dummy_t 也不算
static const char *greeting = "call_in_string(int)";

int real(int a) { return a / 2; }

int proto(int a);

static void
multiline_body(void)
{
    if (x) { y(); }
    for (i = 0; i < n; i++) { }
}

typedef int myint;
typedef struct node {
    int value;
    struct node *next;
} node_t;
typedef enum { A, B } letter_t;
typedef void (*handler)(int);

old_style(p, q)
int p, q;
{ return p + q; }
`

var wantTags = "" +
	"int\tsample.c\t/^typedef void (*handler)(int);$/\n" +
	"letter_t\tsample.c\t/^typedef enum { A, B } letter_t;$/\n" +
	"multiline_body\tsample.c\t/^multiline_body(void)$/\n" +
	"myint\tsample.c\t/^typedef int myint;$/\n" +
	"node_t\tsample.c\t/^} node_t;$/\n" +
	"old_style\tsample.c\t/^old_style(p, q)$/\n" +
	"real\tsample.c\t/^int real(int a) { return a \\/ 2; }$/\n"

func TestScanFunctions(t *testing.T) {
	var names []string
	scanFunctions(cleanLines(sample), func(name string, lineNo int) {
		names = append(names, name)
	})

	if strings.Join(names, ",") != "real,multiline_body,old_style" {
		t.Fatalf("函数标签: %v", names)
	}
}

func TestScanTypedefs(t *testing.T) {
	var lines strings.Builder
	scanTypedefs(cleanLines(sample), func(name string, lineNo int) {
		lines.WriteString(name + "\n")
	})

	if lines.String() != "myint\nnode_t\nletter_t\nint\n" {
		t.Fatalf("typedef 标签: %q", lines.String())
	}
}

func TestTagsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.c"), []byte(sample), 0o644); err != nil {
		t.Error(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"sample.c"}, "")
	if result.Code != 0 {
		t.Fatalf("ctags: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tags"))
	if err != nil {
		t.Fatal(err)
	}

	want := wantTags
	if string(data) != want {
		t.Fatalf("tags:\n got\n%s\nwant\n%s", data, want)
	}
}

func TestNamedOutputAndAppend(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.c"), []byte("int one(void) { return 1; }\n"), 0o644); err != nil {
		t.Error(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-f", "mytags", "a.c"}, "")
	if result.Code != 0 {
		t.Fatalf("ctags -f: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "mytags"))
	if err != nil || string(data) != "one\ta.c\t/^int one(void) { return 1; }$/\n" {
		t.Fatalf("mytags: %v %q", err, data)
	}

	if err := os.WriteFile(filepath.Join(dir, "b.c"), []byte("int two(void) { return 2; }\n"), 0o644); err != nil {
		t.Error(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"-a", "-f", "mytags", "b.c"}, "")
	if result.Code != 0 {
		t.Fatalf("ctags -a: %+v", result)
	}
	data, err = os.ReadFile(filepath.Join(dir, "mytags"))
	want := "one\ta.c\t/^int one(void) { return 1; }$/\n" + "two\tb.c\t/^int two(void) { return 2; }$/\n"
	if err != nil || string(data) != want {
		t.Fatalf("追加后:\n got %q\nwant %q", data, want)
	}

	result = commandtest.Run(t, Run, dir, []string{"-f", "-", "a.c"}, "")
	if result.Code != 0 || result.Stdout != "one\ta.c\t/^int one(void) { return 1; }$/\n" {
		t.Fatalf("-f -: %+v", result)
	}
}

func TestCxref(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.c"), []byte("int beta(void) { return 0; }\nint alpha(void) { return 1; }\n"), 0o644); err != nil {
		t.Error(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-x", "x.c"}, "")

	want := "alpha 2 x.c int alpha(void) { return 1; }\nbeta 1 x.c int beta(void) { return 0; }\n"
	if result.Code != 0 || result.Stdout != want {
		t.Fatalf("-x:\n got %q\nwant %q", result.Stdout, want)
	}
}

func TestEscapePattern(t *testing.T) {
	dir := t.TempDir()

	src := "int div(int a) { return a / 2; }\n"
	if err := os.WriteFile(filepath.Join(dir, "e.c"), []byte(src), 0o644); err != nil {
		t.Error(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"e.c"}, "")
	if result.Code != 0 {
		t.Fatalf("ctags: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tags"))
	want := "div\te.c\t/^int div(int a) { return a \\/ 2; }$/\n"
	if err != nil || string(data) != want {
		t.Fatalf("转义:\n got %q\nwant %q", data, want)
	}
}

func TestSkipsAndFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.c"), []byte("int ok(void) { return 0; }\n"), 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("int not_c(void);\n"), 0o644); err != nil {
		t.Error(err)
	}

	result := commandtest.Run(t, Run, dir, []string{"notes.txt", "good.c"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "不是 C 源文件") {
		t.Fatalf("非 C 文件: %+v", result)
	}

	data, err := os.ReadFile(filepath.Join(dir, "tags"))
	if err != nil || !strings.Contains(string(data), "ok\tgood.c\t") {
		t.Fatalf("good.c 应仍被处理: %v %q", err, data)
	}

	result = commandtest.Run(t, Run, dir, []string{"ghost.c"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "ghost.c") {
		t.Fatalf("缺失文件: %+v", result)
	}

	if err := os.Mkdir(filepath.Join(dir, "sub.c"), 0o755); err != nil {
		t.Error(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"sub.c"}, "")
	if result.Code != 1 {
		t.Fatalf("目录: %+v", result)
	}
}

func TestInputBudget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.c"), []byte(strings.Repeat("int a;\n", maxInputBytes/7+2)), 0o644); err != nil {
		t.Error(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"big.c"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "累计输入超过 4 MiB") {
		t.Fatalf("预算: %+v", result)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{}, "需要至少一个源文件"},
		{[]string{"-t", "a.c"}, "只做 C 子集"},
		{[]string{"-u", "a.c"}, "只做 C 子集"},
		{[]string{"-v", "a.c"}, "只做 C 子集"},
		{[]string{"-w", "a.c"}, "只做 C 子集"},
		{[]string{"-a", "-x", "a.c"}, "-x 写标准输出"},
		{[]string{"--bogus", "a.c"}, "参数错误"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 2 || !strings.Contains(result.Stderr, test.want) {
			t.Fatalf("%v: code=%d stderr=%q", test.args, result.Code, result.Stderr)
		}
	}
}

func TestCleanLinesAndHelpers(t *testing.T) {

	source := "a /* c1\nc2 */ b\ns \"x\ny\" t\nl // tail\n"
	clean := cleanLines(source)
	if len(clean) != 6 {
		t.Fatalf("行数: %d", len(clean))
	}
	if strings.Contains(clean[1], "c2") || !strings.Contains(clean[1], "b") {
		t.Fatalf("跨行块注释应清空: %q", clean[1])
	}
	if strings.Contains(clean[2]+clean[3], "x") || strings.Contains(clean[3], "y") {
		t.Fatalf("跨行字符串应清空: %q %q", clean[2], clean[3])
	}
	if strings.Contains(clean[4], "tail") {
		t.Fatalf("行注释应清空: %q", clean[4])
	}

	if got := cleanLines(`c 'a"b' d`)[0]; got != `c       d` {
		t.Fatalf("字符字面量: %q", got)
	}

	if indexWord("mytypedef int", "typedef") >= 0 {
		t.Fatal("标识符内部的 typedef 不应命中")
	}
	if indexWord("  typedef int", "typedef") != 2 {
		t.Fatal("typedef 应命中")
	}

	if got := lastIdentifier("} foo_t"); got != "foo_t" {
		t.Fatalf("lastIdentifier: %q", got)
	}
	if got := lastIdentifier("} 42"); got != "" {
		t.Fatalf("数字开头不是标识符: %q", got)
	}
	if got := lastIdentifier("}"); got != "" {
		t.Fatalf("无标识符: %q", got)
	}

	if _, ok := matchParens("f(a))", 1); ok {
		t.Fatal("多出的右括号应不配平")
	}
	if closed, ok := matchParens("f((a))", 1); !ok || closed != 5 {
		t.Fatalf("嵌套配平: %d %v", closed, ok)
	}

	if got := escapePattern(`a/b\c`); got != `a\/b\\c` {
		t.Fatalf("escapePattern: %q", got)
	}
}

func TestStickyWriteFailure(t *testing.T) {
	w := &stickyWriter{writer: failWriter{}}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("写失败应返回错误")
	}
	if _, err := w.Write([]byte("y")); err == nil {
		t.Fatal("失败应粘连")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
