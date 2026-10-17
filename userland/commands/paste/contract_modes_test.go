package pastecmd

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestDelimiterListAndZero(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"one": "a\nb\nc\n", "two": "1\n2\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name  string
		args  []string
		input string
		want  string
	}{
		{"cycle", []string{"-d", ":,-", "one", "two"}, "", "a:1\nb:2\nc:\n"},

		{"backslash-n", []string{"-d", `\n`, "one", "two"}, "", "a\n1\nb\n2\nc\n\n"},
		{"backslash-escape", []string{"-d", `\\`, "one", "two"}, "", "a\\1\nb\\2\nc\\\n"},
		{"unknown-escape", []string{"-d", `\q`, "one", "two"}, "", "aq1\nbq2\ncq\n"},

		{"serial-zero", []string{"-s", "-z", "one"}, "", "a\nb\nc\n\x00"},
		{"serial-multi", []string{"-s", "one", "two"}, "", "a\tb\tc\n1\t2\n"},
		{"three-files", []string{"one", "two", "one"}, "", "a\t1\ta\nb\t2\tb\nc\t\tc\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("%s: 得到 %+v，want %q", test.name, result, test.want)
		}
	}
	if result := commandtest.Run(t, Run, dir, []string{"--help"}, ""); result.Code != 0 {
		t.Fatalf("--help 得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-s", "-d", ":,-", "one"}, ""); result.Code != 0 || result.Stdout != "a:b,c\n" {
		t.Fatalf("串行分隔符循环: %+v", result)
	}
}

func TestZeroTerminatedAndFaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nul1"), []byte("a\x00b\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nul2"), []byte("1\x002\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"-z", "nul1", "nul2"}, "a\t1\x00b\t2\x00"},
		{[]string{"-s", "-z", "nul1"}, "a\tb\x00"},
		{[]string{"-z", "-d", ":", "nul1", "nul2"}, "a:1\x00b:2\x00"},
		{[]string{"-d", `\t`, "nul1", "nul2"}, "a\x00b\x00\t1\x002\x00\n"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want %q", test.args, result, test.want)
		}
	}
	for _, operation := range []string{"Read", "OpenRead", "MissingFS"} {
		result := commandtest.RunFault(t, Run, dir, []string{"nul1"}, "", operation)
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("%s 得到 %+v", operation, result)
		}
	}
}
