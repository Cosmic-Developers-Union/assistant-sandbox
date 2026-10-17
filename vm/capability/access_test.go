package capability

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestMissingAndTypedNilResources(t *testing.T) {
	var set Set
	if _, err := set.Filesystem(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Filesystem", err)
	}
	if _, err := set.Time(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Time", err)
	}
	if _, err := set.Random(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Random", err)
	}
	if _, err := set.Credentials(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Credentials", err)
	}
	if _, err := set.Descriptors(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Descriptors", err)
	}
	if _, err := set.Meter(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Meter", err)
	}
	if _, err := set.Host(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Host", err)
	}
	if _, err := set.InitialImage(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("InitialImage", err)
	}
	if _, err := set.Events(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Events", err)
	}
	if _, err := set.Languages(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Languages", err)
	}
	if _, err := set.SessionStore(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("SessionStore", err)
	}
	if _, err := set.SnapshotStore(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("SnapshotStore", err)
	}
	if _, err := set.Journal(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Journal", err)
	}
	if _, err := set.Network(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Network", err)
	}
	if _, err := set.Communication(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal("Communication", err)
	}

	var missing *fixedClock
	set.Clock = missing
	if _, err := set.Time(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	actual := &fixedClock{}
	set.Clock = actual
	got, err := set.Time()
	if err != nil || got != actual {
		t.Fatal(got, err)
	}
	if value, err := Require(42); value != 42 || err != nil {
		t.Fatal(value, err)
	}
	var nilMap map[string]int
	if _, err := Require(nilMap); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	_ = clock.Source(actual)
}

type fixedClock struct{}

func (*fixedClock) Wall(context.Context) (int64, error)       { return 1, nil }
func (*fixedClock) Monotonic(context.Context) (uint64, error) { return 1, nil }
func (*fixedClock) Sleep(context.Context, uint64) error       { return nil }
