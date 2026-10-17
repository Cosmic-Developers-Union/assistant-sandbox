package ar

import (
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const fixedTime = 1_000_000_000

func stampFile(t *testing.T, dir, name string, offsetSeconds int64) {
	t.Helper()
	moment := time.Unix(0, fixedTime+offsetSeconds*1e9)
	if err := os.Chtimes(filepath.Join(dir, name), moment, moment); err != nil {
		t.Fatal(err)
	}
}

func rawHeader(name, mode string, size int) string {
	var b strings.Builder
	b.WriteString(name + "/")
	for i := len(name) + 1; i < 16; i++ {
		b.WriteByte(' ')
	}
	b.WriteString("1           ")
	b.WriteString("0     0     ")
	b.WriteString(mode)
	for i := len(mode); i < 8; i++ {
		b.WriteByte(' ')
	}
	sizeText := itoa(size)
	b.WriteString(sizeText)
	for i := len(sizeText); i < 10; i++ {
		b.WriteByte(' ')
	}
	b.WriteString("`\n")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func wantArchive(t *testing.T, dir, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("档案 %s 内容不符：\n got %q\nwant %q", name, data, want)
	}
}

func readMembers(t *testing.T, dir, name string) []member {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	members, err := parseArchive(data)
	if err != nil {
		t.Fatal(err)
	}
	return members
}

func memberNames(members []member) string {
	var out []string
	for _, m := range members {
		out = append(out, m.name)
	}
	return strings.Join(out, ",")
}

func TestCreateListPrintExtract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha"), []byte("hello"), 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta"), []byte("wo"), 0o644); err != nil {
		t.Error(err)
	}
	stampFile(t, dir, "alpha", 0)
	stampFile(t, dir, "beta", 0)

	result := commandtest.Run(t, Run, dir, []string{"rc", "lib.a", "alpha"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("rc 创建应安静: %+v", result)
	}
	want := "!<arch>\n" + rawHeader("alpha", "644", 5) + "hello" + "\n"
	wantArchive(t, dir, "lib.a", want)

	result = commandtest.Run(t, Run, dir, []string{"q", "lib.a", "beta"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("q 追加: %+v", result)
	}
	want += rawHeader("beta", "644", 2) + "wo"
	wantArchive(t, dir, "lib.a", want)

	if err := os.WriteFile(filepath.Join(dir, "odd"), []byte("abc"), 0o644); err != nil {
		t.Error(err)
	}
	stampFile(t, dir, "odd", 0)
	result = commandtest.Run(t, Run, dir, []string{"q", "lib.a", "odd"}, "")
	if result.Code != 0 {
		t.Fatalf("rq 失败: %+v", result)
	}
	want += rawHeader("odd", "644", 3) + "abc" + "\n"
	wantArchive(t, dir, "lib.a", want)

	result = commandtest.Run(t, Run, dir, []string{"t", "lib.a"}, "")
	if result.Code != 0 || result.Stdout != "alpha\nbeta\nodd\n" {
		t.Fatalf("t 列表: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"tv", "lib.a"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "644 5 alpha\n") || !strings.Contains(result.Stdout, "644 3 odd\n") {
		t.Fatalf("tv 列表: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"p", "lib.a", "beta"}, "")
	if result.Code != 0 || result.Stdout != "wo" {
		t.Fatalf("p 打印: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-t", "lib.a", "alpha"}, "")
	if result.Code != 0 || result.Stdout != "alpha\n" {
		t.Fatalf("长划形态: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"xv", "lib.a", "odd"}, "")
	if result.Code != 0 || result.Stdout != "x - odd\n" {
		t.Fatalf("xv 解出: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "odd"))
	if err != nil || string(data) != "abc" {
		t.Fatalf("解出内容: %v %q", err, data)
	}
}

func TestFormatRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m1"), []byte("one"), 0o600); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m22"), []byte("four"), 0o640); err != nil {
		t.Error(err)
	}
	stampFile(t, dir, "m1", 0)
	stampFile(t, dir, "m22", 0)
	result := commandtest.Run(t, Run, dir, []string{"rc", "a.a", "m1", "m22"}, "")
	if result.Code != 0 {
		t.Fatalf("rc: %+v", result)
	}
	members := readMembers(t, dir, "a.a")
	if len(members) != 2 || members[0].name != "m1" || string(members[0].data) != "one" ||
		members[1].name != "m22" || string(members[1].data) != "four" {
		t.Fatalf("成员不符: %+v", members)
	}
	if members[0].mode != 0o600 || members[1].mode != 0o640 {
		t.Fatalf("模式应来自文件: %+v", members)
	}
	if members[0].mtime != 1 || members[1].mtime != 1 {
		t.Fatalf("mtime 应来自文件: %+v", members)
	}
}

func TestReplaceDeleteMove(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Error(err)
		}
	}
	write("a", "A1")
	write("b", "BB")
	commandtest.Run(t, Run, dir, []string{"rc", "x.a", "a", "b"}, "")

	write("a", "A2")
	if r := commandtest.Run(t, Run, dir, []string{"rv", "x.a", "a"}, ""); r.Code != 0 || r.Stdout != "r - a\n" {
		t.Fatalf("r 替换: %+v", r)
	}
	members := readMembers(t, dir, "x.a")
	if len(members) != 2 || members[0].name != "a" || string(members[0].data) != "A2" {
		t.Fatalf("替换后: %+v", members)
	}

	write("c", "C")
	if r := commandtest.Run(t, Run, dir, []string{"rva", "a", "x.a", "c"}, ""); r.Code != 0 || r.Stdout != "r - c\n" {
		t.Fatalf("ra 插入: %+v", r)
	}
	write("d", "D")
	if r := commandtest.Run(t, Run, dir, []string{"rb", "b", "x.a", "d"}, ""); r.Code != 0 {
		t.Fatalf("rb 插入: %+v", r)
	}
	if got := memberNames(readMembers(t, dir, "x.a")); got != "a,c,d,b" {
		t.Fatalf("插入后顺序: %s", got)
	}

	if r := commandtest.Run(t, Run, dir, []string{"m", "x.a", "a"}, ""); r.Code != 0 {
		t.Fatalf("m 移动: %+v", r)
	}
	if got := memberNames(readMembers(t, dir, "x.a")); got != "c,d,b,a" {
		t.Fatalf("m 后顺序: %s", got)
	}
	if r := commandtest.Run(t, Run, dir, []string{"mva", "a", "x.a", "b"}, ""); r.Code != 0 || r.Stdout != "m - b\n" {
		t.Fatalf("ma 移动: %+v", r)
	}
	if got := memberNames(readMembers(t, dir, "x.a")); got != "c,d,a,b" {
		t.Fatalf("ma 后顺序: %s", got)
	}

	if r := commandtest.Run(t, Run, dir, []string{"dv", "x.a", "c", "ghost"}, ""); r.Code != 0 || r.Stdout != "d - c\n" {
		t.Fatalf("d 删除: %+v", r)
	}
	if got := memberNames(readMembers(t, dir, "x.a")); got != "d,a,b" {
		t.Fatalf("d 后: %s", got)
	}
}

func TestOnlyNewer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Error(err)
	}
	stampFile(t, dir, "m", 0)
	commandtest.Run(t, Run, dir, []string{"rc", "u.a", "m"}, "")

	if err := os.WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Error(err)
	}
	stampFile(t, dir, "m", 120)
	if r := commandtest.Run(t, Run, dir, []string{"ruv", "u.a", "m"}, ""); r.Code != 0 || r.Stdout != "r - m\n" {
		t.Fatalf("u 替换: %+v", r)
	}
	if string(readMembers(t, dir, "u.a")[0].data) != "new" {
		t.Fatal("u 应替换更老的成员")
	}

	if r := commandtest.Run(t, Run, dir, []string{"ruv", "u.a", "m"}, ""); r.Code != 0 || r.Stdout != "" {
		t.Fatalf("u 跳过: %+v", r)
	}

	if err := os.WriteFile(filepath.Join(dir, "extra"), []byte("E"), 0o644); err != nil {
		t.Error(err)
	}
	if r := commandtest.Run(t, Run, dir, []string{"ru", "u.a", "extra"}, ""); r.Code != 0 {
		t.Fatalf("u 新增: %+v", r)
	}
	if len(readMembers(t, dir, "u.a")) != 2 {
		t.Fatal("u 不阻止新成员入档")
	}
}

func TestExtractRestoresMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Error(err)
	}
	stampFile(t, dir, "m", 0)
	commandtest.Run(t, Run, dir, []string{"rc", "t.a", "m"}, "")
	if err := os.Remove(path); err != nil {
		t.Error(err)
	}
	if r := commandtest.Run(t, Run, dir, []string{"x", "t.a"}, ""); r.Code != 0 {
		t.Fatalf("x: %+v", r)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Unix() != 1 {
		t.Fatalf("mtime 未回填: 文件 %d，档案 1", info.ModTime().Unix())
	}
}

func TestFailurePaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("not an archive"), 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "member"), []byte("x"), 0o644); err != nil {
		t.Error(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "ext.a"), []byte("!<arch>\n"+rawHeader("//", "0", 0)), 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sym.a"), []byte("!<arch>\n"+rawHeader("/", "0", 0)), 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cut.a"), []byte("!<arch>\nshort"), 0o644); err != nil {
		t.Error(err)
	}

	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"t", "plain"}, "ar: plain: 档案格式不正确"},
		{[]string{"r", "plain", "member"}, "ar: plain: 档案格式不正确"},
		{[]string{"t", "absent.a"}, "ar: absent.a: 档案不存在"},
		{[]string{"x", "absent.a"}, "ar: absent.a: 档案不存在"},
		{[]string{"t", "ext.a"}, "不支持的成员段"},
		{[]string{"t", "sym.a"}, "不支持的成员段"},
		{[]string{"t", "cut.a"}, "档案格式不正确"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 1 || !strings.Contains(result.Stderr, test.want) {
			t.Fatalf("%v: code=%d stderr=%q", test.args, result.Code, result.Stderr)
		}
	}

	result := commandtest.Run(t, Run, dir, []string{"rc", "new.a", "ghost"}, "")
	if result.Code != 1 {
		t.Fatalf("r 缺文件: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.a")); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("缺文件时不应创建档案: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "m"), []byte("d"), 0o644); err != nil {
		t.Error(err)
	}
	commandtest.Run(t, Run, dir, []string{"rc", "ok.a", "m"}, "")
	for _, args := range [][]string{{"t", "ok.a", "nope"}, {"p", "ok.a", "nope"}, {"x", "ok.a", "nope"}} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 1 || !strings.Contains(result.Stderr, "nope: 档案里没有那个成员") {
			t.Fatalf("%v: %+v", args, result)
		}
	}

	before := memberNames(readMembers(t, dir, "ok.a"))
	result = commandtest.Run(t, Run, dir, []string{"ma", "m", "ok.a", "nope"}, "")
	if result.Code != 1 || memberNames(readMembers(t, dir, "ok.a")) != before {
		t.Fatalf("m 锚点缺失: %+v", result)
	}

	edge := strings.Repeat("e", 15)
	if err := os.WriteFile(filepath.Join(dir, edge), []byte("E"), 0o644); err != nil {
		t.Error(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"q", "ok.a", edge}, ""); result.Code != 0 {
		t.Fatalf("15 字符名应成功: %+v", result)
	}

	long := strings.Repeat("n", 16)
	if err := os.WriteFile(filepath.Join(dir, long), []byte("L"), 0o644); err != nil {
		t.Error(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"q", "ok.a", long}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "扩展名段") {
		t.Fatalf("长名: %+v", result)
	}

	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "f"), []byte("S"), 0o644); err != nil {
		t.Error(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"q", "ok.a", "sub/f"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "成员名不能包含") {
		t.Fatalf("带斜杠成员: %+v", result)
	}
}

