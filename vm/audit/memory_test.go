package audit

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestStrictSequenceAndPayloadCloning(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	payload := []byte("v1")
	if err := m.Append(ctx, Event{PID: 2, Kind: "spawn", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	payload[0] = 'X'
	if err := m.Append(ctx, Event{PID: 2, Kind: "exit", Payload: []byte("v2")}); err != nil {
		t.Fatal(err)
	}
	events := m.Events()
	if len(events) != 2 {
		t.Fatal(events)
	}
	if events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("序号不严格: %d,%d", events[0].Sequence, events[1].Sequence)
	}
	if string(events[0].Payload) != "v1" {
		t.Fatalf("负载被共享: %q", events[0].Payload)
	}
}

func TestCapacityFailure(t *testing.T) {
	m := NewMemory()
	m.limit = 1
	if err := m.Append(context.Background(), Event{Kind: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Append(context.Background(), Event{Kind: "b"}); !errors.Is(err, errno.NoSpace) {
		t.Fatal(err)
	}
}

func TestInjectedFailureSurfaces(t *testing.T) {
	m := NewMemory()
	boom := errors.New("存储失败")
	m.FailNext(boom)
	if err := m.Append(context.Background(), Event{}); !errors.Is(err, boom) {
		t.Fatal("注入的写入失败必须显式返回")
	}

	if err := m.Append(context.Background(), Event{}); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionGapMarking(t *testing.T) {
	m := NewMemory()
	sub := m.Subscribe()

	for i := range defaultQueueLimit + 2 {
		if err := m.Append(context.Background(), Event{Kind: "e", Payload: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	events, gap := sub.Drain()
	if gap != 2 {
		t.Fatalf("gap=%d", gap)
	}
	if len(events) != defaultQueueLimit {
		t.Fatalf("events=%d", len(events))
	}

	if _, gap := sub.Drain(); gap != 0 {
		t.Fatalf("gap=%d", gap)
	}
	sub.Close()

	if err := m.Append(context.Background(), Event{}); err != nil {
		t.Fatal(err)
	}
	if events, _ := sub.Drain(); len(events) != 0 {
		t.Fatal("撤销后不应再收到事件")
	}
}

func TestCancelRefusesAppend(t *testing.T) {
	m := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Append(ctx, Event{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
