package editor

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type store struct{ files map[string]string }

func memoryFiles(initial map[string]string) (*store, Files) {
	s := &store{files: map[string]string{}}
	for name, data := range initial {
		s.files[name] = data
	}
	return s, Files{
		Read: func(name string) (string, error) {
			data, ok := s.files[name]
			if !ok {
				return "", fmt.Errorf("%s: 不存在", name)
			}
			return data, nil
		},
		Write:  func(name string, data string) error { s.files[name] = data; return nil },
		Append: func(name string, data string) error { s.files[name] += data; return nil },
	}
}

type session struct {
	out, diag string
	code      int
	files     map[string]string
}

func drive(initial map[string]string, input string, setup func(*Engine)) session {
	st, files := memoryFiles(initial)
	var out, diag bytes.Buffer
	eng := &Engine{Out: &out, Diag: &diag, Files: files}
	if setup != nil {
		setup(eng)
	}
	code := eng.Run(strings.NewReader(input))
	return session{out: out.String(), diag: diag.String(), code: code, files: st.files}
}

func load(name string) func(*Engine) {
	return func(e *Engine) {
		if _, err := e.Load(name); err != nil {
			panic(err)
		}
	}
}

func expect(t *testing.T, got session, wantOut, wantDiag string, wantCode int) {
	t.Helper()
	if got.code != wantCode || got.out != wantOut || got.diag != wantDiag {
		t.Fatalf("code=%d out=%q diag=%q，want code=%d out=%q diag=%q",
			got.code, got.out, got.diag, wantCode, wantOut, wantDiag)
	}
}

func TestAppendInsertChange(t *testing.T) {
	expect(t, drive(nil, "a\nhello\nworld\n.\n,n\n", nil), "1\thello\n2\tworld\n", "?\n", 1)
	expect(t, drive(nil, "i\nfirst\n.\n1p\n", nil), "first\n", "?\n", 1)

	expect(t, drive(nil, "a\none\n.\n1i\n.\n,n\n", nil), "1\tone\n", "?\n", 1)

	expect(t, drive(nil, "0a\nhi\n.\n,n\n", nil), "1\thi\n", "?\n", 1)
	expect(t, drive(nil, "0p\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\n.\n1,2c\nmid\n.\nn\n", nil), "1\tmid\n", "?\n", 1)
}

