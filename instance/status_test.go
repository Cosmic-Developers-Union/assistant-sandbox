package instance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestVMStatusReportsUnifiedInspection 验证 T01:统一状态列出 VM 状态、
// 进程/线程、等待类别、fd、预算、网络、轨迹与存储,且只含值类型。
func TestVMStatusReportsUnifiedInspection(t *testing.T) {
	hold := executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		<-ctx.Done()
		return 0, nil
	})
	machine, err := New(t.Context(), Options{
		Deterministic: true,
		Seed:          1,
		Session:       resources.Limits{Memory: 64 << 20, Work: 1 << 30, Descriptors: 64},
		Process:       resources.Limits{Memory: 32 << 20, Work: 1 << 28, Descriptors: 32},
		Programs:      map[string]executable.Program{"hold": hold},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	file, err := machine.Filesystem().Open(t.Context(), "/work/status-data", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(t.Context())
	process, err := connection.Exec(runCtx, ExecSpec{Program: "hold", Args: []string{"arg"}, Streams: fd.Streams{}})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	var status Status
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, err = machine.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if entry, ok := findProcess(status, process.ID); ok && entry.Budget.WorkUsed > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("进程未出现在状态快照: %+v", status.Processes)
		}
		time.Sleep(time.Millisecond)
	}
	entry, ok := findProcess(status, process.ID)
	if !ok {
		t.Fatalf("缺少进程 %d: %+v", process.ID, status.Processes)
	}
	if entry.Program != "hold" || len(entry.Args) != 1 || entry.Args[0] != "arg" {
		t.Fatalf("进程身份=%+v", entry)
	}
	if entry.TID != entry.ID || entry.TGID != entry.ID {
		t.Fatalf("线程标识=%+v", entry)
	}
	if entry.State != "running" && entry.State != "waiting" && entry.State != "ready" {
		t.Fatalf("进程状态=%q", entry.State)
	}
	if entry.Budget.WorkLimit == 0 || entry.Budget.MemoryLimit == 0 || entry.Budget.DescriptorLimit == 0 {
		t.Fatalf("进程预算=%+v", entry.Budget)
	}
	if len(entry.FDs) == 0 || !containsFD(entry.FDs, 0) || !containsFD(entry.FDs, 1) {
		t.Fatalf("进程 fd=%v", entry.FDs)
	}
	if status.State != StateRunning || status.Connections != 1 {
		t.Fatalf("VM 状态=%+v", status)
	}
	if status.Session.MemoryLimit == 0 || status.Session.WorkLimit == 0 || status.Session.DescriptorLimit == 0 {
		t.Fatalf("会话预算=%+v", status.Session)
	}
	if len(status.Storage) == 0 {
		t.Fatalf("缺少存储快照: %+v", status)
	}
	foundBytes := false
	for _, space := range status.Storage {
		if space.Bytes.Used >= 5 {
			foundBytes = true
		}
	}
	if !foundBytes {
		t.Fatalf("存储未计入写入: %+v", status.Storage)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("状态不可序列化: %v", err)
	}
	if !strings.Contains(string(encoded), `"program":"hold"`) || strings.Contains(string(encoded), "Resources") {
		t.Fatalf("序列化含内部字段: %s", encoded)
	}

	// 取消执行并回收:进程从状态中消失,fd 随退出关闭。
	cancel()
	if _, err := connection.Wait(t.Context(), process.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		status, err = machine.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := findProcess(status, process.ID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("回收后进程仍在状态中: %+v", status.Processes)
		}
		time.Sleep(time.Millisecond)
	}

	if err := machine.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, err = machine.Status(t.Context())
	if err != nil || status.State != StateClosed {
		t.Fatalf("关闭后状态=%+v err=%v", status, err)
	}
}

func findProcess(status Status, pid proc.PID) (ProcessStatus, bool) {
	for _, entry := range status.Processes {
		if entry.ID == uint64(pid) {
			return entry, true
		}
	}
	return ProcessStatus{}, false
}

func containsFD(fds []uint32, want uint32) bool {
	for _, value := range fds {
		if value == want {
			return true
		}
	}
	return false
}
