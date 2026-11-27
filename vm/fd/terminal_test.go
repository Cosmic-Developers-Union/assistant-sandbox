package fd

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type sizedStream struct{ rows, columns uint16 }

func (s *sizedStream) Read([]byte) (int, error)    { return 0, nil }
func (s *sizedStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *sizedStream) Close() error                { return nil }
func (s *sizedStream) Dimensions() (uint16, uint16) {
	return s.rows, s.columns
}

// wrappedStream 模拟作用域包装器:总是提供 Dimensions,但用
// IsTerminal 明确否认终端身份。
type wrappedStream struct {
	sizedStream
	terminal bool
}

func (s *wrappedStream) IsTerminal() bool { return s.terminal }

type foregroundStream struct {
	sizedStream
	foreground uint64
}

func (s *foregroundStream) Foreground() uint64 { return s.foreground }
func (s *foregroundStream) SetForeground(pgid uint64) error {
	s.foreground = pgid
	return nil
}
func (s *foregroundStream) ForegroundSupported() bool { return true }

func TestStandardStreamForwardsTerminalCapabilities(t *testing.T) {
	ctx := context.Background()
	table := New()
	terminal := &foregroundStream{sizedStream: sizedStream{rows: 30, columns: 90}, foreground: 7}
	table.Attach(1, Streams{Input: terminal, Output: terminal, Error: terminal})

	info, err := table.Terminal(ctx, 1, 0)
	if err != nil || !info.TTY || info.Rows != 30 || info.Columns != 90 {
		t.Fatalf("标准流终端信息 = %+v err=%v", info, err)
	}
	handle, err := table.Get(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	foreground, ok := handle.(ForegroundTerminal)
	if !ok || !foreground.ForegroundSupported() || foreground.Foreground() != 7 {
		t.Fatalf("前台进程组转发 = %+v", handle)
	}
	if err := foreground.SetForeground(9); err != nil || terminal.foreground != 9 {
		t.Fatalf("设置前台进程组: %v foreground=%d", err, terminal.foreground)
	}
}

func TestTerminalQueryReportsOnlyRealTerminals(t *testing.T) {
	ctx := context.Background()
	table := New()
	table.Attach(1, Streams{})

	for _, id := range []uint32{0, 1, 2} {
		info, err := table.Terminal(ctx, 1, id)
		if err != nil || info.TTY || info.Rows != 0 || info.Columns != 0 {
			t.Fatalf("标准流 %d 不应是终端: %+v err=%v", id, info, err)
		}
	}

	plain, err := table.InstallNext(1, &counted{})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := table.Terminal(ctx, 1, plain); err != nil || info.TTY {
		t.Fatalf("普通对象不应是终端: %+v err=%v", info, err)
	}

	terminal, err := table.InstallNext(1, &sizedStream{rows: 24, columns: 80})
	if err != nil {
		t.Fatal(err)
	}
	info, err := table.Terminal(ctx, 1, terminal)
	if err != nil {
		t.Fatal(err)
	}
	if !info.TTY || info.Rows != 24 || info.Columns != 80 {
		t.Fatalf("终端信息不符: %+v", info)
	}

	wrapped, err := table.InstallNext(1, &wrappedStream{sizedStream: sizedStream{rows: 10, columns: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := table.Terminal(ctx, 1, wrapped); err != nil || info.TTY {
		t.Fatalf("包装后的普通句柄不应是终端: %+v err=%v", info, err)
	}
	realWrapped, err := table.InstallNext(1, &wrappedStream{terminal: true, sizedStream: sizedStream{rows: 30, columns: 90}})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := table.Terminal(ctx, 1, realWrapped); err != nil || !info.TTY || info.Rows != 30 || info.Columns != 90 {
		t.Fatalf("包装后的终端应被识别: %+v err=%v", info, err)
	}

	stream := table.entries[1][terminal].ref.handle.(*sizedStream)
	stream.rows, stream.columns = 40, 120
	if info, _ := table.Terminal(ctx, 1, terminal); info.Rows != 40 || info.Columns != 120 {
		t.Fatalf("尺寸未更新: %+v", info)
	}
}

// TestNestedStandardStreamDoesNotInheritTerminal 验证标准流再包一层
// 时不会把普通管道误判为 TTY:内层 stream 虽然实现 Dimensions,
// 但外层的 IsTerminal 标记必须优先。
func TestNestedStandardStreamDoesNotInheritTerminal(t *testing.T) {
	ctx := context.Background()
	outer := New()
	outer.Attach(1, Streams{})
	stdin, err := outer.Get(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}

	inner := New()
	inner.Attach(1, Streams{Input: stdin})
	info, err := inner.Terminal(ctx, 1, 0)
	if err != nil || info.TTY {
		t.Fatalf("嵌套标准流不应是终端: %+v err=%v", info, err)
	}
}

func TestTerminalQueryErrors(t *testing.T) {
	ctx := context.Background()
	table := New()
	table.Attach(1, Streams{})
	if _, err := table.Terminal(ctx, 9, 0); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}
	if _, err := table.Terminal(ctx, 1, 42); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("无效描述符应报 EBADF: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := table.Terminal(cancelled, 1, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}

	terminal, err := table.InstallNext(1, &sizedStream{rows: 1, columns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Close(ctx, 1, terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Terminal(ctx, 1, terminal); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("已关闭描述符应报 EBADF: %v", err)
	}
}
