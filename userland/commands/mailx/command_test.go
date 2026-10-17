package mailx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
)

const twoMessages = "From alice@example.org Thu Jan  1 00:00:00 1970\n" +
	"From: alice\n" +
	"Subject: hello\n" +
	"\n" +
	"body one\n" +
	"From bob@example.org Fri Jan  2 00:00:00 1970\n" +
	"Subject: lunch\n" +
	"\n" +
	"body two\n"

func run(t *testing.T, dir string, args []string, input string) commandtest.Result {
	t.Helper()
	return commandtest.RunWithIdentity(t, Run, dir, args, input, nil, identity.SingleUser{})
}

func installMailbox(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, "var", "mail")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "sandbox"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNoMail(t *testing.T) {
	dir := t.TempDir()
	result := run(t, dir, nil, "")
	if result.Code != 1 || result.Stdout != "" || result.Stderr != "mailx: No mail for sandbox\n" {
		t.Fatalf("无信箱: %+v", result)
	}
	installMailbox(t, dir, "")
	result = run(t, dir, nil, "")
	if result.Code != 1 || result.Stderr != "mailx: No mail for sandbox\n" {
		t.Fatalf("空信箱: %+v", result)
	}
}

func TestExistFlag(t *testing.T) {
	dir := t.TempDir()
	result := run(t, dir, []string{"-e"}, "")
	if result.Code != 1 || result.Stdout != "" || result.Stderr != "" {
		t.Fatalf("-e 无邮件: %+v", result)
	}
	installMailbox(t, dir, twoMessages)
	result = run(t, dir, []string{"-e"}, "")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("-e 有邮件: %+v", result)
	}
}

func TestReadLoop(t *testing.T) {
	dir := t.TempDir()
	installMailbox(t, dir, twoMessages)
	result := run(t, dir, nil, "p\nh\nq\n")
	if result.Code != 0 {
		t.Fatalf("循环: %+v", result)
	}
	want := "Message 1:\n" +
		"From: alice@example.org\n" +
		"Subject: hello\n" +
		"\n" +
		"body one\n" +

		">   1 alice@example.org hello\n" +
		"    2 bob@example.org lunch\n" +

		"保留 2 条消息，删除 0 条\n"
	if result.Stdout != want {
		t.Fatalf("stdout: %q", result.Stdout)
	}

	result = run(t, dir, nil, "2\n-\n+\nq\n")
	if result.Code != 0 {
		t.Fatalf("跳转: %+v", result)
	}
	printTwo := "Message 2:\nFrom: bob@example.org\nSubject: lunch\n\nbody two\n"
	printOne := "Message 1:\nFrom: alice@example.org\nSubject: hello\n\nbody one\n"
	if result.Stdout != printTwo+printOne+printTwo+"保留 2 条消息，删除 0 条\n" {
		t.Fatalf("跳转输出: %q", result.Stdout)
	}

	result = run(t, dir, nil, "9\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "消息号超出范围") {
		t.Fatalf("越界: %+v", result)
	}
	result = run(t, dir, nil, "1\n-\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "没有更多消息") || !strings.Contains(result.Stdout, "保留") {
		t.Fatalf("边界: %+v", result)
	}
	result = run(t, dir, nil, "bogus\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "未知命令: bogus") {
		t.Fatalf("未知命令: %+v", result)
	}
}

func TestDeleteRewrite(t *testing.T) {
	dir := t.TempDir()
	installMailbox(t, dir, twoMessages)
	result := run(t, dir, nil, "d 1\nq\n")
	if result.Code != 0 {
		t.Fatalf("删除: %+v", result)
	}
	if result.Stdout != "保留 1 条消息，删除 1 条\n" {
		t.Fatalf("计数: %q", result.Stdout)
	}
	body := read(t, dir, filepath.Join("var", "mail", "sandbox"))
	if !strings.HasPrefix(body, "From bob@example.org") || strings.Contains(body, "alice") {
		t.Fatalf("重写后的信箱: %q", body)
	}

	if _, err := os.Stat(filepath.Join(dir, "var", "mail", "sandbox"+mailxTemp)); err == nil {
		t.Fatal("临时文件不应残留")
	}
}

func TestExitWithoutSave(t *testing.T) {
	dir := t.TempDir()
	installMailbox(t, dir, twoMessages)
	result := run(t, dir, nil, "d\nx\n")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("x: %+v", result)
	}
	if body := read(t, dir, filepath.Join("var", "mail", "sandbox")); !strings.Contains(body, "alice") {
		t.Fatalf("x 不应重写信箱: %q", body)
	}
}

func TestSaveWrite(t *testing.T) {
	dir := t.TempDir()
	installMailbox(t, dir, twoMessages)
	result := run(t, dir, nil, "s saved\n2\nw body\nq\n")
	if result.Code != 0 {
		t.Fatalf("保存: %+v", result)
	}
	if body := read(t, dir, "saved"); !strings.HasPrefix(body, "From alice@example.org") {
		t.Fatalf("s 保存: %q", body)
	}
	if body := read(t, dir, "body"); body != "body two\n" {
		t.Fatalf("w 保存: %q", body)
	}
}

func TestFileFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "box")
	if err := os.WriteFile(path, []byte(twoMessages), 0o600); err != nil {
		t.Fatal(err)
	}
	result := run(t, dir, []string{"-f", "box"}, "p\nq\n")
	if result.Code != 0 || !strings.Contains(result.Stdout, "body one") {
		t.Fatalf("-f: %+v", result)
	}

	result = run(t, dir, []string{"-f", "missing"}, "")
	if result.Code != 1 || result.Stderr != "mailx: No mail for sandbox\n" {
		t.Fatalf("-f 缺失: %+v", result)
	}
}

