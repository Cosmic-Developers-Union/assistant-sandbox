package vm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
)

func stdinKernel(t *testing.T, journal *replay.Memory, input io.Reader) (*Kernel, proc.Proc) {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:      capability.Set{FD: fd.New()},
		Programs: registry,
		Journal:  journal,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process
}

func TestKernelReadRecordsAndReplaysStdin(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	kernel, process := stdinKernel(t, journal, bytes.NewReader([]byte("hello world")))
	buffer := make([]byte, 5)
	n, err := kernel.ReadFD(ctx, process.ID, 0, buffer)
	if err != nil || string(buffer[:n]) != "hello" {
		t.Fatalf("n=%d buffer=%q err=%v", n, buffer[:n], err)
	}
	facts := journal.Facts()
	if len(facts) != 1 || facts[0].Operation != replay.OpStdin {
		t.Fatalf("应记录一条 stdin 事实: %+v", facts)
	}

	if len(facts[0].Input) != 6 || facts[0].Input[1] != 0 || facts[0].Input[5] != 5 {
		t.Fatalf("请求编码不符: %v", facts[0].Input)
	}

	replayKernel, replayProcess := stdinKernel(t, replay.Replay(facts), failingReader{})
	replayed := make([]byte, 5)
	n, err = replayKernel.ReadFD(ctx, replayProcess.ID, 0, replayed)
	if err != nil || string(replayed[:n]) != "hello" {
		t.Fatalf("回放 n=%d buffer=%q err=%v", n, replayed[:n], err)
	}
}

func TestKernelReadRecordsEveryReadOutcome(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()

	kernel, process := stdinKernel(t, journal, bytes.NewReader(nil))
	if _, err := kernel.ReadFD(ctx, process.ID, 0, make([]byte, 4)); err == nil {
		t.Fatal("EOF 应上报")
	}
	if len(journal.Facts()) != 1 {
		t.Fatalf("EOF 应记录为一条事实: %+v", journal.Facts())
	}

	terminalJournal := replay.Record()
	terminalKernel, terminalProcess := stdinKernel(t, terminalJournal, nil)
	if err := terminalKernel.InstallFD(terminalProcess.ID, 0, wouldBlockStream{}); err != nil {
		t.Fatal(err)
	}
	if _, err := terminalKernel.ReadFD(ctx, terminalProcess.ID, 0, make([]byte, 4)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("应报 EWOULDBLOCK: %v", err)
	}
	facts := terminalJournal.Facts()
	if len(facts) != 1 || len(facts[0].Output) != 1 || facts[0].Output[0] != readStatusWouldBlock {
		t.Fatalf("EWOULDBLOCK 必须记录为独立状态: %+v", facts)
	}
	replayKernel, replayProcess := stdinKernel(t, replay.Replay(facts), bytes.NewReader(nil))
	if _, err := replayKernel.ReadFD(ctx, replayProcess.ID, 0, make([]byte, 4)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("回放应返回 EWOULDBLOCK: %v", err)
	}
}

func TestKernelReadReplayDetectsRequestShapeMismatch(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	kernel, process := stdinKernel(t, journal, bytes.NewReader([]byte("longer than request")))
	if _, err := kernel.ReadFD(ctx, process.ID, 0, make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	replayKernel, replayProcess := stdinKernel(t, replay.Replay(journal.Facts()), bytes.NewReader(nil))
	if _, err := replayKernel.ReadFD(ctx, replayProcess.ID, 0, make([]byte, 4)); err == nil {
		t.Fatal("请求长度不一致必须失败")
	} else if !errors.Is(err, errno.Invalid) {
		t.Fatalf("分歧应带 EINVAL 身份: %v", err)
	}
}

func TestKernelReadReplayDetectsDifferentDescriptor(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	kernel, process := stdinKernel(t, journal, bytes.NewReader([]byte("first stream")))
	if err := kernel.InstallFD(process.ID, 3, &bytesReadCloser{data: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.ReadFD(ctx, process.ID, 0, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.ReadFD(ctx, process.ID, 3, make([]byte, 6)); err != nil {
		t.Fatal(err)
	}

	replayKernel, replayProcess := stdinKernel(t, replay.Replay(journal.Facts()), bytes.NewReader(nil))
	if _, err := replayKernel.ReadFD(ctx, replayProcess.ID, 3, make([]byte, 6)); err == nil {
		t.Fatal("描述符不一致必须失败")
	}
}

func TestKernelReadReplayReportsRecordedFailure(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	kernel, process := stdinKernel(t, journal, failingReader{})
	if _, err := kernel.ReadFD(ctx, process.ID, 0, make([]byte, 4)); err == nil {
		t.Fatal("失败读取应上报")
	}
	replayKernel, replayProcess := stdinKernel(t, replay.Replay(journal.Facts()), bytes.NewReader(nil))
	if _, err := replayKernel.ReadFD(ctx, replayProcess.ID, 0, make([]byte, 4)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("回放的失败事实应报 EINVAL: %v", err)
	}

	broken := replay.Replay([]replay.Fact{{PID: uint64(replayProcess.ID), Operation: replay.OpStdin, Input: encodeReadRequest(0, 4)}})
	brokenKernel, brokenProcess := stdinKernel(t, broken, bytes.NewReader(nil))
	if _, err := brokenKernel.ReadFD(ctx, brokenProcess.ID, 0, make([]byte, 4)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空事实输出应报 EINVAL: %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("真实输入不应被读取") }

type wouldBlockStream struct{}

func (wouldBlockStream) Read([]byte) (int, error)    { return 0, errno.WouldBlock }
func (wouldBlockStream) Write(p []byte) (int, error) { return len(p), nil }
func (wouldBlockStream) Close() error                { return nil }

type bytesReadCloser struct {
	data   []byte
	offset int
}

func (s *bytesReadCloser) Read(p []byte) (int, error) {
	if s.offset >= len(s.data) {
		return 0, errno.NotFound
	}
	n := copy(p, s.data[s.offset:])
	s.offset += n
	return n, nil
}
func (s *bytesReadCloser) Write(p []byte) (int, error) { return len(p), nil }
func (s *bytesReadCloser) Close() error                { return nil }
