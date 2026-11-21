package instance

import (
	"context"
	"testing"
	"time"
)

// TestManagementResponseEnvelope 锁定 T10 的管理面响应门限:创建 VM、
// 100 次状态快照与关闭均在固定预算内完成,超限直接失败。
func TestManagementResponseEnvelope(t *testing.T) {
	started := time.Now()
	machine, err := New(t.Context(), Options{Deterministic: true, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("VM 创建耗时 %v 超过 2s", elapsed)
	}
	started = time.Now()
	for index := 0; index < 100; index++ {
		if _, err := machine.Status(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("100 次状态查询耗时 %v 超过 2s", elapsed)
	}
	started = time.Now()
	if err := machine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("VM 关闭耗时 %v 超过 2s", elapsed)
	}
}
