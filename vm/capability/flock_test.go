package capability

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

func TestScopedFlockForwardsPID(t *testing.T) {
	table := fd.New()
	table.Attach(7, fd.Streams{})
	descriptors := scopedFDs{base: table, pid: 7}
	if err := descriptors.Flock(context.Background(), 9, 0, 2); err != nil {
		t.Fatal(err)
	}
	if err := descriptors.Flock(context.Background(), 9, 0, 8); err != nil {
		t.Fatal(err)
	}
}
