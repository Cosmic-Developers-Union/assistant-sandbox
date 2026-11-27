package instance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// TestInitSpecBootsProgramAsPID1 验证发行版 init 入口:指定 Init 后
// VM 不创建内置 Bash 会话,PID 1 直接执行给定程序。
func TestInitSpecBootsProgramAsPID1(t *testing.T) {
	release := make(chan struct{})
	hold := executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})
	machine, err := New(t.Context(), Options{
		Deterministic: true,
		Seed:          7,
		Init:          &InitSpec{Program: "hold", Dir: "/"},
		Programs:      map[string]executable.Program{"hold": hold},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	defer close(release)

	initPID := machine.Kernel().Init()
	var initProcess proc.Proc
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, process := range machine.Kernel().Processes() {
			if process.ID == initPID {
				initProcess = process
				break
			}
		}
		if initProcess.Program == "hold" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PID 1 未启动 init: %+v", initProcess)
		}
		time.Sleep(time.Millisecond)
	}
	if initProcess.Parent != 0 || initProcess.State != proc.Running {
		t.Fatalf("PID 1=%+v", initProcess)
	}
	if len(machine.Kernel().Processes()) != 1 {
		t.Fatalf("Init 模式不应创建额外会话进程: %+v", machine.Kernel().Processes())
	}
	status := readProcFile(t, machine, t.Context(), "/proc/1/status")
	if !strings.Contains(status, "Name:\thold\n") || !strings.Contains(status, "Pid:\t1\n") {
		t.Fatalf("status=%q", status)
	}
}
