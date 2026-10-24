package fd

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/eventfd"
)

func TestDescriptorLifecycleAndOwnership(t *testing.T) {
	table := New()
	var out bytes.Buffer
	table.Attach(2, Streams{Input: bytes.NewBufferString("input"), Output: &out, Error: &out})
	input, err := table.Get(t.Context(), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(input)
	if err != nil || string(data) != "input" {
		t.Fatal(string(data), err)
	}
	if _, err := input.Write([]byte("bad")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	output, err := table.Get(t.Context(), 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if err := table.Dup(t.Context(), 2, 1, 3); err != nil {
		t.Fatal(err)
	}
	if err := table.Close(t.Context(), 2, 1); err != nil {
		t.Fatal(err)
	}

	if _, err := table.Get(t.Context(), 2, 1); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	duplicate, _ := table.Get(t.Context(), 2, 3)
	if _, err := duplicate.Write([]byte("kept")); err != nil || out.String() != "kept" {
		t.Fatal(out.String(), err)
	}
	if _, err := table.Terminal(t.Context(), 2, 3); err != nil {
		t.Fatal(err)
	}
	if opened := table.Opened(2); len(opened) != 3 || opened[0] != 0 || opened[1] != 2 || opened[2] != 3 {
		t.Fatalf("opened=%v", opened)
	}
	if err := table.Detach(2); err != nil {
		t.Fatal(err)
	}

	if err := table.Dup(t.Context(), 2, 1, 3); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if err := table.Close(t.Context(), 2, 1); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if _, err := table.Terminal(t.Context(), 2, 1); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if err := table.Install(2, 5, &stream{}); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := table.Get(ctx, 2, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := table.Dup(ctx, 2, 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := table.Close(ctx, 2, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	table.Attach(3, Streams{})
	empty, _ := table.Get(t.Context(), 3, 0)
	if _, err := empty.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

type counted struct {
	closes atomic.Int32
}

func (c *counted) Read([]byte) (int, error)    { return 0, io.EOF }
func (c *counted) Write(p []byte) (int, error) { return len(p), nil }
func (c *counted) Close() error                { c.closes.Add(1); return nil }

func TestSharedReferenceSemantics(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	handle := &counted{}
	if err := table.Install(1, 4, handle); err != nil {
		t.Fatal(err)
	}

	if err := table.Install(1, 4, &counted{}); !errors.Is(err, errno.Exist) {
		t.Fatal(err)
	}
	if err := table.Dup(t.Context(), 1, 4, 5); err != nil {
		t.Fatal(err)
	}
	if err := table.Close(t.Context(), 1, 4); err != nil {
		t.Fatal(err)
	}

	if n := handle.closes.Load(); n != 0 {
		t.Fatalf("closes=%d", n)
	}
	shared, _ := table.Get(t.Context(), 1, 5)
	if _, err := shared.Write([]byte("alive")); err != nil {
		t.Fatal(err)
	}
	if err := table.Close(t.Context(), 1, 5); err != nil {
		t.Fatal(err)
	}

	if n := handle.closes.Load(); n != 1 {
		t.Fatalf("closes=%d", n)
	}

	if err := table.Install(1, 6, nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := table.Close(t.Context(), 1, 6); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}

	if err := table.Dup(t.Context(), 1, 7, 7); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}

	var borrowed closedProbe
	table.Attach(9, Streams{Output: &borrowed})
	if err := table.Detach(9); err != nil {
		t.Fatal(err)
	}
	if borrowed.closed {
		t.Fatal("标准流不应被关闭")
	}
}

type closedProbe struct{ closed bool }

func (p *closedProbe) Read([]byte) (int, error)  { return 0, io.EOF }
func (p *closedProbe) Write([]byte) (int, error) { return 0, io.EOF }
func (p *closedProbe) Close() error              { p.closed = true; return nil }

func TestCloexecMarker(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	if err := table.SetCloexec(1, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := table.SetCloexec(1, 9, true); !errors.Is(err, errno.BadHandle) {
		t.Fatal(err)
	}
	if err := table.SetCloexec(1, 0, false); err != nil {
		t.Fatal(err)
	}
}

func TestInstallNextAllocatesLowestFreeAboveStdio(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	first, err := table.InstallNext(1, &counted{})
	if err != nil || first != 3 {
		t.Fatalf("first=%d err=%v", first, err)
	}
	second, err := table.InstallNext(1, &counted{})
	if err != nil || second != 4 {
		t.Fatalf("second=%d err=%v", second, err)
	}

	if err := table.Close(t.Context(), 1, 3); err != nil {
		t.Fatal(err)
	}
	reused, err := table.InstallNext(1, &counted{})
	if err != nil || reused != 3 {
		t.Fatalf("reused=%d err=%v", reused, err)
	}

	if opened := table.Opened(1); !slices.Equal(opened, []uint32{0, 1, 2, 3, 4}) {
		t.Fatalf("opened=%v", opened)
	}

	if _, err := table.InstallNext(1, nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	empty := New()
	if _, err := empty.InstallNext(9, &counted{}); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
}

type tabStopTerminal struct {
	stream
	stops []uint16
}

func (t *tabStopTerminal) TabStops() []uint16               { return t.stops }
func (t *tabStopTerminal) SetTabStops(stops []uint16) error { t.stops = stops; return nil }

func TestTableTabStopForwarding(t *testing.T) {
	table := New()
	terminal := &tabStopTerminal{}
	table.Attach(1, Streams{})
	if err := table.Close(context.Background(), 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := table.Install(1, 0, terminal); err != nil {
		t.Fatal(err)
	}

	if stops := table.TabStops(); stops != nil {
		t.Fatalf("初始制表位 = %v", stops)
	}

	if err := table.SetTabStops([]uint16{4, 8}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(terminal.stops, []uint16{4, 8}) {
		t.Fatalf("终端收到 = %v", terminal.stops)
	}

	empty := New()
	empty.Attach(1, Streams{})
	if err := empty.SetTabStops([]uint16{4}); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("无能力 = %v", err)
	}
	if stops := empty.TabStops(); stops != nil {
		t.Fatalf("无能力读取 = %v", stops)
	}
}

func TestStreamReadContextForwarding(t *testing.T) {

	table := New()
	blocking := &blockingReader{}
	table.Attach(1, Streams{Input: blocking, Output: io.Discard})
	stream, err := table.Get(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.(interface {
		ReadContext(context.Context, []byte) (int, error)
	}); !ok {
		t.Fatal("流应暴露 ReadContext")
	}
	buffer := make([]byte, 2)
	n, err := stream.(interface {
		ReadContext(context.Context, []byte) (int, error)
	}).ReadContext(context.Background(), buffer)
	if err != nil || string(buffer[:n]) != "ok" {
		t.Fatalf("ReadContext = %q %v", buffer[:n], err)
	}

	if !table.IsOpen(1, 0) {
		t.Fatal("fd0 应打开")
	}
	if table.IsOpen(1, 9) {
		t.Fatal("fd9 未安装")
	}
}

type blockingReader struct{}

func (b *blockingReader) ReadContext(_ context.Context, p []byte) (int, error) {
	copy(p, "ok")
	return 2, nil
}
func (b *blockingReader) Read(p []byte) (int, error) { return 0, io.EOF }

func TestEventFDDescriptorLifecycle(t *testing.T) {
	ctx := context.Background()
	table := New()
	table.Attach(7, Streams{Input: strings.NewReader(""), Output: io.Discard, Error: io.Discard})

	descriptor, err := table.EventFD(7, 5, eventfd.Options{Semaphore: true})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := table.Get(ctx, 7, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var raw [8]byte
	if n, err := stream.Read(raw[:]); err != nil || n != 8 {
		t.Fatalf("读初值: n=%d err=%v", n, err)
	}
	if got := binary.LittleEndian.Uint64(raw[:]); got != 1 {
		t.Fatalf("信号量模式应返回 1, got %d", got)
	}
	if _, err := table.EventFD(99, 0, eventfd.Options{}); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}
}
