package prcmd

import (
	"context"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	vmclock "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func bindDir(t *testing.T, dir string) {
	t.Helper()
	release := vfs.Bind(filesystem.NewFilesystem(dir), dir, nil)
	t.Cleanup(release)
}

var guestStamp = time.Date(2020, 1, 2, 3, 4, 0, 0, time.UTC)

var guestNow = time.Date(2020, 1, 2, 3, 4, 0, 0, time.UTC)

func guestClockContext(t *testing.T, wall time.Time) context.Context {
	t.Helper()
	virtual := &vmclock.Virtual{}
	if err := virtual.SetElapsedTo(uint64(wall.UnixNano())); err != nil {
		t.Fatal(err)
	}
	return proc.WithContext(t.Context(), proc.Proc{ID: 1, Resources: capability.Set{Clock: virtual}})
}
