package bash_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/guest"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/shell"
	"mvdan.cc/sh/v3/interp"
)

func TestBashChildModesAndStateIsolation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.sh"), []byte(`printf '%s:%s' "$MESSAGE" "$1"`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ source, input, want string }{
		{`export MESSAGE=正文; LOCAL=隐藏; bash -c 'printf "%s:%s:%s" "$MESSAGE" "$1" "${LOCAL:-无}"; MESSAGE=子' 名称 参数; printf ':%s' "$MESSAGE"`, "", "正文:参数:无:正文"},
		{`export MESSAGE=正文; bash demo.sh 参数`, "", "正文:参数"},
		{`bash -s 参数`, `printf '%s' "$1"`, "参数"},
		{`bash -c 'exit 7'; printf '%s' "$?"`, "", "7"},
		{`bash -c 'printf "%s:%s" "$0" "$1"' 名称 参数`, "", "名称:参数"},
		{`bash -e -c 'false; echo 错误'; printf '%s' "$?"`, "", "1"},
		{`bash -c 'printf 里; bash -c "printf 层"'`, "", "里层"},
	} {

		var out, errout bytes.Buffer
		err := run(t.Context(), test.source, dir, nil, strings.NewReader(test.input), &out, &errout)
		if err != nil || out.String() != test.want {
			t.Fatalf("%s: out=%q error=%v stderr=%s", test.source, out.String(), err, errout.String())
		}
	}
}
func TestBashFailuresAndHelp(t *testing.T) {
	for _, source := range []string{`bash --unknown`, `bash -c 'echo 不应执行 &'`, `bash absent.sh`, `bash -c 'if'`, `bash -c`, `bash -u -c 'echo "$UNSET"'`, `bash -c 'bash -c "$(printf x)"'`} {
		var out, diagnostic bytes.Buffer
		err := run(t.Context(), source, t.TempDir(), nil, strings.NewReader(""), &out, &diagnostic)
		if _, ok := errors.AsType[interp.ExitStatus](err); !ok {
			t.Fatal(source, err, diagnostic.String())
		}
	}
	var out, diagnostic bytes.Buffer
	if err := run(t.Context(), `bash --help`, t.TempDir(), nil, strings.NewReader(""), &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostic.String(), "command") {
		t.Fatal(diagnostic.String())
	}
	if err := run(t.Context(), `bash -s`, t.TempDir(), nil, strings.NewReader(strings.Repeat("x", (64<<10)+1)), &out, &diagnostic); err == nil {
		t.Fatal("未拒绝超限脚本")
	}
	if err := run(t.Context(), `export LOOP='bash -c "$LOOP"'; bash -c "$LOOP"`, t.TempDir(), nil, strings.NewReader(""), &out, &diagnostic); err == nil || !strings.Contains(diagnostic.String(), "嵌套超过16层") {
		t.Fatal(err, diagnostic.String())
	}
}

func run(ctx context.Context, source, dir string, env []string, in io.Reader, out, diagnostic io.Writer) error {
	k, p, err := guest.New(dir, env, in, out, diagnostic)
	if err != nil {
		return err
	}
	return shell.Run(ctx, k, p, source, dir, env, in, out, diagnostic)
}
