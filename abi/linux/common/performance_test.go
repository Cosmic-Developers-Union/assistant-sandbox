package common

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// TestSyscallPerformanceEnvelope 锁定 T10 的最低系统调用吞吐:固定
// 配置下 10k 次 getpid 必须在 1s 内完成,超出即失败而不是延长超时。
func TestSyscallPerformanceEnvelope(t *testing.T) {
	space := newFakeMemory()
	table := fd.New()
	table.Attach(1, fd.Streams{Output: io.Discard})
	handler := Syscalls{Memory: space, Descriptors: ProcessDescriptors{Table: table, PID: 1}, Identity: &Identity{PID: 1}}
	call := Call{Syscall: SysGetPID}
	started := time.Now()
	for index := 0; index < 10_000; index++ {
		if reply := handler.Handle(context.Background(), call); reply.Value != 1 {
			t.Fatalf("getpid=%+v", reply)
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("10k getpid 耗时 %v 超过 1s 门限", elapsed)
	}
}
