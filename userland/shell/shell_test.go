package shell

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/interp"
)

func TestBashAndVMCommands(t *testing.T) {
	dir := t.TempDir()
	script := `set -e
values=(first second)
mkdir -p sub
printf '%s\n' "${values[@]}" > sub/value
cp sub/value sub/copy
mv sub/copy sub/moved
cat sub/moved | base64 > encoded
base64 -d encoded
ls sub
find sub -name moved
touch touched
shasum sub/moved
mktemp
rm sub/moved
[[ ! -f sub/moved ]]`
	var out, diagnostic bytes.Buffer
	if err := run(t.Context(), script, dir, []string{"TMPDIR=" + dir}, strings.NewReader(""), &out, &diagnostic); err != nil {
		t.Fatal(err, diagnostic.String())
	}
	if !strings.Contains(out.String(), "first\nsecond\n") || !strings.Contains(out.String(), "moved") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "value")); err != nil {
		t.Fatal(err)
	}
}
func TestShellFailuresAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		script string
		code   interp.ExitStatus
	}{{"unregistered", 127}, {"@wasi", 127}, {"vm-exec", 127}, {"cat missing", 2}, {"exit 7", 7}} {
		var diagnostic bytes.Buffer
		err := run(t.Context(), tc.script, t.TempDir(), nil, strings.NewReader(""), &diagnostic, &diagnostic)
		status, ok := errors.AsType[interp.ExitStatus](err)
		if !ok || status != tc.code {
			t.Fatal(tc, err)
		}
	}
	if err := run(t.Context(), "if", t.TempDir(), nil, nil, new(bytes.Buffer), new(bytes.Buffer)); err == nil {
		t.Fatal("非法 Bash 语法")
	}
	if err := run(t.Context(), "true", "/missing-guest-directory", nil, nil, new(bytes.Buffer), new(bytes.Buffer)); err == nil {
		t.Fatal("不存在的工作区")
	}
	var out bytes.Buffer
	if err := run(t.Context(), `VALUE=explicit; export VALUE; printf '%s' "$VALUE"`, t.TempDir(), nil, nil, &out, &out); err != nil || out.String() != "explicit" {
		t.Fatal(out.String(), err)
	}
}

func TestLoopRetainsVariablesFunctionsAndDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Error(err)
	}
	input := `{"ID":1,"Command":"cd sub; VALUE=remembered; answer() { printf '%s' \"$VALUE\"; }","OutputLimit":1024}
{"ID":2,"Command":"answer; pwd","OutputLimit":1024}
{"ID":3,"Command":"test false","OutputLimit":1024}
{"ID":4,"Command":"printf continued","OutputLimit":1024}
`
	var out bytes.Buffer
	if err := loop(t.Context(), strings.NewReader(input), &out, dir, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "remembered") || !strings.Contains(out.String(), "/sub") || !strings.Contains(out.String(), "continued") {
		t.Fatal(out.String())
	}
}
