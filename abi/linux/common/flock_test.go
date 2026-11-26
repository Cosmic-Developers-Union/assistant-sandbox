package common

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

func TestProcessDescriptorsFlock(t *testing.T) {
	table := fd.New()
	table.Attach(1, fd.Streams{})
	descriptors := ProcessDescriptors{Table: table, PID: 1}
	if err := descriptors.Flock(context.Background(), 0, 2); err != nil {
		t.Fatal(err)
	}
	if err := descriptors.Flock(context.Background(), 0, 8); err != nil {
		t.Fatal(err)
	}
}
