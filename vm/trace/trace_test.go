package trace

import (
	"testing"
)

// TestRecorderKeepsRecentEntriesWithinLimit 验证 G08:轨迹条数有界——
// 超出上限时丢弃最旧条目并计数,内存占用不随调用次数增长。
func TestRecorderKeepsRecentEntriesWithinLimit(t *testing.T) {
	recorder := New(4)
	if recorder.Limit() != 4 {
		t.Fatalf("上限=%d want 4", recorder.Limit())
	}
	for index := 0; index < 10; index++ {
		recorder.Record(Entry{PID: 7, TID: 7, Number: uint64(index), Result: int64(index)})
	}
	if recorder.Len() != 4 {
		t.Fatalf("保留条数=%d want 4", recorder.Len())
	}
	if recorder.Dropped() != 6 {
		t.Fatalf("丢弃计数=%d want 6", recorder.Dropped())
	}
	entries := recorder.Entries()
	if entries[0].Number != 6 || entries[3].Number != 9 {
		t.Fatalf("保留的应是最近条目: %+v", entries)
	}
	// 缩小上限时同样丢弃最旧条目。
	recorder.SetLimit(2)
	if recorder.Len() != 2 {
		t.Fatalf("缩小后保留=%d want 2", recorder.Len())
	}
	if entries := recorder.Entries(); entries[0].Number != 8 || entries[1].Number != 9 {
		t.Fatalf("缩小后保留的应是最近条目: %+v", entries)
	}
	if recorder.Dropped() != 8 {
		t.Fatalf("丢弃计数=%d want 8", recorder.Dropped())
	}
}

// TestRecorderDefaultLimit 验证默认上限生效:非正上限回落到默认值。
func TestRecorderDefaultLimit(t *testing.T) {
	recorder := New(0)
	if recorder.Limit() != DefaultLimit {
		t.Fatalf("默认上限=%d want %d", recorder.Limit(), DefaultLimit)
	}
	recorder.SetLimit(-1)
	if recorder.Limit() != DefaultLimit {
		t.Fatalf("非法上限=%d want %d", recorder.Limit(), DefaultLimit)
	}
}

// TestRecorderKeepsIdentifiersAndTiming 验证轨迹只含标识、调用编号、
// 结果与耗时——不含任何路径或缓冲区内容。
func TestRecorderKeepsIdentifiersAndTiming(t *testing.T) {
	recorder := New(8)
	recorder.Record(Entry{PID: 11, TID: 12, Number: 64, Result: 5, Duration: Duration(3_000_000)})
	entries := recorder.Entries()
	if len(entries) != 1 {
		t.Fatalf("条数=%d want 1", len(entries))
	}
	entry := entries[0]
	if entry.PID != 11 || entry.TID != 12 || entry.Number != 64 || entry.Result != 5 {
		t.Fatalf("轨迹内容=%+v", entry)
	}
	if entry.Duration != Duration(3_000_000) {
		t.Fatalf("耗时=%v want 3ms", entry.Duration)
	}
}
