package auditjsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func decode(t *testing.T, output string) []Record {
	t.Helper()
	var records []Record
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("解析 %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestSinkWritesOrderedJSONL(t *testing.T) {
	var buffer bytes.Buffer
	sink, err := New(&buffer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, event := range []audit.Event{
		{Sequence: 1, PID: 1, Kind: "spawn", Payload: []byte("init")},
		{Sequence: 2, PID: 2, Kind: "exit", Payload: []byte("code=0")},
	} {
		if err := sink.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	records := decode(t, buffer.String())
	if len(records) != 2 {
		t.Fatalf("records=%v", records)
	}
	if records[0].Version != FormatVersion || records[0].Sequence != 1 || records[0].PID != 1 ||
		records[0].Kind != "spawn" || records[0].Payload != "init" || records[0].Redacted {
		t.Fatalf("第一条=%+v", records[0])
	}
	if records[1].Kind != "exit" || records[1].PID != 2 {
		t.Fatalf("第二条=%+v", records[1])
	}
	if sink.Written() != 2 {
		t.Fatalf("written=%d", sink.Written())
	}
}

func TestSinkRejectsNonMonotonicSequence(t *testing.T) {
	var buffer bytes.Buffer
	sink, err := New(&buffer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sink.Append(ctx, audit.Event{Sequence: 1, PID: 1, Kind: "a"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []uint64{0, 1, 5, 2} {
		if err := sink.Append(ctx, audit.Event{Sequence: bad, PID: 1, Kind: "b"}); !errors.Is(err, errno.Invalid) {
			t.Fatalf("序号 %d 应被拒绝: %v", bad, err)
		}
	}

	if err := sink.Append(ctx, audit.Event{Sequence: 2, PID: 1, Kind: "c"}); err == nil {
		t.Fatal("失败后必须继续失败")
	}
	_ = sink.Close()
	if records := decode(t, buffer.String()); len(records) != 1 {
		t.Fatalf("只应写出第一条: %v", records)
	}
}

func TestSinkRedactCanRewriteAndDrop(t *testing.T) {
	var buffer bytes.Buffer
	sink, err := New(&buffer, nil, func(record Record) (Record, bool) {
		if record.Kind == "secret" {
			return Record{}, false
		}
		record.Payload = "[已过滤]"
		return record, true
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sink.Append(ctx, audit.Event{Sequence: 1, PID: 1, Kind: "spawn", Payload: []byte("原始负载")}); err != nil {
		t.Fatal(err)
	}

	if err := sink.Append(ctx, audit.Event{Sequence: 2, PID: 1, Kind: "secret", Payload: []byte("机密")}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Append(ctx, audit.Event{Sequence: 3, PID: 1, Kind: "exit"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	records := decode(t, buffer.String())
	if len(records) != 2 {
		t.Fatalf("被丢弃事件不应写入: %v", records)
	}
	if records[0].Payload != "[已过滤]" || !records[0].Redacted {
		t.Fatalf("改写后应标记 Redacted: %+v", records[0])
	}

	if records[0].Sequence != 1 || records[0].PID != 1 || records[0].Kind != "spawn" {
		t.Fatalf("过滤不得改身份: %+v", records[0])
	}
	if records[1].Sequence != 3 || records[1].Kind != "exit" {
		t.Fatalf("第三条=%+v", records[1])
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSinkReportsWriteFailure(t *testing.T) {
	want := errors.New("磁盘故障")
	sink, err := New(failingWriter{err: want}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	appendErr := sink.Append(context.Background(), audit.Event{Sequence: 1, PID: 1, Kind: "a"})
	if appendErr == nil || !errors.Is(appendErr, want) {
		t.Fatalf("写入失败应上报: %v", appendErr)
	}
	if err := sink.Close(); !errors.Is(err, want) {
		t.Fatalf("Close 应保留失败原因: %v", err)
	}
}

func TestSinkRejectsInvalidConstructionAndClosedUse(t *testing.T) {
	if _, err := New(nil, nil, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 写入端应报 EINVAL: %v", err)
	}
	var buffer bytes.Buffer
	sink, err := New(&buffer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sink.Append(cancelled, audit.Event{Sequence: 1, PID: 1, Kind: "a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close 必须幂等: %v", err)
	}
	if err := sink.Append(context.Background(), audit.Event{Sequence: 1, PID: 1, Kind: "a"}); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后应报 ECANCELED: %v", err)
	}
}

type countingCloser struct{ closes int }

func (c *countingCloser) Close() error { c.closes++; return nil }

func TestSinkClosesCloser(t *testing.T) {
	var buffer bytes.Buffer
	closer := &countingCloser{}
	sink, err := New(&buffer, closer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if closer.closes != 1 {
		t.Fatalf("closer 应只关闭一次: %d", closer.closes)
	}
}

func TestSinkRecordsKernelAuditEvents(t *testing.T) {
	var buffer bytes.Buffer
	sink, err := New(&buffer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set:      capability.Set{FD: fd.New(), Audit: sink},
		Programs: auditRegistry(t),
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Exec(context.Background(), "noop", nil); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	records := decode(t, buffer.String())
	if len(records) < 2 {
		t.Fatalf("应记录 spawn 与 exit: %v", records)
	}

	for i, record := range records {
		if record.Sequence != uint64(i+1) {
			t.Fatalf("第 %d 条序号=%d", i, record.Sequence)
		}
		if record.Version != FormatVersion || record.Kind == "" {
			t.Fatalf("第 %d 条=%+v", i, record)
		}
	}
	if records[0].Kind != "spawn" {
		t.Fatalf("第一条应是 spawn: %+v", records[0])
	}
}

func auditRegistry(t *testing.T) executable.Registry {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
