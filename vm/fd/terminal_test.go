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
