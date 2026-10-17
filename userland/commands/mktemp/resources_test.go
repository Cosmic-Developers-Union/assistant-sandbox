package mktemp

import (
	"context"
	"io"
	"io/fs"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type failedRandom struct{ short bool }

func (f failedRandom) Read(context.Context, []byte) (int, error) {
	if f.short {
		return 1, nil
	}
	return 0, io.ErrUnexpectedEOF
}

type collisionFS struct {
	vmfs.System
	calls *int
}

func (f collisionFS) Open(context.Context, string, uint32, fs.FileMode) (vmfs.File, error) {
	*f.calls++
	return nil, fs.ErrExist
}

func TestRandomErrorsShortReadsAndCollisionBudget(t *testing.T) {
	for _, source := range []entropy.Source{nil, failedRandom{}, failedRandom{short: true}, &entropy.Counter{}} {
		calls := 0
		run := func(ctx context.Context, args []string) error {
			p, _ := proc.FromContext(ctx)
			system, _ := p.Resources.Filesystem()
			descriptors, _ := p.Resources.Descriptors()
			p.Resources = capability.Set{FS: collisionFS{system, &calls}, FD: descriptors, Entropy: source}
			return Run(proc.WithContext(ctx, p), args)
		}
		result := commandtest.Run(t, run, t.TempDir(), nil, "")
		if result.Code == 0 {
			t.Fatal(result)
		}
		if source != nil {
			if _, ok := source.(*entropy.Counter); ok && calls != 64 {
				t.Fatal("碰撞重试预算", calls)
			}
		}
	}
	result := commandtest.Run(t, func(ctx context.Context, args []string) error { return Run(proc.WithContext(ctx, proc.Proc{}), args) }, t.TempDir(), nil, "")
	if result.Code == 0 {
		t.Fatal("脱离 VM 的 mktemp 成功")
	}
}