func TestAppendPrintWrite(t *testing.T) {
	expect(t, drive(nil, "a\nhello\nworld\n.\nw f\nq\n", nil), "12\n", "", 0)
	if got := drive(nil, "a\nhello\n.\nw f\nq\n", nil); got.files["f"] != "hello\n" {
		t.Fatalf("写出内容: %q", got.files["f"])
	}

	expect(t, drive(nil, "a\nhello\n.\nwq f\n", nil), "6\n", "", 0)

	expect(t, drive(nil, "a\nhi\n.\nw f\nq\n", func(e *Engine) { e.Silent = true }), "", "", 0)

	expect(t, drive(map[string]string{"f": "a\n"}, "a\nb\n.\nW f\nq\nq\n", nil), "2\n", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\n.\n1w f\nf\n", nil), "4\nf\n", "?\n", 1)
}

func TestPrintForms(t *testing.T) {
	expect(t, drive(nil, "a\nx\x07z\nb$\n.\n,n\n,l\n,p\n", nil),
		"1\tx\x07z\n2\tb$\n"+
			"x\\az$\nb\\$$\n"+
			"x\x07z\nb$\n",
		"?\n", 1)

	long := strings.Repeat("a", 80)
	got := drive(nil, "a\n"+long+"\n.\nl\n", nil)
	if got.code != 1 || !strings.Contains(got.out, "\\\n") {
		t.Fatalf("l 折行: %+v", got)
	}
}

func TestAddresses(t *testing.T) {

	expect(t, drive(nil, "a\nfoo\nbar\nbaz\n.\n3\n$-1p\n", nil), "baz\nbar\n", "?\n", 1)

	expect(t, drive(nil, "a\nfoo\nbar\n.\n1\n/bar/\n", nil), "foo\nbar\n", "?\n", 1)

	expect(t, drive(nil, "a\nfoo\nbar\n.\n2\n?foo?\n", nil), "bar\nfoo\n", "?\n", 1)

	expect(t, drive(nil, "a\nfoo\nbar\n.\n1kx\n'x+1p\n", nil), "bar\n", "?\n", 1)

	expect(t, drive(nil, "a\nfoo\nbar\n.\n/bar/p\n//p\n", nil), "bar\nbar\n", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\n.\n2\n,p\n", nil), "two\none\ntwo\n", "?\n", 1)

	expect(t, drive(nil, "9p\n", nil), "", "?\n", 1)
	expect(t, drive(nil, "1k1\n", nil), "", "?\n", 1)
}

func TestSubstitute(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"基本", "a\nhello world\n.\ns/world/there/\np\n", "hello there\n"},
		{"g标志", "a\naaa\n.\ns/a/b/g\np\n", "bbb\n"},
		{"计数", "a\naaa\n.\ns/a/b/2\np\n", "aba\n"},
		{"&引用", "a\nab\n.\ns/b/&X/\np\n", "abX\n"},
		{"分组引用", "a\nab\n.\ns/\\(a\\)\\(b\\)/\\2\\1/\np\n", "ba\n"},
		{"p后缀", "a\nhi\n.\ns/i/I/p\n", "hI\n"},
		{"n后缀", "a\nhi\n.\ns/i/I/n\n", "1\thI\n"},
		{"空模式复用", "a\nfoo\nbar\n.\n/bar/\ns//x/\np\n", "bar\nx\n"},
		{"多行替换", "a\nab\n.\ns/a/\\\nX/\np\n", "Xb\n"},
		{"%复用替换", "a\nab\n.\ns/a/Y/\ns/b/%/\np\n", "YY\n"},
	}
	for _, test := range cases {
		expect(t, drive(nil, test.input, nil), test.want, "?\n", 1)
	}

	expect(t, drive(nil, "a\nhi\n.\ns/z/q\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\nx\n.\ns/x/y/q\n", nil), "", "?\n?\n", 1)
	expect(t, drive(nil, "s\n", nil), "", "?\n", 1)
}

func TestUndo(t *testing.T) {

	expect(t, drive(nil, "a\none\ntwo\n.\nd\nu\n,n\nu\nu\n,n\n", nil),
		"1\tone\n2\ttwo\n1\tone\n2\ttwo\n", "?\n", 1)
	expect(t, drive(nil, "u\n", nil), "", "?\n", 1)
}

