package entropy

import (
	"bytes"
	"context"
	"testing"
)

func TestProductionAvoidsFixedSeeds(t *testing.T) {
	ctx := context.Background()
	first := make([]byte, 32)
	second := make([]byte, 32)

	a := NewProduction()
	if n, err := a.Read(ctx, first); err != nil || n != 32 {
		t.Fatalf("读=%d err=%v", n, err)
	}
	b := NewProduction()
	if n, err := b.Read(ctx, second); err != nil || n != 32 {
		t.Fatalf("读=%d err=%v", n, err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("两次独立实例不应产生相同序列(不得使用固定种子)")
	}
	c := NewProduction()
	again := make([]byte, 32)
	if _, err := c.Read(ctx, again); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, again) {
		t.Fatal("同实例跨次读取不应重复")
	}
}

func TestProductionHonoursCancellationAndEmptyReads(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	var source Production
	if _, err := source.Read(cancelled, make([]byte, 8)); err == nil {
		t.Fatal("已取消上下文应报错")
	}
	if n, err := source.Read(context.Background(), nil); err != nil || n != 0 {
		t.Fatalf("空读=%d err=%v", n, err)
	}
}
