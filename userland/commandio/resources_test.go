package commandio

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func TestMissingAndClosedDescriptorCapabilitiesFailExplicitly(t *testing.T) {
	table := fd.New()
	table.Attach(2, fd.Streams{Input: bytes.NewBufferString("value"), Output: new(bytes.Buffer), Error: new(bytes.Buffer)})
	ctx := proc.WithContext(t.Context(), proc.Proc{ID: 2, Resources: capability.Set{FD: table}})

	runner, _ := interp.New(interp.Dir(t.TempDir()), interp.ExecHandlers(func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(hctx context.Context, _ []string) error {
			if _, err := Filesystem(hctx); !errors.Is(err, errno.NotImplemented) {
				t.Fatal(err)
			}
			if _, err := Open(hctx, "x", 0, 0); !errors.Is(err, errno.NotImplemented) {
				t.Fatal(err)
			}
			for _, id := range []uint32{0, 1, 2} {
				if err := table.Close(t.Context(), 2, id); err != nil {
					t.Error(err)
				}
			}
			h := Context(hctx)
			if _, err := h.Stdin.Read(make([]byte, 1)); !errors.Is(err, errno.BadHandle) {
				t.Fatal(err)
			}
			if _, err := h.Stdout.Write([]byte("x")); !errors.Is(err, errno.BadHandle) {
				t.Fatal(err)
			}
			if _, err := h.Stderr.Write([]byte("x")); !errors.Is(err, errno.BadHandle) {
				t.Fatal(err)
			}
			noResources := proc.WithContext(hctx, proc.Proc{Resources: capability.Set{}})
			h = Context(noResources)
			if _, err := h.Stdout.Write(nil); !errors.Is(err, errno.NotImplemented) {
				t.Fatal(err)
			}
			missing := proc.WithContext(hctx, proc.Proc{})
			h = Context(missing)
			if _, err := h.Stdin.Read(nil); !errors.Is(err, errno.NotImplemented) {
				t.Fatal(err)
			}
			if _, err := Filesystem(missing); !errors.Is(err, errno.NotImplemented) {
				t.Fatal(err)
			}
			return nil
		}
	}))
	tree, _ := syntax.NewParser().Parse(strings.NewReader("under_test"), "")
	if err := runner.Run(ctx, tree); err != nil {
		t.Fatal(err)
	}
}

func TestWalkOrderCancellationFailureAndDepthBudget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "child"), nil, 0600); err != nil {
		t.Error(err)
	}
	ctx := proc.WithContext(t.Context(), proc.Proc{Resources: capability.Set{FS: filesystem.NewFilesystem(dir)}})
	var seen []string
	visitor := func(name string, _ fs.DirEntry) error { seen = append(seen, name); return nil }
	if err := Walk(ctx, dir, visitor); err != nil || len(seen) != 3 {
		t.Fatal(seen, err)
	}
	sentinel := errors.New("拒绝节点")
	if err := Walk(ctx, dir, func(string, fs.DirEntry) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := Walk(ctx, filepath.Join(dir, "missing"), visitor); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := Walk(cancelCtx, dir, visitor); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := Walk(t.Context(), dir, visitor); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(dir, "cycle")); err != nil {
		t.Fatal(err)
	}
	if err := Walk(ctx, dir, visitor); err == nil {
		t.Fatal("目录循环未受预算限制")
	}
}
