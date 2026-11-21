package instance

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestIsolationAndFaultAcceptance 验证 U08:资源耗尽、恶意 ELF、
// 故障信号、后台死循环与强制关闭都被限制在单台 VM,管理面仍响应。
func TestIsolationAndFaultAcceptance(t *testing.T) {
	quick := executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 7, nil })
	hold := executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		<-ctx.Done()
		return 0, nil
	})
	options := func(seed uint64, work uint64) Options {
		return Options{
			Deterministic: true, Seed: seed, MaxProcesses: 4,
			Session:  resources.Limits{Memory: 128 << 20, Work: 1 << 30},
			Process:  resources.Limits{Memory: 32 << 20, Work: work},
			Programs: map[string]executable.Program{"quick": quick, "hold": hold},
		}
	}
	first, err := New(t.Context(), options(1, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(context.Background()) }()
	second, err := New(t.Context(), options(2, 1<<30))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()
	fault, err := os.ReadFile("../internal/guest/testdata/m-fault")
	if err != nil {
		t.Fatal(err)
	}
	writeInstanceFile(t, first, "/spin", guest.SpinForeverProbe())
	writeInstanceFile(t, second, "/spin", guest.SpinForeverProbe())
	writeInstanceFile(t, first, "/fault", fault)
	writeInstanceFile(t, first, "/bad", []byte("not an ELF image"))
	firstConnection, err := first.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secondConnection, err := second.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// CPU 预算耗尽只终止目标进程,管理面仍可执行。
	spin, err := firstConnection.Exec(t.Context(), ExecSpec{Program: "/spin", Streams: fd.Streams{}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := firstConnection.Wait(t.Context(), spin.ID)
	if err != nil || state.ExitCode == 0 {
		t.Fatalf("死循环未被限制: %+v err=%v", state, err)
	}
	assertQuick(t, firstConnection, "预算耗尽后第一台 VM 不可用")

	// 畸形 ELF 被拒绝,VM 不被拖垮。
	if _, err := firstConnection.Exec(t.Context(), ExecSpec{Program: "/bad", Streams: fd.Streams{}}); err == nil {
		t.Fatal("畸形 ELF 不应启动")
	}
	assertQuick(t, firstConnection, "畸形 ELF 后第一台 VM 不可用")

	// 进程上限只影响本 VM;第二台仍可执行。
	holdCtx, cancelHold := context.WithCancel(t.Context())
	firstHold, err := firstConnection.Exec(holdCtx, ExecSpec{Program: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	secondHold, err := firstConnection.Exec(holdCtx, ExecSpec{Program: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstConnection.Exec(t.Context(), ExecSpec{Program: "hold"}); !errors.Is(err, errno.Limit) {
		t.Fatalf("进程上限 err=%v want EAGAIN", err)
	}
	assertQuick(t, secondConnection, "第二台 VM 受第一台进程耗尽影响")
	cancelHold()
	if _, err := firstConnection.Wait(t.Context(), firstHold.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := firstConnection.Wait(t.Context(), secondHold.ID); err != nil {
		t.Fatal(err)
	}

	// 后台死循环期间强制关闭第二台 VM 有界完成,第一台继续可用。
	secondSpin, err := secondConnection.Exec(t.Context(), ExecSpec{Program: "/spin", Streams: fd.Streams{}})
	if err != nil {
		t.Fatal(err)
	}
	_ = secondSpin
	time.Sleep(10 * time.Millisecond)
	closeCtx, cancelClose := context.WithTimeout(t.Context(), 2*time.Second)
	started := time.Now()
	if err := second.Close(closeCtx); err != nil {
		cancelClose()
		t.Fatalf("强制关闭: %v", err)
	}
	cancelClose()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("强制关闭耗时=%v", elapsed)
	}
	assertQuick(t, firstConnection, "关闭另一台 VM 后第一台不可用")

	// 致命故障把第一台标记为 failed,但不影响新建 VM。
	faultProcess, err := firstConnection.Exec(t.Context(), ExecSpec{Program: "/fault", Streams: fd.Streams{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstConnection.Wait(t.Context(), faultProcess.ID); err != nil {
		t.Fatal(err)
	}
	status, err := first.Status(t.Context())
	if err != nil || status.State != StateRunning || len(status.Crashes) == 0 {
		t.Fatalf("故障后状态=%+v err=%v", status, err)
	}
	assertQuick(t, firstConnection, "客体崩溃后第一台 VM 不可用")
	third, err := New(t.Context(), options(3, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = third.Close(context.Background()) }()
	thirdConnection, err := third.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertQuick(t, thirdConnection, "故障 VM 拖垮了新 VM")
}

func assertQuick(t *testing.T, connection *Connection, message string) {
	t.Helper()
	process, err := connection.Exec(t.Context(), ExecSpec{Program: "quick"})
	if err != nil {
		t.Fatalf("%s: %v", message, err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil || state.ExitCode != 7 {
		t.Fatalf("%s: %+v err=%v", message, state, err)
	}
}

func writeInstanceFile(t *testing.T, machine *VM, name string, content []byte) {
	t.Helper()
	file, err := machine.Filesystem().Open(t.Context(), name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
