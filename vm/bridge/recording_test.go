package bridge

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

type fakeBridge struct {
	calls    int
	response []byte
	err      error
}

func (f *fakeBridge) Call(context.Context, uint64, string, []byte) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

func TestRecordingRoundTripWithoutHostCalls(t *testing.T) {
	ctx := context.Background()
	base := &fakeBridge{response: []byte("ok")}
	journal := replay.Record()
	recorder := NewRecording(base, journal)
	out, err := recorder.Call(ctx, 4, "exec", []byte("payload"))
	if err != nil || string(out) != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := recorder.Call(ctx, 4, "exec", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if base.calls != 2 {
		t.Fatalf("calls=%d", base.calls)
	}

	playback := NewRecording(&fakeBridge{response: []byte("WRONG")}, replay.Replay(journal.Facts()))
	out, err = playback.Call(ctx, 4, "exec", []byte("payload"))
	if err != nil || string(out) != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}

	_, err = playback.Call(ctx, 4, "exec", []byte("different"))
	var mismatch *replay.MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err=%v", err)
	}
}

func TestNilJournalPassesThroughAndNilBridgeFails(t *testing.T) {
	ctx := context.Background()
	base := &fakeBridge{response: []byte("direct")}
	recorder := NewRecording(base, nil)
	out, err := recorder.Call(ctx, 1, "exec", nil)
	if err != nil || string(out) != "direct" || base.calls != 1 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, base.calls)
	}
	if _, err := NewRecording(nil, nil).Call(ctx, 1, "exec", nil); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
}

func TestRecordedErrorsReplay(t *testing.T) {
	ctx := context.Background()
	base := &fakeBridge{err: errno.Limit}
	journal := replay.Record()
	recorder := NewRecording(base, journal)
	if _, err := recorder.Call(ctx, 2, "exec", nil); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	playback := NewRecording(nil, replay.Replay(journal.Facts()))
	if _, err := playback.Call(ctx, 2, "exec", nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}
