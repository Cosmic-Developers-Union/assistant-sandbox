package commandtest

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands"
)

func TestLimitedBufferCapsAndFlags(t *testing.T) {
	buffer := newLimitedBuffer(64, nil)
	if written, err := buffer.Write([]byte("hello")); written != 5 || err != nil {
		t.Fatalf("写入应始终成功: %d, %v", written, err)
	}
	if buffer.overflowed() || buffer.String() != "hello" {
		t.Fatalf("未超限时不应标记溢出: %q", buffer.String())
	}

	written, err := buffer.Write(bytes.Repeat([]byte("x"), 200))
	if written != 200 || err != nil {
		t.Fatalf("超限写入仍应返回成功长度: %d, %v", written, err)
	}
	if !buffer.overflowed() || len(buffer.String()) != 64 {
		t.Fatalf("溢出后应截断到上限并标记: %d, %v", len(buffer.String()), buffer.overflowed())
	}

	if _, err := buffer.Write([]byte("more")); err != nil {
		t.Fatal(err)
	}
	if len(buffer.String()) != 64 {
		t.Fatalf("溢出后不应继续增长: %d", len(buffer.String()))
	}
}

func TestUnboundedCommandIsCancelled(t *testing.T) {
	registry := commands.Registry(nil)
	handler, known := registry["yes"]
	if !known {
		t.Skip("`yes` 未注册")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	buffer := newLimitedBuffer(1<<16, cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = InvokeWith(ctx, handler, t.TempDir(), nil, strings.NewReader(""), buffer, newLimitedBuffer(1<<16, cancel), Options{})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("无界输出没有被取消：命令仍在写，测试会挂住")
	}
	if !buffer.overflowed() {
		t.Fatal("上限没有生效：命令在预算内就被取消了")
	}
}
