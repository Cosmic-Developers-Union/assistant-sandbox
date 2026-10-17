package language

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const StateFormat = "assistant-sandbox-language-state-1"

type State struct {
	Format   string          `json:"format"`
	Language string          `json:"language"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

func (s State) Encode() ([]byte, error) {
	if s.Language == "" {
		return nil, errno.Wrap(errno.Invalid, "语言标识不能为空")
	}
	encoded := s
	encoded.Format = StateFormat
	data, err := json.Marshal(encoded)
	if err != nil {
		return nil, errno.Wrap(err, "编码语言槽位状态")
	}
	return data, nil
}

func Decode(data []byte) (State, error) {
	if len(data) == 0 {
		return State{}, errno.Wrap(errno.Invalid, "语言槽位状态为空")
	}
	var state State
	decoder := json.NewDecoder(newReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {

		return State{}, errors.Join(errno.Wrap(err, "解析语言槽位状态"), errno.Invalid)
	}
	if state.Format != StateFormat {
		return State{}, errno.Wrap(errno.Version, "语言槽位状态格式为 "+state.Format)
	}
	if state.Language == "" {
		return State{}, errno.Wrap(errno.Invalid, "语言槽位状态缺少语言标识")
	}
	return state, nil
}

type Slot interface {
	Language() string

	Step(context.Context, []byte) (Output, error)

	Encode(context.Context) (State, error)

	Restore(context.Context, State) error

	Close(context.Context) error
}

type Output struct {
	Data []byte

	Status int

	Done bool
}

type Lifecycle uint8

const (
	Closed Lifecycle = iota

	Open

	Finished
)

func (l Lifecycle) String() string {
	switch l {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case Finished:
		return "finished"
	default:
		return "lifecycle(" + itoa(uint64(l)) + ")"
	}
}

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}

type Slots interface {
	Open(ctx context.Context, pid uint64, name string) (Slot, error)

	Lookup(ctx context.Context, pid uint64) (Slot, error)

	Close(ctx context.Context, pid uint64) error
}