func TestSendFailsHonestly(t *testing.T) {
	dir := t.TempDir()
	result := run(t, dir, []string{"-s", "greeting", "alice@example.org", "bob@example.net"}, "hello body\n")
	if result.Code != 1 {
		t.Fatalf("发送: %+v", result)
	}
	if result.Stderr != "mailx: 客体没有邮件传输设施，正文已存 dead.letter\n" {
		t.Fatalf("诊断: %q", result.Stderr)
	}
	if body := read(t, dir, "dead.letter"); body != "hello body\n" {
		t.Fatalf("dead.letter: %q", body)
	}

	result = run(t, dir, []string{"a@b,c@d", " d@e "}, "x\n")
	if result.Code != 1 {
		t.Fatalf("逗号收件人: %+v", result)
	}

	result = run(t, dir, []string{"-s", "x", ","}, "")
	if result.Code != 2 || !strings.Contains(result.Stderr, "缺少收件人") {
		t.Fatalf("无收件人: %+v", result)
	}
}

func TestRejectedOptions(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"-u", "bob"}, {"-i"}, {"-n"}, {"-N"}, {"-F"}} {
		result := run(t, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("选项 %v: %+v", args, result)
		}
	}
}

func TestSkippedFrames(t *testing.T) {
	dir := t.TempDir()
	installMailbox(t, dir, "散行\nFrom x Thu Jan 1 00:00:00 1970\n没有头\n")
	result := run(t, dir, []string{"-e"}, "")
	_ = result
	result = run(t, dir, nil, "q\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "解析失败") {
		t.Fatalf("跳过: %+v", result)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDefaultCommandAndSaveErrors(t *testing.T) {
	dir := t.TempDir()
	installMailbox(t, dir, twoMessages)

	result := run(t, dir, nil, "\np\nq\n")
	if result.Code != 0 || strings.Count(result.Stdout, "Message 1:") != 2 {
		t.Fatalf("缺省命令: %+v", result)
	}

	result = run(t, dir, nil, "s\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "需要一个文件操作数") {
		t.Fatalf("s 无操作数: %+v", result)
	}

	result = run(t, dir, nil, "s nodir/f\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "nodir/f") {
		t.Fatalf("s 失败: %+v", result)
	}
}

func TestQuitWriteFailure(t *testing.T) {
	dir := t.TempDir()
	box := filepath.Join(dir, "box")
	if err := os.WriteFile(box, []byte(twoMessages), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(box+mailxTemp, 0o755); err != nil {
		t.Fatal(err)
	}
	result := run(t, dir, []string{"-f", "box"}, "d 1\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "删除落盘失败") {
		t.Fatalf("落盘失败: %+v", result)
	}
}

func TestDeadLetterFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dead.letter"), 0o755); err != nil {
		t.Fatal(err)
	}
	result := run(t, dir, []string{"alice@example.org"}, "body\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "无法写入 dead.letter") {
		t.Fatalf("dead.letter 失败: %+v", result)
	}
}

func TestMissingIdentity(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.RunWith(t, Run, dir, nil, "", commandtest.Options{})
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("缺身份读取: %+v", result)
	}
	result = commandtest.RunWith(t, Run, dir, []string{"-e"}, "", commandtest.Options{})
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("缺身份探测: %+v", result)
	}
	result = commandtest.RunWith(t, Run, dir, []string{"-f"}, "", commandtest.Options{})
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("缺身份 -f: %+v", result)
	}
}

func TestMessageWithoutSubject(t *testing.T) {
	dir := t.TempDir()
	installMailbox(t, dir, "From x Thu Jan  1 00:00:00 1970\nFrom: x\n\nbody\n")
	result := run(t, dir, nil, "p\nh\nq\n")
	if result.Code != 0 {
		t.Fatalf("无主题: %+v", result)
	}

	want := "Message 1:\nFrom: x\n\nbody\n>   1 x \n保留 1 条消息，删除 0 条\n"
	if result.Stdout != want {
		t.Fatalf("stdout: %q", result.Stdout)
	}
}

func TestLoopEdgeCommands(t *testing.T) {
	dir := t.TempDir()
	installMailbox(t, dir, twoMessages)

	result := run(t, dir, nil, "bogus\nx\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "未知命令") {
		t.Fatalf("bogus+x: %+v", result)
	}

	result = run(t, dir, nil, "d x\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "消息号无效") {
		t.Fatalf("d x: %+v", result)
	}
	result = run(t, dir, nil, "d 9\nq\n")
	if result.Code != 1 || !strings.Contains(result.Stderr, "消息号无效") {
		t.Fatalf("d 9: %+v", result)
	}

	result = run(t, dir, nil, "d 1\nh\nq\n")
	if result.Code != 0 || !strings.Contains(result.Stdout, "alice@example.org hello*\n") {
		t.Fatalf("删除标记: %+v", result)
	}

	result = run(t, dir, []string{"-f", "a", "b"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("-f 多操作数: %+v", result)
	}

	result = run(t, dir, []string{"-z"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("-z: %+v", result)
	}

	result = run(t, dir, []string{"-f", "."}, "")
	if result.Code != 1 || result.Stderr != "mailx: No mail for sandbox\n" {
		t.Fatalf("-f 目录: %+v", result)
	}
}
