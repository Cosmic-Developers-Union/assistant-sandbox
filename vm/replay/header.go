package replay

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const HeaderFormat = "assistant-sandbox-replay-header-1"

const (
	ModeRecord = "record"
	ModeReplay = "replay"
)

type Header struct {
	Format  string `json:"format"`
	Mode    string `json:"mode"`
	Image   string `json:"image,omitempty"`
	Program string `json:"program,omitempty"`
	ABI     string `json:"abi,omitempty"`

	InitialDigest string `json:"initial_digest,omitempty"`
}

func EncodeHeader(header Header) ([]byte, error) {
	if header.Format == "" {
		header.Format = HeaderFormat
	}
	if header.Format != HeaderFormat {
		return nil, errno.Wrap(errno.Version, "事实头格式为 "+header.Format)
	}
	if header.Mode != ModeRecord && header.Mode != ModeReplay {
		return nil, errno.Wrap(errno.Invalid, "事实头模式非法: "+header.Mode)
	}
	return json.Marshal(header)
}

func DecodeHeader(data []byte) (Header, error) {
	if len(data) == 0 {
		return Header{}, errno.Invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var header Header
	if err := decoder.Decode(&header); err != nil {
		return Header{}, errno.Wrap(errno.Invalid, "解析事实头: "+err.Error())
	}
	if header.Format != HeaderFormat {
		return Header{}, errno.Wrap(errno.Version, "事实头格式为 "+header.Format)
	}
	if header.Mode != ModeRecord && header.Mode != ModeReplay {
		return Header{}, errno.Wrap(errno.Invalid, "事实头模式非法: "+header.Mode)
	}
	return header, nil
}

func CheckHeader(recorded, expected Header) error {
	if recorded.Format != expected.Format {
		return errno.Wrap(errno.Version, "事实头格式不一致")
	}
	if recorded.Mode != ModeRecord {
		return errno.Wrap(errno.Invalid, "只有记录模式的事实头可以用于回放")
	}
	for _, field := range []struct{ name, recorded, expected string }{
		{"镜像", recorded.Image, expected.Image},
		{"程序", recorded.Program, expected.Program},
		{"ABI", recorded.ABI, expected.ABI},
		{"初始状态", recorded.InitialDigest, expected.InitialDigest},
	} {
		if field.expected != "" && field.recorded != field.expected {
			return errno.Wrap(errno.Invalid, "事实头"+field.name+"不一致")
		}
	}
	return nil
}

func (m *Memory) SetHeader(ctx context.Context, header Header) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if header.Format == "" {
		header.Format = HeaderFormat
	}
	if header.Format != HeaderFormat {
		return errno.Wrap(errno.Version, "事实头格式为 "+header.Format)
	}
	if header.Mode != ModeRecord && header.Mode != ModeReplay {
		return errno.Wrap(errno.Invalid, "事实头模式非法: "+header.Mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.facts) > 0 || m.pos > 0 {
		return errno.Busy
	}
	m.header = &header
	return nil
}

func (m *Memory) Header() (Header, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.header == nil {
		return Header{}, false
	}
	return *m.header, true
}

func (m *Memory) Mode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record {
		return ModeRecord
	}
	return ModeReplay
}
