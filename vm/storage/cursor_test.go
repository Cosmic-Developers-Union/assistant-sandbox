package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

func realJournal(t *testing.T, count int) *replay.Memory {
	t.Helper()
	journal := replay.Record()
	for index := 0; index < count; index++ {
		if err := journal.Record(context.Background(), replay.Fact{
			Sequence:  uint64(index + 1),
			PID:       1,
			Operation: replay.OpStdin,
			Input:     []byte{byte(index)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	return replay.Replay(journal.Facts())
}

func TestReconcileCursorAcceptsAlignedStream(t *testing.T) {
	ctx := context.Background()
	journal := realJournal(t, 3)
	envelope := &Envelope{Format: EnvelopeFormat, CursorPos: 3}
	if err := envelope.ReconcileCursor(ctx, journal); err != nil {
		t.Fatalf("对齐应通过对账: %v", err)
	}

	envelope = &Envelope{Format: EnvelopeFormat, CursorPos: 2}
	if err := envelope.ReconcileCursor(ctx, journal); err != nil {
		t.Fatalf("位置落后应通过对账: %v", err)
	}
}

func TestReconcileCursorRejectsTruncatedJournal(t *testing.T) {
	envelope := &Envelope{Format: EnvelopeFormat, CursorPos: 5}
	journal := realJournal(t, 3)
	if err := envelope.ReconcileCursor(context.Background(), journal); !errors.Is(err, errno.Version) {
		t.Fatalf("游标超出范围应报 EPROTO: %v", err)
	}
}

func TestReconcileCursorRejectsAdvancedJournal(t *testing.T) {
	ctx := context.Background()
	recorded := replay.Record()
	for index := 0; index < 3; index++ {
		if err := recorded.Record(ctx, replay.Fact{
			Sequence:  uint64(index + 1),
			PID:       1,
			Operation: replay.OpStdin,
			Input:     []byte{byte(index)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	facts := recorded.Facts()
	journal := replay.Replay(facts)

	for index := 0; index < 2; index++ {
		if _, err := journal.Next(ctx, replay.Fact{
			Sequence:  uint64(index + 1),
			PID:       1,
			Operation: replay.OpStdin,
			Input:     []byte{byte(index)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	envelope := &Envelope{Format: EnvelopeFormat, CursorPos: 1}
	if err := envelope.ReconcileCursor(ctx, journal); !errors.Is(err, errno.Version) {
		t.Fatalf("位置领先应报 EPROTO: %v", err)
	}
}

func TestReconcileCursorRequiresReplayMode(t *testing.T) {
	envelope := &Envelope{Format: EnvelopeFormat, CursorPos: 0}
	if err := envelope.ReconcileCursor(context.Background(), replay.Record()); !errors.Is(err, errno.Invalid) {
		t.Fatalf("记录模式应对账失败: %v", err)
	}
}

func TestReconcileCursorSkipsWithoutJournalAndChecksInputs(t *testing.T) {
	ctx := context.Background()
	envelope := &Envelope{Format: EnvelopeFormat}
	if err := envelope.ReconcileCursor(ctx, nil); err != nil {
		t.Fatalf("无事实流应跳过对账: %v", err)
	}
	var nilEnvelope *Envelope
	if err := nilEnvelope.ReconcileCursor(ctx, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 信封应报 EINVAL: %v", err)
	}

	badFormat := &Envelope{Format: "other"}
	if err := badFormat.ReconcileCursor(ctx, nil); err != nil {
		t.Fatalf("nil 事实流应跳过格式检查: %v", err)
	}

	journal := realJournal(t, 1)
	if err := badFormat.ReconcileCursor(ctx, journal); !errors.Is(err, errno.Version) {
		t.Fatalf("格式不符应报 EPROTO: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := envelope.ReconcileCursor(cancelled, journal); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}
