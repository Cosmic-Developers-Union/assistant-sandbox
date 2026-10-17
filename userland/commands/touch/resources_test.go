package touch

import (
	"context"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type noTimestamps struct{ vmfs.System }

func TestTouchRequiresClockAndTimestampCapability(t *testing.T) {
	for _, mode := range []string{"proc", "clock", "timestamps", "cancel"} {
		result := commandtest.Run(t, func(ctx context.Context, args []string) error {
			p, _ := proc.FromContext(ctx)
			system, _ := p.Resources.Filesystem()
			descriptors, _ := p.Resources.Descriptors()
			clock, _ := p.Resources.Time()
			set := capability.Set{FS: system, FD: descriptors, Clock: clock}
			switch mode {
			case "proc":
				p = proc.Proc{}
			case "clock":
				set.Clock = nil
			case "timestamps":
				set.FS = noTimestamps{system}
			case "cancel":
				next, cancel := context.WithCancel(ctx)
				cancel()
				ctx = next
			}
			if mode != "proc" {
				p.Resources = set
			}
			return Run(proc.WithContext(ctx, p), args)
		}, t.TempDir(), []string{"new"}, "")
		if result.Code == 0 {
			t.Fatal(mode, result)
		}
	}
}
