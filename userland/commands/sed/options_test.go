package sed

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestScriptFromFile(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "prog.sed")
	if err := os.WriteFile(script, []byte("s/a/A/\n# 注释行\ns/b/B/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-f", "prog.sed"}, "a\nb\nc\n")
	if result.Code != 0 || result.Stdout != "A\nB\nc\n" {
		t.Fatalf("-f 得到 %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-f", "prog.sed", "-e", "s/c/C/"}, "a\nb\nc\n")
	if result.Code != 0 || result.Stdout != "A\nB\nC\n" {
		t.Fatalf("-f 与 -e 叠加得到 %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-f", "prog.sed", "prog.sed"}, "a\n"); result.Code == 2 {
		t.Fatalf("-f 之后的操作数应是文件: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-f", "absent.sed"}, "a\n")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("缺失脚本文件得到 %+v", result)
	}
}

func TestExtendedRegexpFlag(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"BRE 里 a{2} 是字面量", []string{`s/a{2}/X/`}, "aa\nX\n"},
		{"BRE 转义后才是量词", []string{`s/a\{2\}/X/`}, "X\na{2}\n"},
		{"-E 下花括号是量词", []string{"-E", `s/a{2}/X/`}, "X\na{2}\n"},
		{"-r 与 -E 同义", []string{"-r", `s/a+/X/`}, "X\nX{2}\n"},
		{"-E 的地址也用 ERE", []string{"-E", "-n", `/^a{2}$/p`}, "aa\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, t.TempDir(), test.args, "aa\na{2}\n")
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%v 得到 code=%d stdout=%q stderr=%q", test.args, result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

func TestListCommand(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"转义与控制字符", "-n l", "a\tb\\c\rd\x01\n", "a\\tb\\\\c\\rd\\001$\n"},
		{"普通文本", "-n l", "hello\n", "hello$\n"},
		{"宽度 0 不折行", "-n l 0", "xxxxxxxxxx\n", "xxxxxxxxxx$\n"},
		{"小宽度折行", "-n l 5", "abcdefgh\n", "abcd\\\nefgh$\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSed(t, test.script, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%s 得到 code=%d stdout=%q stderr=%q", test.script, result.Code, result.Stdout, result.Stderr)
			}
		})
	}

	long := ""
	for range 80 {
		long += "x"
	}
	result := runSed(t, "-n l", long+"\n")
	want := long[:69] + "\\\n" + long[69:] + "$\n"
	if result.Code != 0 || result.Stdout != want {
		t.Fatalf("默认宽度折行得到 %q", result.Stdout)
	}

	if result := runSed(t, "-n l x", "a\n"); result.Code != 2 {
		t.Fatalf("非法宽度应报错: %+v", result)
	}

	if result := runSed(t, "-n l", "\xc3\xa9\n"); result.Code != 0 || result.Stdout != "\\303\\251$\n" {
		t.Fatalf("高位字节得到 %q", result.Stdout)
	}
}

func TestInPlaceEditing(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.txt")
	second := filepath.Join(dir, "two.txt")
	if err := os.WriteFile(first, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("a\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-i", "s/a/A/", "one.txt", "two.txt"}, "")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("-i 得到 %+v", result)
	}
	for _, test := range []struct{ path, want string }{{first, "A\nb\n"}, {second, "A\nc\n"}} {
		data, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != test.want {
			t.Fatalf("%s = %q，期望 %q", test.path, data, test.want)
		}
	}

	if result := commandtest.Run(t, Run, dir, []string{"-i.bak", "s/A/X/", "one.txt"}, ""); result.Code != 0 {
		t.Fatalf("-i.bak 得到 %+v", result)
	}
	backup, err := os.ReadFile(first + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "A\nb\n" {
		t.Fatalf("备份内容 = %q", backup)
	}
	if current, err := os.ReadFile(first); err != nil || string(current) != "X\nb\n" {
		t.Fatalf("编辑后内容 = %q, %v", current, err)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-i", "-n", "$p", "one.txt", "two.txt"}, ""); result.Code != 0 {
		t.Fatalf("-i -n $p 得到 %+v", result)
	}
	if data, err := os.ReadFile(first); err != nil || string(data) != "b\n" {
		t.Fatalf("one.txt = %q, %v", data, err)
	}
	if data, err := os.ReadFile(second); err != nil || string(data) != "c\n" {
		t.Fatalf("two.txt = %q, %v", data, err)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-i", "s/a/b/", "absent.txt"}, ""); result.Code != 2 || result.Stderr == "" {
		t.Fatalf("-i 缺文件应报错: %+v", result)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-i", "s/a/b/", "-"}, "a\n"); result.Code != 2 || result.Stderr == "" {
		t.Fatalf("-i - 应报错: %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-i", "s/a/b/"}, "a\n"); result.Code != 2 {
		t.Fatalf("-i 无操作数应报错: %+v", result)
	}
}
