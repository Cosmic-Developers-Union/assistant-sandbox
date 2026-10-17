package sandbox

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/protocol"
)

type streamWriter struct{ closed atomic.Int32 }

func (*streamWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *streamWriter) Close() error              { w.closed.Add(1); return nil }

func TestCustomStreamProcessClose(t *testing.T) {
	input := &streamWriter{}
	var cleaned atomic.Int32
	sentinel := errors.New("执行环境清理失败")
	process, err := NewStreamProcess(input, io.NopCloser(strings.NewReader("response")), func() error { cleaned.Add(1); return sentinel })
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Stdin.Close(); err != nil {
		t.Error(err)
	}
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			if err := process.Close(); !errors.Is(err, sentinel) {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if input.closed.Load() != 1 || cleaned.Load() != 1 {
		t.Fatal("重复释放协议资源")
	}
	for _, tc := range []struct {
		input   io.WriteCloser
		output  io.ReadCloser
		cleanup func() error
	}{
		{nil, process.Stdout, func() error { return nil }}, {input, nil, func() error { return nil }}, {input, process.Stdout, nil},
	} {
		if _, err := NewStreamProcess(tc.input, tc.output, tc.cleanup); err == nil {
			t.Fatal("接受不完整协议进程")
		}
	}
}

func TestWASIStreamDuplexAndSecretBoundary(t *testing.T) {
	t.Setenv("AGENT_API_KEY", "never-inherit-model-key")
	runtime, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	process, err := runtime.Start(t.Context(), t.TempDir(), "assistant-bash", nil, []string{"MCP_TEST_TOKEN=allowed-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := io.WriteString(process.Stdin, `{"ID":1,"Command":"test -z \"$AGENT_API_KEY\" && test \"$MCP_TEST_TOKEN\" = allowed-secret && echo protocol","OutputLimit":1024}`+"\n"); err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	line, err := bufio.NewReader(process.Stdout).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(line, &response); err != nil || response.Code != 0 || response.Stdout != "protocol\n" {
		t.Fatal(response, err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestStreamValidationAndRegistry(t *testing.T) {
	runtime, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, tc := range []struct {
		command   string
		args, env []string
	}{
		{command: ""}, {command: "-bad"}, {command: "unregistered"}, {command: "assistant-bash", args: []string{"a\x00b"}}, {command: "assistant-bash", env: []string{"9INVALID=x"}}, {command: "assistant-bash", env: []string{"NOVALUE"}},
	} {
		if _, err := runtime.Start(t.Context(), t.TempDir(), tc.command, tc.args, tc.env); err == nil {
			t.Fatal(tc)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runtime.Start(ctx, t.TempDir(), "assistant-bash", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