func TestInputBudget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.a"), make([]byte, maxInputBytes+1), 0o644); err != nil {
		t.Error(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"t", "big.a"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "累计输入超过 4 MiB") {
		t.Fatalf("预算: %+v", result)
	}

	if err := os.WriteFile(filepath.Join(dir, "pad"), make([]byte, 2<<20), 0o644); err != nil {
		t.Error(err)
	}
	commandtest.Run(t, Run, dir, []string{"rc", "half.a", "pad"}, "")
	if err := os.WriteFile(filepath.Join(dir, "big2"), make([]byte, 3<<20), 0o644); err != nil {
		t.Error(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"q", "half.a", "big2"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "累计输入超过 4 MiB") {
		t.Fatalf("成员预算: %+v", result)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{}, "缺少操作键"},
		{[]string{"lib.a"}, "不支持的键或修饰符"},
		{[]string{"z", "lib.a"}, "不支持的键或修饰符"},
		{[]string{"rd", "lib.a"}, "一次只能给一个操作键"},
		{[]string{"tu", "lib.a"}, "修饰符 u 只能与键 r"},
		{[]string{"ta", "lib.a"}, "缺少档案名"},
		{[]string{"rva"}, "定位修饰符 a/b/i 需要位置成员名"},
		{[]string{"ta", "x", "y"}, "定位修饰符 a/b/i 只能与键 r 或 m"},
		{[]string{"d", "lib.a"}, "需要至少一个成员名"},
		{[]string{"m", "lib.a"}, "需要至少一个成员名"},
		{[]string{"r"}, "缺少档案名"},
		{[]string{"-x"}, "缺少档案名"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 2 || !strings.Contains(result.Stderr, test.want) {
			t.Fatalf("%v: code=%d stderr=%q", test.args, result.Code, result.Stderr)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "m"), []byte("d"), 0o644); err != nil {
		t.Error(err)
	}
	if r := commandtest.Run(t, Run, dir, []string{"rrc", "dup.a", "m"}, ""); r.Code != 0 {
		t.Fatalf("rr 应合法: %+v", r)
	}
	if r := commandtest.Run(t, Run, dir, []string{"r", "quiet.a", "m"}, ""); r.Code != 0 || !strings.Contains(r.Stderr, "创建档案 quiet.a") {
		t.Fatalf("无 c 应有创建提示: %+v", r)
	}
}

