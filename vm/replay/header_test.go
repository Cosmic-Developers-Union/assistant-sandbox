package replay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func sampleHeader() Header {
	return Header{
		Format:        HeaderFormat,
		Mode:          ModeRecord,
		Image:         "sha256:image",
		Program:       "/bin/init",
		ABI:           "builtin-go",
		InitialDigest: "sha256:initial",
	}
}

func TestHeaderEncodeDecodeRoundTrip(t *testing.T) {
	header := sampleHeader()
	data, err := EncodeHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeHeader(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != header {
		t.Fatalf("decoded=%+v want %+v", decoded, header)
	}

	filled, err := EncodeHeader(Header{Mode: ModeRecord, ABI: "builtin-go"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeHeader(filled)
	if err != nil || decoded.Format != HeaderFormat {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

func TestHeaderEncodeRejectsInvalidInput(t *testing.T) {
	if _, err := EncodeHeader(Header{Format: "assistant-sandbox-replay-header-0", Mode: ModeRecord}); !errors.Is(err, errno.Version) {
		t.Fatalf("旧格式应报 EPROTO: %v", err)
	}
	if _, err := EncodeHeader(Header{Format: HeaderFormat}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺少模式应报 EINVAL: %v", err)
	}
	if _, err := EncodeHeader(Header{Format: HeaderFormat, Mode: "whatever"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非法模式应报 EINVAL: %v", err)
	}
}

func TestDecodeHeaderRejectsCorruptionAndVersionMismatch(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"空数据", nil, errno.Invalid},
		{"非法 JSON", []byte("{"), errno.Invalid},
		{"未知字段", []byte(`{"format":"` + HeaderFormat + `","mode":"record","future":1}`), errno.Invalid},
		{"缺少模式", []byte(`{"format":"` + HeaderFormat + `"}`), errno.Invalid},
		{"非法模式", []byte(`{"format":"` + HeaderFormat + `","mode":"x"}`), errno.Invalid},
		{"版本不符", []byte(`{"format":"assistant-sandbox-replay-header-0","mode":"record"}`), errno.Version},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeHeader(tc.data); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
		})
	}
}

func TestCheckHeaderRejectsIdentityDrift(t *testing.T) {
	recorded := sampleHeader()
	if err := CheckHeader(recorded, recorded); err != nil {
		t.Fatalf("相同身份应通过: %v", err)
	}

	if err := CheckHeader(recorded, Header{Format: HeaderFormat, Mode: ModeRecord}); err != nil {
		t.Fatalf("留空期望不应约束: %v", err)
	}
	for _, drift := range []Header{
		{Format: HeaderFormat, Mode: ModeRecord, Image: "sha256:other"},
		{Format: HeaderFormat, Mode: ModeRecord, Program: "/bin/other"},
		{Format: HeaderFormat, Mode: ModeRecord, ABI: "wasi"},
		{Format: HeaderFormat, Mode: ModeRecord, InitialDigest: "sha256:other"},
	} {
		if err := CheckHeader(recorded, drift); !errors.Is(err, errno.Invalid) {
			t.Fatalf("身份漂移应报 EINVAL: %+v err=%v", drift, err)
		}
	}

	if err := CheckHeader(Header{Format: "other", Mode: ModeRecord}, Header{Format: HeaderFormat, Mode: ModeRecord}); !errors.Is(err, errno.Version) {
		t.Fatalf("格式不一致应报 EPROTO: %v", err)
	}

	if err := CheckHeader(Header{Format: HeaderFormat, Mode: ModeReplay}, Header{Format: HeaderFormat, Mode: ModeRecord}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("回放头不能作为来源: %v", err)
	}
}

func TestSetHeaderOnlyBeforeRecording(t *testing.T) {
	ctx := context.Background()
	journal := Record()
	if _, ok := journal.Header(); ok {
		t.Fatal("新 Journal 不应带事实头")
	}
	header := sampleHeader()
	if err := journal.SetHeader(ctx, header); err != nil {
		t.Fatal(err)
	}
	stored, ok := journal.Header()
	if !ok || stored != header {
		t.Fatalf("stored=%+v ok=%v", stored, ok)
	}

	if err := journal.SetHeader(ctx, header); err != nil {
		t.Fatal(err)
	}

	stored.ABI = "mutated"
	if again, _ := journal.Header(); again.ABI != header.ABI {
		t.Fatalf("Header 必须返回副本: %+v", again)
	}

	if err := journal.Record(ctx, Fact{PID: 1, Operation: OpEntropy}); err != nil {
		t.Fatal(err)
	}
	if err := journal.SetHeader(ctx, Header{Format: HeaderFormat, Mode: ModeRecord, ABI: "changed"}); !errors.Is(err, errno.Busy) {
		t.Fatalf("记录后应报 EBUSY: %v", err)
	}

	empty := Record()
	if err := empty.SetHeader(ctx, Header{Format: HeaderFormat, Mode: "x"}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非法模式应报 EINVAL: %v", err)
	}
	if err := empty.SetHeader(ctx, Header{Format: "old", Mode: ModeRecord}); !errors.Is(err, errno.Version) {
		t.Fatalf("旧格式应报 EPROTO: %v", err)
	}
}

func TestModeReflectsJournalState(t *testing.T) {
	if got := Record().Mode(); got != ModeRecord {
		t.Fatalf("Mode=%q want %q", got, ModeRecord)
	}
	if got := Replay(nil).Mode(); got != ModeReplay {
		t.Fatalf("Mode=%q want %q", got, ModeReplay)
	}
}

func TestSetHeaderCancelledContext(t *testing.T) {
	journal := Record()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := journal.SetHeader(ctx, sampleHeader()); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, ok := journal.Header(); ok {
		t.Fatal("取消后不应写入事实头")
	}
}

func TestHeaderRoundTripThroughJSONIsStable(t *testing.T) {
	first, err := EncodeHeader(sampleHeader())
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeHeader(sampleHeader())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Compare(string(first), string(second)) != 0 {
		t.Fatal("相同事实头必须编码一致")
	}
}
