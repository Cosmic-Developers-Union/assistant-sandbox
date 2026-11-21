package native_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	native "github.com/Cosmic-Developers-Union/assistant-sandbox/deployment/native"
)

// TestModelVMJobControlAcrossReconnect 验证 P13:管道、后台 &、wait、
// 停止/继续以及断开重连后的后台任务都符合契约。
func TestModelVMJobControlAcrossReconnect(t *testing.T) {
	machine, err := native.NewModelVM(t.Context(), native.Options{Seed: 1, RealClock: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Shutdown(context.Background()) }()
	terminal, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	probe, err := os.ReadFile("../../internal/guest/testdata/m-bgpid")
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.Filesystem().WriteFile(t.Context(), "/probe", probe); err != nil {
		t.Fatal(err)
	}
	run := func(script string) string {
		result, err := terminal.Bash().Run(t.Context(), script)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("%q => %+v err=%v", script, result, err)
		}
		return result.Output
	}
	if got := run("printf abc | cat"); got != "abc" {
		t.Fatalf("管道输出=%q", got)
	}
	if got := run("printf one > /work/bg & wait; cat /work/bg"); got != "one" {
		t.Fatalf("后台+wait=%q", got)
	}
	if got := run("chmod 755 /probe"); got != "" {
		t.Fatalf("chmod 输出=%q", got)
	}
	run("/probe &")
	var pid string
	deadline := time.Now().Add(2 * time.Second)
	for {
		if data, readErr := machine.Filesystem().ReadFile(t.Context(), "/work/pid"); readErr == nil {
			pid = strings.TrimSpace(string(data))
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("后台探针未写出 PID")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == "" {
		t.Fatal("后台 PID 为空")
	}
	for _, script := range []string{"kill -STOP " + pid, "kill -CONT " + pid, "kill -TERM " + pid} {
		if got := run(script); got != "" {
			t.Fatalf("%s 输出=%q", script, got)
		}
	}
	// 持续 Console 的后台任务在句柄断开后仍运行(P03/P10)。
	console, err := machine.Console().Attach(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := console.Bash().Run(t.Context(), "(sleep 0.1; printf later > /work/later) &")
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Console 后台启动=%+v err=%v", result, err)
	}
	if err := console.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	reconnected, err := machine.Console().Attach(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reconnected.Bash().Run(t.Context(), "cat /work/later"); err != nil || got.Output != "later" {
		t.Fatalf("重连后后台结果=%+v err=%v", got, err)
	}
}
