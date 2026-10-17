package readlink

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type nonLinkFS struct{ vmfs.System }

func (f nonLinkFS) Readlink(ctx context.Context, name string) (string, error) {
	if strings.HasSuffix(name, "plain") {
		return "", errno.Wrap(errno.Invalid, "readlinkat plain")
	}
	return f.System.Readlink(ctx, name)
}

func TestNonLinkOperand(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err := commandtest.Invoke(t.Context(), func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		system, err := p.Resources.Filesystem()
		if err != nil {
			return err
		}
		clock, _ := p.Resources.Time()
		descriptors, _ := p.Resources.Descriptors()
		random, _ := p.Resources.Random()
		p.Resources = capability.Set{FS: nonLinkFS{system}, FD: descriptors, Entropy: random, Clock: clock}
		return Run(proc.WithContext(ctx, p), args)
	}, dir, []string{"link", "plain"}, strings.NewReader(""), &out, io.Discard)
	if err == nil {
		t.Fatal("不是符号链接应返回非零退出")
	}
	if out.String() != "target\n" {
		t.Fatalf("可用操作数仍应输出: %q", out.String())
	}
}

func TestDownstreamFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	err := commandtest.Invoke(t.Context(), Run, dir, []string{"link"}, strings.NewReader(""), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
