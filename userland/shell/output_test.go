package shell

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSlotOutputHistorySeparatesSourcesAndSequences 验证 P06/P07:前台
// 与后台输出进入同一 Console 历史,来源可区分、序列边界单调;前台
// Result 不混入后台输出。
func TestSlotOutputHistorySeparatesSourcesAndSequences(t *testing.T) {
	slot := newTestSlot(t, t.TempDir())
	output, err := slot.Step(context.Background(), []byte("printf front; printf back &"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(output.Data); got != "front" {
		t.Fatalf("前台结果=%q want 仅 front(后台输出不得混入)", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	var chunks []OutputChunk
	for {
		chunks = slot.Output(0)
		hasFront, hasBack := false, false
		for _, chunk := range chunks {
			hasFront = hasFront || (chunk.Source == OutputForeground && strings.Contains(string(chunk.Data), "front"))
			hasBack = hasBack || (chunk.Source == OutputBackground && strings.Contains(string(chunk.Data), "back"))
		}
		if hasFront && hasBack {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("输出历史缺来源片段: %+v", chunks)
		}
		time.Sleep(time.Millisecond)
	}
	var previous uint64
	for _, chunk := range chunks {
		if chunk.Sequence <= previous {
			t.Fatalf("序列未单调递增: %+v", chunks)
		}
		previous = chunk.Sequence
	}
	// 增量读取:自上次序号之后只返回新片段。
	if _, err := slot.Step(context.Background(), []byte("printf tail")); err != nil {
		t.Fatal(err)
	}
	incremental := slot.Output(previous)
	if len(incremental) != 1 || incremental[0].Source != OutputForeground || string(incremental[0].Data) != "tail" {
		t.Fatalf("增量输出=%+v", incremental)
	}
	if got := slot.Output(incremental[0].Sequence); len(got) != 0 {
		t.Fatalf("末尾之后应无片段: %+v", got)
	}
}
