package sort

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func runSort(t *testing.T, args []string, input string) commandtest.Result {
	t.Helper()
	return commandtest.Run(t, Run, t.TempDir(), args, input)
}

func TestKeyExtendsToEndOfLine(t *testing.T) {
	input := "a c b\nd a c\n"
	result := runSort(t, []string{"-k2"}, input)
	if result.Code != 0 || result.Stdout != "d a c\na c b\n" {
		t.Fatalf("-k2 得到 %+v", result)
	}

	whole := runSort(t, nil, input)
	if plain := runSort(t, []string{"-k1"}, input); plain.Stdout != whole.Stdout {
		t.Fatalf("-k1 应与不带 -k 等价: %q vs %q", plain.Stdout, whole.Stdout)
	}

	result = runSort(t, []string{"-k2,2"}, "a c b\nd a c\n")
	if result.Stdout != "d a c\na c b\n" {
		t.Fatalf("-k2,2 得到 %q", result.Stdout)
	}
}

func TestKeyOffsetsAndModifiers(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
	}{
		{"字符偏移", []string{"-k1.3"}, "abY\nabX\n", "abX\nabY\n"},
		{"键修饰 n（数字）", []string{"-k1n"}, "10\n9\n", "9\n10\n"},
		{"键修饰 r（逆序）", []string{"-k1r"}, "a\nb\n", "b\na\n"},
		{"键修饰 b（忽略前导空白）", []string{"-k1b"}, "  b\n a\n", " a\n  b\n"},
		{"数字与字符偏移并存", []string{"-k1.1n"}, "10\n9\n", "9\n10\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSort(t, test.args, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%v 得到 code=%d stdout=%q stderr=%q", test.args, result.Code, result.Stdout, result.Stderr)
			}
		})
	}

	for _, args := range [][]string{{"-k1.x"}, {"-k1.0"}, {"-k0"}, {"-k2,1"}, {"-k1q"}} {
		if result := runSort(t, args, "a\n"); result.Code != 2 {
			t.Fatalf("%v 应当失败: %+v", args, result)
		}
	}
}

func TestComparisonFlags(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
	}{
		{"-b 忽略前导空白", []string{"-b"}, "  b\n a\n", " a\n  b\n"},
		{"-f 折叠大小写", []string{"-f"}, "B\na\n", "a\nB\n"},
		{"-d 只比较字母数字与空白", []string{"-d"}, "a-b\nab\n", "a-b\nab\n"},

		{"-i 忽略不可打印", []string{"-i"}, "a\x01b\nab\n", "a\x01b\nab\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runSort(t, test.args, test.input)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%v 得到 %q", test.args, result.Stdout)
			}
		})
	}
}

func TestOutputFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(target, []byte("b\na\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-o", "out.txt", "in.txt"}, "")
	if result.Code != 0 || result.Stdout != "" {
		t.Fatalf("-o 得到 %+v", result)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "out.txt")); err != nil || string(data) != "a\nb\n" {
		t.Fatalf("out.txt = %q, %v", data, err)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-o", "in.txt", "in.txt"}, ""); result.Code != 0 {
		t.Fatalf("就地 -o 得到 %+v", result)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "a\nb\n" {
		t.Fatalf("in.txt = %q, %v", data, err)
	}

	if result := commandtest.Run(t, Run, dir, []string{"-o", "absent-dir/x.txt", "in.txt"}, ""); result.Code != 2 || result.Stderr == "" {
		t.Fatalf("不可写目标得到 %+v", result)
	}
}

func TestZeroTerminated(t *testing.T) {
	result := runSort(t, []string{"-z"}, "b\x00a\x00")
	if result.Code != 0 || result.Stdout != "a\x00b\x00" {
		t.Fatalf("-z 得到 %q", result.Stdout)
	}

	merged := runSort(t, []string{"-m"}, "b\na\n")
	if merged.Code != 0 || merged.Stdout != "a\nb\n" {
		t.Fatalf("-m 得到 %+v", merged)
	}
}

func TestReverseAppliesToWholeLineFallback(t *testing.T) {
	result := runSort(t, []string{"-r", "-k2,2"}, "x b\ny b\n")
	if result.Code != 0 || result.Stdout != "y b\nx b\n" {
		t.Fatalf("-r 下整行比较也应反转: %q", result.Stdout)
	}
}

func TestCheckMode(t *testing.T) {
	if result := runSort(t, []string{"-c"}, "a\nb\nc\n"); result.Code != 0 || result.Stdout != "" {
		t.Fatalf("有序输入得到 %+v", result)
	}
	result := runSort(t, []string{"-c"}, "a\nc\nb\n")
	if result.Code != 1 || result.Stderr == "" || result.Stdout != "" {
		t.Fatalf("无序输入得到 %+v", result)
	}
	if result := runSort(t, []string{"-cu"}, "a\na\n"); result.Code != 1 {
		t.Fatalf("-cu 重复行应报乱序: %+v", result)
	}
	if result := runSort(t, []string{"-cu"}, "a\nb\n"); result.Code != 0 {
		t.Fatalf("-cu 有序且无重复: %+v", result)
	}

	if result := runSort(t, []string{"-cn"}, "9\n10\n"); result.Code != 0 {
		t.Fatalf("-cn 数字序: %+v", result)
	}
	if result := runSort(t, []string{"-cn"}, "10\n9\n"); result.Code != 1 {
		t.Fatalf("-cn 逆序应报错: %+v", result)
	}
}
