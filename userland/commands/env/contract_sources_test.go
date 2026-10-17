package env

import (
	"context"
	"io"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestInheritedEnvironment(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	err := commandtest.Invoke(t.Context(), func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		p.Env = map[string]string{"INHERITED": "yes"}
		return Run(proc.WithContext(ctx, p), args)
	}, dir, []string{"EXTRA=1"}, strings.NewReader(""), &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "EXTRA=1\nINHERITED=yes\n" {
		t.Fatalf("得到 %q", out.String())
	}

	out.Reset()
	err = commandtest.Invoke(t.Context(), func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		p.Env = map[string]string{"INHERITED": "yes"}
		return Run(proc.WithContext(ctx, p), args)
	}, dir, []string{"-i"}, strings.NewReader(""), &out, io.Discard)
	if err != nil || out.String() != "" {
		t.Fatalf("-i 得到 %q err=%v", out.String(), err)
	}
}

func TestDownstreamFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"FOO=bar"}, strings.NewReader(""), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
