package guest_test

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// TestPython312InteractivePTY 验证 CPython 在真实 PTY 上进入 REPL、执行
// 输入并退出。readline 会把 SIGWINCH 旧处理器保存下来,rt_sigaction 若在
// 安装新处理器后才查询 oldact,handler 会递归调用自身并卡死。
func TestPython312InteractivePTY(t *testing.T) {
	machine, connection := python312Guest(t)
	master, err := machine.Filesystem().Open(t.Context(), "/dev/ptmx", vmfs.ReadWrite, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	slave, err := machine.Filesystem().Open(t.Context(), "/dev/pts/0", vmfs.ReadWrite, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slave.Close() }()

	process, err := connection.Exec(t.Context(), instance.ExecSpec{
		Program: "/usr/bin/python3.12",
		Args:    []string{"-S"},
		Dir:     "/work",
		Env: map[string]string{
			"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
			"PYTHONHOME": "/usr", "TERM": "xterm-256color",
		},
		Streams: fd.Streams{Input: slave, Output: slave, Error: slave},
	})
	if err != nil {
		t.Fatal(err)
	}

	output := &ptyOutput{changed: make(chan struct{}, 1)}
	go output.readFrom(master)
	if !output.waitFor(t.Context(), ">>> ", 30*time.Second) {
		t.Fatalf("等待 REPL 提示符超时, output=%q", output.String())
	}
	if _, err := master.Write([]byte("print(6*7)\n")); err != nil {
		t.Fatal(err)
	}
	if !output.waitFor(t.Context(), "42", 10*time.Second) {
		t.Fatalf("REPL 未执行输入, output=%q", output.String())
	}
	if !output.waitForCount(t.Context(), ">>> ", 2, 10*time.Second) {
		t.Fatalf("REPL 未返回提示符, output=%q", output.String())
	}
	if _, err := master.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	state, err := connection.Wait(waitCtx, process.ID)
	if err != nil {
		t.Fatalf("等待 Python 退出: %v, output=%q", err, output.String())
	}
	if state.ExitCode != 0 {
		t.Fatalf("Python 退出码=%d output=%q", state.ExitCode, output.String())
	}
}

type ptyOutput struct {
	mu      sync.Mutex
	content bytes.Buffer
	changed chan struct{}
}

func (o *ptyOutput) readFrom(reader interface{ Read([]byte) (int, error) }) {
	buffer := make([]byte, 4096)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			o.mu.Lock()
			_, _ = o.content.Write(buffer[:n])
			o.mu.Unlock()
			select {
			case o.changed <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

func (o *ptyOutput) waitFor(ctx context.Context, text string, timeout time.Duration) bool {
	return o.waitForCount(ctx, text, 1, timeout)
}

func (o *ptyOutput) waitForCount(ctx context.Context, text string, count int, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		o.mu.Lock()
		found := bytes.Count(o.content.Bytes(), []byte(text)) >= count
		o.mu.Unlock()
		if found {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-o.changed:
		}
	}
}

func (o *ptyOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.content.String()
}