func TestEditQuitWarnings(t *testing.T) {

	expect(t, drive(map[string]string{"g": "hello\n"}, "a\nx\n.\ne\nw\ne\n", load("g")),
		"8\n8\n", "?\n", 1)

	expect(t, drive(map[string]string{"g": "hi\n"}, "a\nx\n.\nE\nq\n", load("g")), "3\n", "", 0)

	expect(t, drive(nil, "a\nx\n.\nq\nq\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "a\nx\n.\nQ\n", nil), "", "", 0)

	expect(t, drive(nil, "a\nx\n.\n", nil), "", "?\n", 1)
}

func TestFileCommands(t *testing.T) {
	expect(t, drive(nil, "f new\nf\nq\n", nil), "new\nnew\n", "", 0)

	expect(t, drive(nil, "e nope\nw\nq\n", nil), "0\n", "nope: 不存在\n?\n", 1)

	expect(t, drive(map[string]string{"g": "one\n"}, "a\ntwo\n.\nr g\n,n\n", nil),
		"4\n1\ttwo\n2\tone\n", "?\n", 1)

	expect(t, drive(nil, "r\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "af\n", nil), "", "?\n", 1)
}

func TestJoinMoveCopyZ(t *testing.T) {
	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n1,2j\nn\n", nil), "1\tonetwo\n", "?\n", 1)
	expect(t, drive(nil, "a\none\ntwo\n.\n1,2m0\n,n\n", nil), "1\tone\n2\ttwo\n", "?\n", 1)
	expect(t, drive(nil, "a\none\ntwo\n.\n1t0\n,n\n", nil), "1\tone\n2\tone\n3\ttwo\n", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n1z1\nn\n", nil), "two\n2\ttwo\n", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n1z\n", nil), "two\nthree\n", "?\n", 1)

	expect(t, drive(nil, "a\none\n.\n1zx\n", nil), "", "?\n?\n", 1)
}

func TestGlobal(t *testing.T) {

	expect(t, drive(nil, "a\nfoo\nbar\nfoo\n.\n1,$g/foo/s//x/\n,n\n", nil),
		"1\tx\n2\tbar\n3\tx\n", "?\n", 1)

	expect(t, drive(nil, "a\nfoo\nbar\nfoo\n.\n1,$v/foo/d\n,n\n", nil),
		"1\tfoo\n2\tfoo\n", "?\n", 1)

	expect(t, drive(nil, "a\nbar\n.\n1,$g/bar/s/bar/line/\\\ns/line/line2/\n,n\n", nil),
		"1\tline2\n", "?\n", 1)

	expect(t, drive(nil, "a\nbar\n.\n1,$g/bar/s/bar/one\\\ntwo/\n,n\n", nil),
		"1\tone\n2\ttwo\n", "?\n", 1)

	got := drive(nil, "H\na\nfoo\n.\n1,$g/foo/a\nq\nq\n", nil)
	if got.code != 1 || !strings.Contains(got.diag, "不允许 a 命令") {
		t.Fatalf("g 内 a: %+v", got)
	}

	got = drive(nil, "H\na\nfoo\n.\n1,$g/foo/g/bar/s//x/\nq\nq\n", nil)
	if got.code != 1 || !strings.Contains(got.diag, "嵌套") {
		t.Fatalf("g 嵌套: %+v", got)
	}

	expect(t, drive(nil, "a\nfoo\n.\nw f\n1,$g/foo/q\n", nil), "4\n", "", 0)

	got = drive(nil, "H\na\nfoo\n.\n1,$g foo\nq\nq\n", nil)
	if got.code != 1 || !strings.Contains(got.diag, "正则分隔符无效") {
		t.Fatalf("g 分隔符: %+v", got)
	}

	got = drive(nil, "H\na\nfoo\n.\n1,$g/foo/s/zz/y/\nq\nq\n", nil)
	if got.code != 1 || !strings.Contains(got.diag, "没有匹配") {
		t.Fatalf("g 无匹配: %+v", got)
	}
}

func TestDiagnostics(t *testing.T) {

	expect(t, drive(nil, "h\n", nil), "", "", 0)

	expect(t, drive(map[string]string{"g": "x\n"}, "s/y/z/\nh\nq\n", load("g")),
		"", "?\n没有匹配\n", 1)

	expect(t, drive(map[string]string{"g": "x\n"}, "H\ns/y/z/\nq\n", load("g")),
		"", "?\n没有匹配\n", 1)
	expect(t, drive(map[string]string{"g": "x\n"}, "s/y/z/\nH\ns/y/z/\nq\n", load("g")),
		"", "?\n没有匹配\n?\n没有匹配\n", 1)
	expect(t, drive(nil, "Y\n", nil), "", "?\n", 1)
}

func TestPromptToggle(t *testing.T) {

	expect(t, drive(nil, "P\na\nx\n.\nP\nQ\n", nil), "**", "", 0)
	expect(t, drive(nil, "a\nx\n.\nQ\n", func(e *Engine) { e.Prompt = ">" }), ">>", "", 0)
}

func TestShellEscape(t *testing.T) {
	var ran []string
	shell := func(e *Engine) {
		e.Shell = func(command string) error {
			ran = append(ran, command)
			return nil
		}
	}

	expect(t, drive(nil, "!echo hi\n!!\nq\n", shell), "!\necho hi\n!\n", "", 0)

	if len(ran) != 2 || ran[0] != "echo hi" || ran[1] != "echo hi" {
		t.Fatalf("! 执行: %v", ran)
	}

	ran = nil
	expect(t, drive(nil, "f g\n!cat %\n!cat \\%\nq\n", shell), "g\ncat g\n!\n!\n", "", 0)
	if len(ran) != 2 || ran[0] != "cat g" || ran[1] != "cat %" {
		t.Fatalf("%% 展开: %v", ran)
	}

	expect(t, drive(nil, "!ls\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "!\n", func(e *Engine) { e.Shell = func(string) error { return nil } }), "", "?\n", 1)

	expect(t, drive(nil, "r !cat g\n", func(e *Engine) { e.Shell = func(string) error { return nil } }), "", "?\n", 1)
}

func TestInputModeEOF(t *testing.T) {
	got := drive(nil, "a\none\ntwo", nil)
	if got.code != 1 || got.diag != "?\n" {
		t.Fatalf("EOF 输入: %+v", got)
	}
}

func TestBufferBasics(t *testing.T) {
	var b Buffer
	if err := b.Append(0, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Append(2, []string{"b"}); err == nil {
		t.Fatal("越界插入应当失败")
	}
	if err := b.Append(1, []string{"b", "c"}); err != nil {
		t.Fatal(err)
	}
	if b.Last() != 3 || b.Current != 3 {
		t.Fatalf("追加后: %+v", b)
	}
	if err := b.Delete(1, 1); err != nil {
		t.Fatal(err)
	}
	if b.Last() != 2 || b.Current != 1 {
		t.Fatalf("删除后: %+v", b)
	}
	if err := b.Delete(2, 1); err == nil {
		t.Fatal("倒序删除应当失败")
	}
	clone := b.Clone()
	clone.Lines[0] = "z"
	if b.Lines[0] != "b" {
		t.Fatal("克隆必须独立")
	}
	if err := b.Change(1, 2, []string{"x"}); err != nil {
		t.Error(err)
	}
	if b.Last() != 1 || b.Lines[0] != "x" {
		t.Fatalf("Change: %+v", b)
	}
	if joinLines(nil) != "" {
		t.Fatal("空缓冲拼接应为空")
	}
	if got := splitText(""); got != nil {
		t.Fatalf("空文本: %v", got)
	}
	if got := splitText("\n"); len(got) != 1 || got[0] != "" {
		t.Fatalf("单空行: %v", got)
	}
}

func TestMoveRealShift(t *testing.T) {

	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n1,2m3\n,n\n", nil),
		"1\tthree\n2\tone\n3\ttwo\n", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n1,2m2\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n3kz\n1,2m3\n'z p\n,n\n", nil),
		"three\n1\tthree\n2\tone\n3\ttwo\n", "?\n", 1)
}

func TestGlobalShiftWithNewlines(t *testing.T) {
	expect(t, drive(nil, "a\nfoo\nbar\nfoo\n.\n1,$g/foo/s/foo/x\\\ny/\n,n\n", nil),
		"1\tx\n2\ty\n3\tbar\n4\tx\n5\ty\n", "?\n", 1)

	expect(t, drive(nil, "1,$g/x/p\n", nil), "", "?\n", 1)
}

func TestBracketDelimiter(t *testing.T) {

	expect(t, drive(nil, "a\na/b\n.\n/a[/]b/\n", nil), "a/b\n", "?\n", 1)

	expect(t, drive(nil, "a\na/b\n.\ns/a[/]b/X/\np\n", nil), "X\n", "?\n", 1)

	expect(t, drive(nil, "/[/\n", nil), "", "?\n", 1)
}

func TestListEscapesMore(t *testing.T) {
	expect(t, drive(nil, "a\n\b\r\f\v\\\x80\xff\n.\nl\n", nil),
		"\\b\\r\\f\\v\\\\\\200\\377$\n", "?\n", 1)
}

func TestSubstituteMore(t *testing.T) {
	expect(t, drive(nil, "a\nx\n.\ns/x/y/0\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\nxb\n.\ns/x/Y\n", nil), "Yb\n", "?\n", 1)

	expect(t, drive(nil, "a\nx\n.\ns/a/\n", nil), "", "?\n?\n", 1)
}

func TestDeleteMarkInsideRange(t *testing.T) {
	expect(t, drive(nil, "a\none\ntwo\n.\n1kx\n1d\n'x p\n", nil), "", "?\n?\n", 1)
}

func TestUndoDirtyRestore(t *testing.T) {
	expect(t, drive(nil, "a\nx\n.\ns/x/y/\nu\n,p\n", nil), "x\n", "?\n", 1)
}

func TestAppendEmpty(t *testing.T) {
	expect(t, drive(nil, "a\n.\n", nil), "", "", 0)
}

func TestWriteBoundaryErrors(t *testing.T) {

	expect(t, drive(nil, "a\none\n.\n1,9w f\n", nil), "", "?\n?\n", 1)

	_, files := memoryFiles(nil)
	files.Append = nil
	var out, diag bytes.Buffer
	eng := &Engine{Out: &out, Diag: &diag, Files: files}
	if code := eng.Run(strings.NewReader("a\nx\n.\nW f\n")); code != 1 || diag.String() != "?\n?\n" {
		t.Fatalf("W 无能力: code=%d diag=%q", code, diag.String())
	}
}

func TestExecuteAPI(t *testing.T) {
	_, files := memoryFiles(nil)
	var out, diag bytes.Buffer
	eng := &Engine{Out: &out, Diag: &diag, Files: files}
	eng.Start(strings.NewReader("x\n.\n"))
	if eng.Execute("a") {
		t.Fatal("a 不应退出")
	}
	if eng.HadError() {
		t.Fatal("不应有错误")
	}
	eng.Fail("测试失败")
	if !eng.HadError() {
		t.Fatal("Fail 应置失败位")
	}
	if !eng.Execute("Q") {
		t.Fatal("Q 应退出")
	}
}

func TestCommandLetterTable(t *testing.T) {
	for line, want := range map[string]byte{
		"s/a/b/": 's', "1,2d": 'd', "'x p": 'p', "/re/p": 'p',
		"?re?d": 'd', ",+2p": 'p', "$=": '=', "": 0, "  j": 'j',
	} {
		if got := commandLetter(line); got != want {
			t.Fatalf("commandLetter(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestSemicolonAddress(t *testing.T) {
	expect(t, drive(nil, "a\none\ntwo\n.\n2\n;p\n", nil), "two\ntwo\n", "?\n", 1)
}

func TestBufferChangeEdges(t *testing.T) {
	var b Buffer
	if err := b.Change(0, 0, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if b.Last() != 1 {
		t.Fatalf("空缓冲 Change: %+v", b)
	}
	if err := b.Change(1, 1, nil); err != nil {
		t.Fatal(err)
	}
	if b.Last() != 0 {
		t.Fatalf("清空 Change: %+v", b)
	}
	if err := b.Change(2, 3, nil); err == nil {
		t.Fatal("空缓冲外的 Change 应失败")
	}

	e := &Engine{}
	e.saveUndo()
	if !e.undoValid {
		t.Fatal("saveUndo 应武装撤销")
	}
}

type badReader struct{}

func (badReader) Read([]byte) (int, error) { return 0, errors.New("读取故障") }

func TestReadError(t *testing.T) {
	st, files := memoryFiles(nil)
	var out, diag bytes.Buffer
	eng := &Engine{Out: &out, Diag: &diag, Files: files}
	eng.Start(badReader{})
	if code := eng.Run(badReader{}); code != 1 || diag.String() != "?\n" {
		t.Fatalf("读取故障: code=%d diag=%q", code, diag.String())
	}

	out.Reset()
	diag.Reset()
	eng = &Engine{Out: &out, Diag: &diag, Files: files}
	eng.Start(badReader{})
	if eng.Execute("a") {
		t.Fatal("a 不应退出")
	}
	if diag.String() != "?\n" {
		t.Fatalf("输入模式故障: %q", diag.String())
	}
	_ = st
}

func TestReportErrorHook(t *testing.T) {
	_, files := memoryFiles(nil)
	var out, diag bytes.Buffer
	var details []string
	eng := &Engine{Out: &out, Diag: &diag, Files: files,
		ReportError: func(detail string) { details = append(details, detail) }}
	eng.Start(strings.NewReader(""))
	eng.Fail("装配层失败")
	if len(details) != 1 || details[0] != "装配层失败" {
		t.Fatalf("钩子: %v", details)
	}
	if !eng.HadError() {
		t.Fatal("Fail 应置失败位")
	}
}

func TestJoinMarksAndErrors(t *testing.T) {
	expect(t, drive(nil, "a\none\ntwo\n.\n1kx\n1,2j\n'x p\n", nil), "onetwo\n", "?\n", 1)
	expect(t, drive(nil, "a\none\n.\n0,1j\n", nil), "", "?\n?\n", 1)
	expect(t, drive(nil, "a\none\n.\n1,9j\n", nil), "", "?\n?\n", 1)
}

func TestChangeEmptyBuffer(t *testing.T) {
	expect(t, drive(nil, "0,0c\ntext\n.\np\n", nil), "text\n", "?\n", 1)
	expect(t, drive(nil, "a\none\ntwo\n.\n1,2c\n.\n", nil), "", "?\n", 1)
}

func TestGlobalDeleteShift(t *testing.T) {
	expect(t, drive(nil, "a\nfoo\nx\nfoo\n.\n1,$g/foo/d\n,n\n", nil), "1\tx\n", "?\n", 1)
}

func TestDestErrors(t *testing.T) {
	expect(t, drive(nil, "a\none\ntwo\n.\n1t9\n", nil), "", "?\n?\n", 1)
	expect(t, drive(nil, "a\none\ntwo\n.\n1m9\n", nil), "", "?\n?\n", 1)
	expect(t, drive(nil, "a\none\ntwo\n.\n1m'z\n", nil), "", "?\n?\n", 1)
}

func TestShellRecallEmpty(t *testing.T) {
	expect(t, drive(nil, "!!\n", func(e *Engine) { e.Shell = func(string) error { return nil } }), "", "?\n", 1)
	expect(t, drive(nil, "!cat %\n", func(e *Engine) { e.Shell = func(string) error { return nil } }), "", "?\n", 1)
}

func TestClassDelimiters(t *testing.T) {
	expect(t, drive(nil, "a\nzx\n.\n/[[:alpha:]]x/\n", nil), "zx\n", "?\n", 1)
	expect(t, drive(nil, "a\nax\n.\n/[.a.]x/\n", nil), "ax\n", "?\n", 1)
	expect(t, drive(nil, "a\n=\n.\n/[=]/\n", nil), "=\n", "?\n", 1)
}

func TestSubstituteEOFContinuation(t *testing.T) {
	got := drive(nil, "a\nx\n.\ns/a/\nq", nil)
	if got.code != 1 || got.diag != "?\n?\n" {
		t.Fatalf("EOF 续行: %+v", got)
	}
}

func TestDefensiveBufferErrors(t *testing.T) {
	eng := &Engine{}
	if err := eng.deleteLines(1, 2); err == nil {
		t.Fatal("空缓冲删除应失败")
	}
	if err := eng.appendLines(5, nil); err == nil {
		t.Fatal("越界插入应失败")
	}
	eng2 := &Engine{}
	if err := eng2.changeLines(1, 2, nil); err == nil {
		t.Fatal("空缓冲外的 c 应失败")
	}
}

func TestPrintSuffixes(t *testing.T) {
	expect(t, drive(nil, "a\none\ntwo\n.\n1dp\n", nil), "two\n", "?\n", 1)
	expect(t, drive(nil, "a\none\n.\n1ap\nx\n.\n", nil), "x\n", "?\n", 1)
	expect(t, drive(nil, "a\none\n.\n1ip\nx\n.\n", nil), "x\n", "?\n", 1)
	expect(t, drive(nil, "a\none\n.\n1cp\nx\n.\n", nil), "x\n", "?\n", 1)
	expect(t, drive(nil, "a\none\ntwo\n.\n1,2jp\n", nil), "onetwo\n", "?\n", 1)
	expect(t, drive(nil, "a\none\n.\n1kxp\n", nil), "one\n", "?\n", 1)
	expect(t, drive(nil, "a\none\ntwo\n.\n1m2p\n", nil), "one\n", "?\n", 1)
	expect(t, drive(nil, "a\none\ntwo\n.\n1t0p\n,n\n", nil),
		"one\n1\tone\n2\tone\n3\ttwo\n", "?\n", 1)
	expect(t, drive(nil, "a\none\n.\n=p\n", nil), "1\none\n", "?\n", 1)

	expect(t, drive(nil, "a\none\n.\n1dq\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\n.\n1\n\n", nil), "one\ntwo\n", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n3kz\n1d\n'z p\n", nil), "three\n", "?\n", 1)
}

func TestSuffixEdgePaths(t *testing.T) {

	expect(t, drive(nil, "a\none\n.\n1dp\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\n.\n1m2q\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\n.\nd\nup\n", nil), "two\n", "?\n", 1)

	expect(t, drive(nil, "a\none\n.\ns/y/z/\nhp\n", nil), "one\n", "?\n没有匹配\n?\n", 1)
	expect(t, drive(nil, "a\none\n.\nHp\ns/y/z/\n", nil), "one\n", "?\n没有匹配\n?\n警告：缓冲已修改\n", 1)
}

func TestEscapedDelimiters(t *testing.T) {

	expect(t, drive(nil, "a\nab\n.\ns|a|b\\|c|\np\n", nil), "b|cb\n", "?\n", 1)

	expect(t, drive(nil, "a\nx/y\n.\ns/x\\/y/Z/\np\n", nil), "Z\n", "?\n", 1)

	got := drive(nil, "H\na\nfoo\n.\n1,$g/foo/s/a\\/b/X/\nq\nq\n", nil)
	if got.code != 1 || !strings.Contains(got.diag, "没有匹配") {
		t.Fatalf("g 转义: %+v", got)
	}

	expect(t, drive(nil, "a\nabc\n.\n/a[=b=]c/\n", nil), "abc\n", "?\n", 1)
}

func TestSubstituteArgumentErrors(t *testing.T) {

	expect(t, drive(nil, "a\nx\n.\ns x/y/\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\nx\n.\ns/a\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\nx\n.\ns//y/\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\nx\n.\ns/x/%/\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\nax\n.\ns/a/b/5\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\nfoo\n.\n1kx\ns/foo/bar/\n'x p\n", nil), "bar\n", "?\n", 1)

	expect(t, drive(nil, "a\nx\ty\n.\nl\n", nil), "x\\ty$\n", "?\n", 1)
}

func TestAddressErrorSweep(t *testing.T) {
	for _, line := range []string{
		"9d\n", "9j\n", "9m1\n", "9t1\n", "9n\n", "9l\n", "9s/a/b/\n",
		"9z\n", "9r\n", "9=\n", "9a\n", "9i\n", "9kx\n",
		"1,2m1\n", "9,1d\n",
	} {
		got := drive(nil, line, nil)
		if got.code != 1 || got.diag != "?\n" {
			t.Fatalf("%q: code=%d diag=%q out=%q", line, got.code, got.diag, got.out)
		}
	}

	_, files := memoryFiles(nil)
	files.Write = func(name string, data string) error {
		return fmt.Errorf("%s: 写出失败", name)
	}
	var out, diag bytes.Buffer
	eng := &Engine{Out: &out, Diag: &diag, Files: files}
	eng.Start(strings.NewReader("x\n.\n"))
	eng.Execute("f g")
	if eng.Execute("wq nodir/x") {
		t.Fatal("写出失败不应退出")
	}
	if !eng.HadError() || !strings.Contains(diag.String(), "写出失败") {
		t.Fatalf("wq 失败: %q", diag.String())
	}
}

func TestMiscCommandEdges(t *testing.T) {

	expect(t, drive(nil, "a\none\n.\n1j\nn\n", nil), "1\tone\n", "?\n", 1)

	expect(t, drive(map[string]string{"g": "one\n"}, "f g\na\nfoo\n.\nw g\n1,$g/foo/e\n", nil), "g\n4\n4\n", "", 0)

	expect(t, drive(nil, "f !x\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "'xh\n", nil), "", "?\n", 1)
}

func TestAddressEdgesMore(t *testing.T) {

	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n,2d\n,n\n", nil), "1\tthree\n", "?\n", 1)

	expect(t, drive(nil, "'K p\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\n.\n2\n.p\n+0p\n", nil), "two\ntwo\ntwo\n", "?\n", 1)

	expect(t, drive(nil, "a\nfoo\n.\n/foo\n", nil), "foo\n", "?\n", 1)

	expect(t, drive(nil, "/x/\n", nil), "", "?\n", 1)

	expect(t, drive(nil, "a\none\n.\n/x/\n", nil), "", "?\n?\n", 1)

	expect(t, drive(nil, "a\none\ntwo\nthree\n.\n1 + 1\n", nil), "three\n", "?\n", 1)
}

func TestMarkShiftWithNewline(t *testing.T) {
	expect(t, drive(nil, "a\nfoo\n.\n1kx\n1s/foo/x\\\ny/\n'x p\n", nil), "y\n", "?\n", 1)
}

func TestBufferChangeOffsets(t *testing.T) {
	var b Buffer
	if err := b.Append(0, []string{"a", "b"}); err != nil {
		t.Error(err)
	}
	if err := b.Change(0, 1, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if b.Last() != 2 || b.Lines[0] != "x" {
		t.Fatalf("Change 0 地址: %+v", b)
	}
	if err := b.Change(1, 0, []string{"z"}); err != nil {
		t.Fatal(err)
	}
	if b.Last() != 2 || b.Lines[0] != "z" {
		t.Fatalf("Change 末地址 0: %+v", b)
	}
}

func TestHelperEdges(t *testing.T) {
	if got := skipBlank("  x", 0); got != 2 {
		t.Fatalf("skipBlank: %d", got)
	}
	if got := skipBlank("x", 5); got != 5 {
		t.Fatalf("skipBlank 越界: %d", got)
	}
	if got := trimBlank("  a\t"); got != "a" {
		t.Fatalf("trimBlank: %q", got)
	}
	if got := lastLine("a\nb\nc"); got != "c" {
		t.Fatalf("lastLine: %q", got)
	}
	if style, err := printSuffix("q"); style != 0 || err == nil {
		t.Fatalf("printSuffix 非法: %q %v", style, err)
	}
	if style, err := printSuffix(""); style != 0 || err != nil {
		t.Fatalf("printSuffix 空: %q %v", style, err)
	}
	e := &Engine{}
	if delim, n := e.commandDelimiter(""); delim != 0 || n != 0 {
		t.Fatalf("commandDelimiter 空: %q %d", delim, n)
	}
	if delim, n := e.commandDelimiter("ab"); delim != 'a' || n != 1 {
		t.Fatalf("commandDelimiter: %q %d", delim, n)
	}
}

func TestExecuteAPIError(t *testing.T) {
	_, files := memoryFiles(nil)
	var out, diag bytes.Buffer
	eng := &Engine{Out: &out, Diag: &diag, Files: files}
	eng.Start(strings.NewReader(""))
	if eng.Execute("Y") {
		t.Fatal("未知命令不应退出")
	}
	if !eng.HadError() || diag.String() != "?\n" {
		t.Fatalf("未知命令: %+v", diag.String())
	}
}

func TestGlobalEdgePaths(t *testing.T) {

	expect(t, drive(nil, "a\nfoo\n.\n1,$g/foo\n", nil), "foo\n", "?\n", 1)

	expect(t, drive(nil, "a\nfoo\n.\n1,$g/foo/s//x/\\\n", nil), "", "?\n?\n", 1)
}

func TestReadFileRemembersName(t *testing.T) {
	expect(t, drive(map[string]string{"g": "one\n"}, "r g\nf\n", nil), "4\ng\n", "?\n", 1)
}
