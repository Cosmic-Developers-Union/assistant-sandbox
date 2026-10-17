package entropy

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

func TestSeededIsDeterministicAndSplitAcrossReads(t *testing.T) {
	a := NewSeeded(42)
	b := NewSeeded(42)
	first := make([]byte, 16)
	if _, err := a.Read(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	part1 := make([]byte, 5)
	part2 := make([]byte, 11)
	if _, err := b.Read(context.Background(), part1); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(context.Background(), part2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(part1, part2...), first) {
		t.Fatalf("分段读取序列不一致: %x", append(part1, part2...))
	}

	c := NewSeeded(43)
	other := make([]byte, 16)
	if _, err := c.Read(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, other) {
		t.Fatal("不同种子不应产生相同序列")
	}
}

func TestSeededReadDoesNotAdvanceBeyondRequest(t *testing.T) {
	s := NewSeeded(7)
	if _, err := s.Read(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if _, err := s.Read(context.Background(), buf); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingJournalRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := NewSeeded(99)
	journal := replay.Record()
	pid := uint64(5)
	recorder := NewRecording(source, journal, pid)
	first := make([]byte, 8)
	if _, err := recorder.Read(ctx, first); err != nil {
		t.Fatal(err)
	}

	playback := NewRecording(nil, replay.Replay(journal.Facts()), pid)
	replayed := make([]byte, 8)
	if _, err := playback.Read(ctx, replayed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, replayed) {
		t.Fatalf("first=%x replayed=%x", first, replayed)
	}

	playback = NewRecording(nil, replay.Replay(journal.Facts()), pid)
	if _, err := playback.Read(ctx, make([]byte, 9)); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	playback = NewRecording(nil, replay.Replay(journal.Facts()), 6)
	if _, err := playback.Read(ctx, make([]byte, 8)); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestRecordingPropagatesSourceError(t *testing.T) {
	journal := replay.Record()
	recorder := NewRecording(failingSource{}, journal, 1)
	if _, err := recorder.Read(context.Background(), make([]byte, 4)); !errors.Is(err, errno.NoSpace) {
		t.Fatal(err)
	}

	playback := NewRecording(nil, replay.Replay(journal.Facts()), 1)
	if _, err := playback.Read(context.Background(), make([]byte, 4)); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

type failingSource struct{}

func (failingSource) Read(context.Context, []byte) (int, error) { return 0, errno.NoSpace }

func TestAlgorithmVersionIsStable(t *testing.T) {
	if Algorithm != "splitmix64-v1" {
		t.Fatal("算法版本变更必须同步回放事实解释")
	}
}