func TestEmptyArchiveCreation(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"qc", "b.a"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("qc 安静创建: %+v", result)
	}
	wantArchive(t, dir, "b.a", "!<arch>\n")
}

func TestStreamWriteFailure(t *testing.T) {
	out := &stickyWriter{writer: failWriter{}}
	w := &work{out: out, diag: &stickyWriter{writer: io.Discard}}
	code := w.print(&invocation{key: 'p'}, []member{{name: "m", data: []byte("x")}})
	if out.err == nil || code != 0 {
		t.Fatalf("写失败应被记住: err=%v code=%d", out.err, code)
	}
	if _, err := out.Write([]byte("more")); err == nil {
		t.Fatal("粘连失败")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestErrorIdentities(t *testing.T) {
	if !isNotExist(errno.NotFound) || !isNotExist(stdfs.ErrNotExist) {
		t.Fatal("isNotExist 应识别两套身份")
	}
	if !isNotExist(&stdfs.PathError{Op: "stat", Path: "x", Err: stdfs.ErrNotExist}) {
		t.Fatal("isNotExist 应识别 PathError 包装")
	}
	if !isExist(errno.Exist) || !isExist(stdfs.ErrExist) {
		t.Fatal("isExist 应识别两套身份")
	}
	if isNotExist(errors.New("别的错")) || isExist(errors.New("别的错")) {
		t.Fatal("普通错误不应误判")
	}
}

func TestInsertHelpers(t *testing.T) {
	list := []member{{name: "a"}, {name: "b"}}
	if got := memberNames(insertAll(list, 1, []member{{name: "x"}, {name: "y"}})); got != "a,x,y,b" {
		t.Fatalf("insertAll: %s", got)
	}
	if got := memberNames(insertAll(list, 2, nil)); got != "a,b" {
		t.Fatalf("insertAll 空插入: %s", got)
	}
	if got := memberNames(insertAt(list, 0, member{name: "z"})); got != "z,a,b" {
		t.Fatalf("insertAt: %s", got)
	}
	if !membersEqual(list, []member{{name: "a"}, {name: "b"}}) || membersEqual(list, []member{{name: "a"}}) {
		t.Fatal("membersEqual 判断不符")
	}
	if indexOf(list, "b") != 1 || indexOf(list, "zz") != -1 {
		t.Fatal("indexOf 判断不符")
	}
	if !nameIn(list, "a") || nameIn(list, "zz") {
		t.Fatal("nameIn 判断不符")
	}
	if len(nameSet([]string{"a", "a", "b"})) != 2 {
		t.Fatal("nameSet 应去重")
	}
}
