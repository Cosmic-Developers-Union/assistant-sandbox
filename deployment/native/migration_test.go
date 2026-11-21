package native_test

import (
	"context"
	"testing"

	native "github.com/Cosmic-Developers-Union/assistant-sandbox/deployment/native"
)

// TestModelVMOriginalWorkflowsMigrate 验证 R08:命令执行、连续修改
// 文件、断开重连与取消等待都落到同一台真实 VM,结果明确。
func TestModelVMOriginalWorkflowsMigrate(t *testing.T) {
	machine, err := native.NewModelVM(t.Context(), native.Options{Seed: 1, RealClock: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Shutdown(context.Background()) }()
	terminal, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := terminal.Bash().Run(t.Context(), "printf one > /work/state; cat /work/state")
	if err != nil || result.ExitCode != 0 || result.Output != "one" {
		t.Fatalf("首次执行=%+v err=%v", result, err)
	}
	result, err = terminal.Bash().Run(t.Context(), "printf two > /work/state; printf three >> /work/state; cat /work/state")
	if err != nil || result.ExitCode != 0 || result.Output != "twothree" {
		t.Fatalf("连续修改=%+v err=%v", result, err)
	}
	if err := terminal.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	reconnected, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err = reconnected.Bash().Run(t.Context(), "cat /work/state")
	if err != nil || result.ExitCode != 0 || result.Output != "twothree" {
		t.Fatalf("重连后文件=%+v err=%v", result, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reconnected.Bash().Run(canceled, "printf never"); err == nil {
		t.Fatal("已取消的等待应返回错误")
	}
	result, err = reconnected.Bash().Run(t.Context(), "printf alive")
	if err != nil || result.ExitCode != 0 || result.Output != "alive" {
		t.Fatalf("取消后 VM 不可用=%+v err=%v", result, err)
	}
}
