package language

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestStateEncodeForcesFormat(t *testing.T) {

	data, err := State{Format: "伪造", Language: "bash", Payload: json.RawMessage(`{"cwd":"/work"}`)}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), StateFormat) {
		t.Fatalf("编码应写入当前格式: %s", data)
	}
	if strings.Contains(string(data), "伪造") {
		t.Fatalf("不得保留调用方伪造的格式: %s", data)
	}
	decoded, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Format != StateFormat || decoded.Language != "bash" {
		t.Fatalf("往返结果=%+v", decoded)
	}

	var payload map[string]string
	if err := json.Unmarshal(decoded.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["cwd"] != "/work" {
		t.Fatalf("负载=%v", payload)
	}
}

func TestEncodeRequiresLanguage(t *testing.T) {
	if _, err := (State{}).Encode(); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺少语言标识应报 EINVAL: %v", err)
	}
}

func TestDecodeRejectsBadStates(t *testing.T) {
	for _, tc := range []struct {
		label string
		data  string
		want  error
	}{
		{"空数据", "", errno.Invalid},
		{"非 JSON", "not json", errno.Invalid},
		{"缺格式", `{"language":"bash"}`, errno.Version},
		{"格式不符", `{"format":"other-1","language":"bash"}`, errno.Version},
		{"缺语言", `{"format":"` + StateFormat + `"}`, errno.Invalid},
		{"未知字段", `{"format":"` + StateFormat + `","language":"bash","extra":1}`, errno.Invalid},
	} {
		if _, err := Decode([]byte(tc.data)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err=%v want %v", tc.label, err, tc.want)
		}
	}
}

func TestLifecycleNamesAreStable(t *testing.T) {
	for lifecycle, want := range map[Lifecycle]string{Closed: "closed", Open: "open", Finished: "finished"} {
		if got := lifecycle.String(); got != want {
			t.Errorf("Lifecycle(%d).String()=%q want %q", lifecycle, got, want)
		}
	}
	if got := Lifecycle(42).String(); got != "lifecycle(42)" {
		t.Fatalf("未知生命周期=%q", got)
	}
}

type probeSlot struct {
	name    string
	state   Lifecycle
	steps   int
	payload []byte
}

func (s *probeSlot) Language() string { return s.name }

func (s *probeSlot) Step(ctx context.Context, input []byte) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	switch s.state {
	case Closed:
		return Output{}, errno.Wrap(errno.Cancelled, "槽位已关闭")
	case Finished:
		return Output{}, errno.Wrap(errno.Busy, "槽位已结束")
	}
	s.steps++
	s.payload = append(s.payload, input...)
	return Output{Data: input, Done: false}, nil
}

func encodePayload(raw []byte) json.RawMessage {
	encoded, err := json.Marshal(string(raw))
	if err != nil {
		panic(err)
	}
	return encoded
}

func decodePayload(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		panic(err)
	}
	return []byte(value)
}

func (s *probeSlot) Encode(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if s.state == Closed {
		return State{}, errno.Wrap(errno.Cancelled, "槽位已关闭")
	}

	encoded, err := State{Language: s.name, Payload: encodePayload(s.payload)}.Encode()
	if err != nil {
		return State{}, err
	}
	return Decode(encoded)
}

func (s *probeSlot) Restore(ctx context.Context, state State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.state == Closed {
		return errno.Wrap(errno.Cancelled, "槽位已关闭")
	}
	if state.Language != s.name {
		return errno.Wrap(errno.Invalid, "状态语言与槽位不符")
	}
	s.payload = decodePayload(state.Payload)
	return nil
}

func (s *probeSlot) Close(context.Context) error {
	s.state = Closed
	return nil
}

func TestSlotContractIsImplementable(t *testing.T) {
	ctx := context.Background()
	slot := &probeSlot{name: "probe", state: Open}
	var contract Slot = slot
	if contract.Language() != "probe" {
		t.Fatalf("language=%q", contract.Language())
	}
	output, err := contract.Step(ctx, []byte("echo"))
	if err != nil || string(output.Data) != "echo" {
		t.Fatalf("output=%+v err=%v", output, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := contract.Step(cancelled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := contract.Encode(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}

	state, err := contract.Encode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := &probeSlot{name: "probe", state: Open}
	if err := restored.Restore(ctx, state); err != nil {
		t.Fatal(err)
	}
	if string(restored.payload) != "echo" {
		t.Fatalf("恢复负载=%q", restored.payload)
	}

	if err := restored.Restore(ctx, State{Language: "other"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("跨语言恢复应报 EINVAL: %v", err)
	}

	if err := contract.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.Step(ctx, nil); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Step 应报 ECANCELED: %v", err)
	}
	if _, err := contract.Encode(ctx); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Encode 应报 ECANCELED: %v", err)
	}

	finished := &probeSlot{name: "probe", state: Finished}
	if _, err := finished.Step(ctx, nil); !errors.Is(err, errno.Busy) {
		t.Fatalf("已结束槽位应报 EBUSY: %v", err)
	}
}
