package yes

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type limitedWriter struct {
	remaining int
	data      strings.Builder
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, io.ErrClosedPipe
	}
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	w.remaining -= len(p)
	w.data.Write(p)
	return len(p), nil
}

func TestRepeatsDefaultLineUntilDownstreamCloses(t *testing.T) {
	writer := &limitedWriter{remaining: 7}
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), nil, nil, writer, io.Discard)
	if err != nil {
		t.Fatalf("下游关闭不应是命令错误: %v", err)
	}
	if got := writer.data.String(); got != "y\ny\ny\ny" {
		t.Fatalf("out=%q", got)
	}
}

func TestRepeatsJoinedOperands(t *testing.T) {

	writer := &limitedWriter{remaining: 2 * writeChunk * len("hello world\n")}
	if err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{"hello", "world"}, nil, writer, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("hello world\n", 2*writeChunk)
	if got := writer.data.String(); got != want {
		t.Fatalf("out=%q want %d 字节", got, len(want))
	}
}

func TestCancelledContextStopsOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	writer := &limitedWriter{remaining: 1024}
	err := commandtest.Invoke(ctx, Run, t.TempDir(), nil, nil, writer, io.Discard)
	if err == nil {
		t.Fatal("取消的上下文必须结束命令")
	}
	if writer.data.Len() != 0 {
		t.Fatalf("取消后不应产生输出: %q", writer.data.String())
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestNonPipeWriteErrorSurfaces(t *testing.T) {
	want := errors.New("输出设备故障")
	var diagnostic strings.Builder
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), nil, nil, errorWriter{err: want}, &diagnostic)
	if err == nil {
		t.Fatal("非管道错误必须结束命令")
	}
	if !strings.Contains(diagnostic.String(), want.Error()) {
		t.Fatalf("诊断=%q 未包含 %q", diagnostic.String(), want)
	}
}

func TestBrokenPipeFromGuestEndpointIsNormalTermination(t *testing.T) {

	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), nil, nil, errorWriter{err: errno.BrokenPipe}, io.Discard)
	if err != nil {
		t.Fatalf("客体管道断裂是正常终止: %v", err)
	}
}

func TestShortWriteIsRejected(t *testing.T) {
	var diagnostic strings.Builder
	if err := commandtest.Invoke(t.Context(), Run, t.TempDir(), nil, nil, zeroWriter{}, &diagnostic); err == nil {
		t.Fatal("零字节写入必须结束命令")
	}
	if !strings.Contains(diagnostic.String(), io.ErrShortWrite.Error()) {
		t.Fatalf("诊断=%q", diagnostic.String())
	}
}

func TestCancelledAfterWriteStopsNextBlock(t *testing.T) {

	ctx, cancel := context.WithCancel(t.Context())
	writer := &cancelWriter{cancel: cancel, remaining: writeChunk * 2}
	err := commandtest.Invoke(ctx, Run, t.TempDir(), nil, nil, writer, io.Discard)
	if err == nil {
		t.Fatal("取消后必须结束命令")
	}
	if !errors.Is(writer.ctxErr, context.Canceled) && ctx.Err() == nil {
		t.Fatalf("上下文未取消: %v", err)
	}
}

type cancelWriter struct {
	cancel    context.CancelFunc
	remaining int
	ctxErr    error
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	w.remaining -= len(p)
	if w.remaining <= 0 {
		w.cancel()
	}
	return len(p), nil
}
