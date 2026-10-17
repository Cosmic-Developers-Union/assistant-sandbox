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

func runScript(t *testing.T, dir, script string) (string, string, int) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	err := run(t.Context(), script, dir, nil, strings.NewReader(""), &out, &diagnostic)
	code := 0
	if err != nil {
		status, ok := errors.AsType[interp.ExitStatus](err)
		if !ok {
			t.Fatalf("%s: %v", script, err)
		}
		code = int(status)
	}
	return out.String(), diagnostic.String(), code
}

func TestUmaskReportsAndSets(t *testing.T) {
	dir := t.TempDir()

	if out, diag, code := runScript(t, dir, "umask"); code != 0 || out != "0022\n" {
		t.Fatalf("初值 = (%q, %q, %d)", out, diag, code)
	}
	if out, _, code := runScript(t, dir, "umask -S"); code != 0 || out != "u=rwx,g=rx,o=rx\n" {
		t.Fatalf("-S = (%q, %d)", out, code)
	}

	out, diag, code := runScript(t, dir, "umask 077; touch made; umask")
	if code != 0 || out != "0077\n" {
		t.Fatalf("设置后 = (%q, %q, %d)", out, diag, code)
	}
	info, err := os.Stat(filepath.Join(dir, "made"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("创建模式 = %o，期望 0600（掩码未生效？）", info.Mode().Perm())
	}

	if _, _, code := runScript(t, dir, "umask 027; mkdir sub; umask 022"); code != 0 {
		t.Fatal(code)
	}
	info, err = os.Stat(filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("目录模式 = %o，期望 0750", info.Mode().Perm())
	}
}

func TestUmaskInheritanceAndIsolation(t *testing.T) {
	dir := t.TempDir()
	out, diag, code := runScript(t, dir, "umask 077; sh -c 'umask'")
	if code != 0 || out != "0077\n" {
		t.Fatalf("子进程应继承掩码: (%q, %q, %d)", out, diag, code)
	}
	out, diag, code = runScript(t, dir, "umask 077; sh -c 'umask 000'; umask")
	if code != 0 || out != "0077\n" {
		t.Fatalf("子进程改掩码不应影响父 shell: (%q, %q, %d)", out, diag, code)
	}
}

func TestUmaskRejectsBadOperands(t *testing.T) {
	dir := t.TempDir()

	for _, script := range []string{"umask abc", "umask 999", "umask 07777", "umask --bad", "umask 022 077"} {
		out, _, code := runScript(t, dir, script)
		if code == 0 || out != "" {
			t.Fatalf("%s 应失败: (%q, %d)", script, out, code)
		}
	}

	if out, _, code := runScript(t, dir, "umask abc; umask"); out != "0022\n" || code != 0 {
		t.Fatalf("失败后掩码应保持且会话继续: (%q, %d)", out, code)
	}

	if out, _, code := runScript(t, dir, "umask 0777; umask"); code != 0 || out != "0777\n" {
		t.Fatalf("0777 应被接受: (%q, %d)", out, code)
	}
}
