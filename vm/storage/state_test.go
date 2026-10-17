package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type fakeState struct {
	quiet    error
	table    []byte
	encodeTo error
	restored []byte
	resetErr error
}

func (f *fakeState) QuietPoint() error { return f.quiet }

func (f *fakeState) ProcTable(context.Context) ([]byte, error) {
	if f.encodeTo != nil {
		return nil, f.encodeTo
	}
	return f.table, nil
}

func (f *fakeState) RestoreProcTable(_ context.Context, data []byte) error {
	f.restored = append([]byte(nil), data...)
	return f.resetErr
}

func TestCaptureStateWritesProcTable(t *testing.T) {
	ctx := context.Background()
	envelope := &Envelope{Format: EnvelopeFormat}
	state := &fakeState{table: []byte("table-bytes")}
	if err := envelope.CaptureState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if !envelope.HasProcTable() {
		t.Fatal("应写入进程表")
	}
	if string(envelope.ProcTable) != "table-bytes" {
		t.Fatalf("进程表=%q", envelope.ProcTable)
	}

	source, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	complete, err := Capture(ctx, Source{Filesystem: source, Language: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if err := complete.CaptureState(ctx, state); err != nil {
		t.Fatal(err)
	}
	encoded, err := complete.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.HasProcTable() {
		t.Fatal("往返后应保留进程表")
	}
}

func TestCaptureStateRejectsInvalidInputs(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label    string
		envelope *Envelope
		state    SnapshotState
		want     error
	}{
		{"nil 信封", nil, &fakeState{table: []byte("x")}, errno.Invalid},
		{"nil 状态源", &Envelope{Format: EnvelopeFormat}, nil, errno.Invalid},
		{"非安静点", &Envelope{Format: EnvelopeFormat}, &fakeState{quiet: errno.Busy}, errno.Busy},
		{"空进程表", &Envelope{Format: EnvelopeFormat}, &fakeState{}, errno.Invalid},
		{"编码失败", &Envelope{Format: EnvelopeFormat}, &fakeState{table: []byte("x"), encodeTo: errno.Limit}, errno.Limit},
	} {
		if err := tc.envelope.CaptureState(ctx, tc.state); !errors.Is(err, tc.want) {
			t.Errorf("%s: err=%v want %v", tc.label, err, tc.want)
		}
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := (&Envelope{Format: EnvelopeFormat}).CaptureState(cancelled, &fakeState{table: []byte("x")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestRestoreStateAppliesProcTable(t *testing.T) {
	ctx := context.Background()
	envelope := &Envelope{Format: EnvelopeFormat, ProcTable: []byte("restored")}
	state := &fakeState{}
	if err := envelope.RestoreState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if string(state.restored) != "restored" {
		t.Fatalf("restored=%q", state.restored)
	}

	busy := &fakeState{quiet: errno.Busy}
	if err := envelope.RestoreState(ctx, busy); !errors.Is(err, errno.Busy) {
		t.Fatalf("非安静点应报 EBUSY: %v", err)
	}
	if busy.restored != nil {
		t.Fatal("拒绝时不得改动进程表")
	}

	failing := &fakeState{resetErr: errno.Limit}
	if err := envelope.RestoreState(ctx, failing); !errors.Is(err, errno.Limit) {
		t.Fatalf("写回失败应上报: %v", err)
	}
}

func TestRestoreStateRejectsInvalidEnvelopes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label    string
		envelope *Envelope
		state    SnapshotState
		want     error
	}{
		{"nil 信封", nil, &fakeState{}, errno.Invalid},
		{"nil 状态", &Envelope{Format: EnvelopeFormat, ProcTable: []byte("x")}, nil, errno.Invalid},
		{"缺进程表", &Envelope{Format: EnvelopeFormat}, &fakeState{}, errno.Invalid},
		{"格式不符", &Envelope{Format: "other", ProcTable: []byte("x")}, &fakeState{}, errno.Version},
	} {
		if err := tc.envelope.RestoreState(ctx, tc.state); !errors.Is(err, tc.want) {
			t.Errorf("%s: err=%v want %v", tc.label, err, tc.want)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := (&Envelope{Format: EnvelopeFormat, ProcTable: []byte("x")}).RestoreState(cancelled, &fakeState{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}

	var nilEnvelope *Envelope
	if nilEnvelope.HasProcTable() {
		t.Fatal("nil 信封不应报告有进程表")
	}
}
